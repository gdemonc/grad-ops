package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrConsoleDead means the persistent shell is gone: it was killed after a
// timeout, the script replaced it with exec, or it exited on its own. A dead
// console is never restarted behind the caller's back, because silently losing
// the state the caller built up would be worse than the error.
var ErrConsoleDead = errors.New("persistent shell is no longer running")

// endMarker prefixes the line the console writes when a call finishes. The
// script's own output goes to files, so anything else on the console's stdout
// is either a framing failure or a bug.
const endMarker = "__GRADOPS_CALL_END__"

// Console is a persistent Bash process shared by several calls.
//
// Unlike Session, which starts a fresh shell (and, when enabled, a fresh
// cgroup and bubblewrap namespace) for every script, a Console creates its
// cgroup and sandbox once and keeps them for the lifetime of the Console:
// environment, working directory, shell functions and background jobs survive
// between calls. It is scoped to one investigation's probe sequence, not to
// independent actions; the executor does not use a Console because an approved
// contract must not depend on what an earlier, unrelated script left behind.
type Console struct {
	cfg Config

	dir    string
	cmd    *exec.Cmd
	cgroup *cgroupGuard
	stdin  io.WriteCloser
	reader *bufio.Reader
	diag   *cappedBuffer
	waited chan error

	mu     sync.Mutex
	calls  int
	dead   bool
	closed bool
}

// NewConsole validates cfg and starts a persistent shell.
func NewConsole(cfg Config) (*Console, error) {
	cfg, err := prepareConfig(cfg)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp(cfg.ScratchDir, "gradops-console-")
	if err != nil {
		return nil, fmt.Errorf("console scratch directory: %w", err)
	}
	if err := prepareConsoleDirectory(dir, cfg); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	command, err := newShellCommand(cfg, "--noprofile", "--norc", "-s")
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("prepare console: %w", err)
	}
	guard, err := newCgroupGuard(cfg.Cgroup)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("prepare console cgroup: %w", err)
	}
	if err := guard.attach(command); err != nil {
		_ = guard.close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("attach console cgroup: %w", err)
	}

	stdin, err := command.StdinPipe()
	if err != nil {
		_ = guard.close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("console stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = guard.close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("console stdout: %w", err)
	}
	diag := newCappedBuffer(cfg.MaxOutputBytes)
	command.Stderr = diag

	if err := command.Start(); err != nil {
		_ = guard.close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start console: %w", err)
	}

	console := &Console{
		cfg:    cfg,
		dir:    dir,
		cmd:    command,
		cgroup: guard,
		stdin:  stdin,
		reader: bufio.NewReaderSize(stdout, 4096),
		diag:   diag,
		waited: make(chan error, 1),
	}
	go func() { console.waited <- command.Wait() }()
	return console, nil
}

// Alive reports whether the shell is still usable.
func (c *Console) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.dead && !c.closed
}

// Calls reports how many scripts the console has run.
func (c *Console) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Close shuts the shell down. It is safe to call more than once.
func (c *Console) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	dead := c.dead
	c.dead = true
	c.mu.Unlock()

	if !dead {
		// Sending EOF makes Bash exit on its own terms; a shell that ignores it
		// is killed, and the wait below is what actually reaps it.
		_ = c.stdin.Close()
		select {
		case <-c.waited:
		case <-time.After(killGrace):
			if c.cmd.Process != nil {
				killProcess(c.cmd)
			}
			<-c.waited
		}
	} else {
		_ = c.stdin.Close()
	}

	var closeErr error
	if c.cgroup != nil {
		closeErr = c.cgroup.close()
		c.cgroup = nil
	}
	if err := os.RemoveAll(c.dir); err != nil && closeErr == nil {
		closeErr = err
	}
	return closeErr
}

// Run executes one script in the persistent shell.
//
// The script's stdout and stderr are captured through files, so the console's
// own stdout stays clean for framing. A script that ends the shell, or one that
// exceeds the timeout, leaves the console dead; the caller gets ErrConsoleDead
// on the next call and has to start a new one.
func (c *Console) Run(ctx context.Context, script string) (Result, error) {
	if strings.TrimSpace(script) == "" {
		return Result{}, fmt.Errorf("%w: script must not be empty", ErrInvalidConfig)
	}

	c.mu.Lock()
	switch {
	case c.closed:
		c.mu.Unlock()
		return Result{}, ErrConsoleDead
	case c.dead:
		c.mu.Unlock()
		return Result{}, ErrConsoleDead
	case c.calls >= c.cfg.MaxCalls:
		c.mu.Unlock()
		return Result{}, ErrBudgetExhausted
	}
	c.calls++
	call := c.calls
	c.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	outPath := filepath.Join(c.dir, fmt.Sprintf("call-%d.out", call))
	errPath := filepath.Join(c.dir, fmt.Sprintf("call-%d.err", call))
	defer os.Remove(outPath)
	defer os.Remove(errPath)

	started := time.Now()

	// The group redirects the script's streams into their own files, captures
	// the exit status without letting it end the shell, and prints the marker
	// the reader waits for. Nothing here is derived from caller input except
	// the script, which is placed between the braces verbatim.
	framed := fmt.Sprintf(
		"__gradops_out=%s; __gradops_err=%s; __gradops_rc=0\n{\n%s\n} >\"${__gradops_out}\" 2>\"${__gradops_err}\" || __gradops_rc=$?\nprintf '%s%%s\\n' \"${__gradops_rc}\"\n",
		shellQuote(outPath), shellQuote(errPath), script, endMarker,
	)
	if _, err := io.WriteString(c.stdin, framed); err != nil {
		c.markDead()
		return Result{}, fmt.Errorf("write to console: %w", err)
	}

	done := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			readErr <- err
			return
		}
		done <- line
	}()

	var codeLine string
	select {
	case codeLine = <-done:
	case err := <-readErr:
		c.markDead()
		if errors.Is(err, io.EOF) {
			return Result{}, fmt.Errorf("%w: the script ended the shell%s", ErrConsoleDead, c.diagnosticSuffix())
		}
		return Result{}, fmt.Errorf("read from console: %w", err)
	case <-callCtx.Done():
		c.markDead()
		killProcess(c.cmd)
		if c.cgroup != nil {
			if err := c.cgroup.kill(); err != nil {
				return Result{
					Script:   script,
					ExitCode: -1,
					Stdout:   c.readCapped(outPath),
					Stderr:   c.readCapped(errPath),
					TimedOut: true,
					Duration: time.Since(started),
					Call:     call,
				}, err
			}
		}
		return Result{
			Script:   script,
			ExitCode: -1,
			Stdout:   c.readCapped(outPath),
			Stderr:   c.readCapped(errPath),
			TimedOut: true,
			Duration: time.Since(started),
			Call:     call,
		}, nil
	}

	exitCode := parseEndMarker(codeLine)
	if exitCode < 0 {
		c.markDead()
		return Result{}, fmt.Errorf("%w: unexpected output %q%s", ErrConsoleDead, strings.TrimSpace(codeLine), c.diagnosticSuffix())
	}

	result := Result{
		Script:   script,
		ExitCode: exitCode,
		Stdout:   c.readCapped(outPath),
		Stderr:   c.readCapped(errPath),
		Duration: time.Since(started),
		Call:     call,
	}
	result.StdoutTruncated, result.StderrTruncated = c.truncated(outPath), c.truncated(errPath)

	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

// markDead records that the shell can no longer be trusted to be where the
// framing thinks it is.
func (c *Console) markDead() {
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()
}

// readCapped reads a captured stream up to the configured limit. The limit is
// enforced while reading rather than after, so a script that fills the disk
// with output cannot make the service allocate it.
func (c *Console) readCapped(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	data, err := io.ReadAll(&cappedReader{reader: io.LimitReader(file, int64(c.cfg.MaxOutputBytes)+1)})
	if err != nil {
		return ""
	}
	if len(data) > c.cfg.MaxOutputBytes {
		data = data[:c.cfg.MaxOutputBytes]
	}
	return string(data)
}

// truncated reports whether the captured stream exceeded the limit.
func (c *Console) truncated(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > int64(c.cfg.MaxOutputBytes)
}

// cappedReader exists only to satisfy io.ReadAll's interface; the real bound is
// the LimitReader around the file.
type cappedReader struct {
	reader io.Reader
}

func (r *cappedReader) Read(p []byte) (int, error) { return r.reader.Read(p) }

// parseEndMarker reads the exit code out of the marker line, or -1 when the
// line is not the marker.
func parseEndMarker(line string) int {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, endMarker) {
		return -1
	}
	code, err := strconv.Atoi(strings.TrimPrefix(line, endMarker))
	if err != nil {
		return -1
	}
	return code
}

// shellQuote makes a path safe to place in the framed script: the value is a
// path this package created, but quoting keeps it correct for paths that
// contain spaces (a temporary directory may).
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Diagnostics is the shell's own stderr: whatever it said while starting or
// while rejecting a script. The script's stderr is captured separately.
func (c *Console) Diagnostics() string {
	if c.diag == nil {
		return ""
	}
	return c.diag.String()
}

// diagnosticSuffix appends the shell's own complaints to an error, so a failure
// is not just "the shell is gone".
func (c *Console) diagnosticSuffix() string {
	diag := strings.TrimSpace(c.Diagnostics())
	if diag == "" {
		return ""
	}
	if len(diag) > 200 {
		diag = diag[:200] + "..."
	}
	return ": " + diag
}
