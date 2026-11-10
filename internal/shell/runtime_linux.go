//go:build linux

package shell

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// defaultPrlimit is where util-linux installs prlimit on the supported
// distributions. A deployment can override it, but an empty value must not
// silently turn limits off.
const defaultPrlimit = "/usr/bin/prlimit"

// prepareRuntimeConfig resolves the optional identity and checks the tool that
// applies resource limits. Both are configuration errors rather than runtime
// surprises: a deployment that asks for a boundary it cannot enforce must not
// start.
func prepareRuntimeConfig(cfg Config) (Config, error) {
	cfg.RunAs = strings.TrimSpace(cfg.RunAs)
	if cfg.RunAs != "" {
		if _, err := credentialFor(cfg.RunAs); err != nil {
			return Config{}, err
		}
	}

	if cfg.Limits.IsZero() {
		// No limits were configured, so prlimit is not part of the command.
		cfg.Prlimit = ""
		return cfg, nil
	}

	cfg.Prlimit = strings.TrimSpace(cfg.Prlimit)
	if cfg.Prlimit == "" {
		cfg.Prlimit = defaultPrlimit
	}
	if !filepath.IsAbs(cfg.Prlimit) {
		return Config{}, fmt.Errorf("%w: prlimit must be an absolute path", ErrInvalidConfig)
	}
	info, err := os.Stat(cfg.Prlimit)
	if err != nil {
		return Config{}, fmt.Errorf("%w: prlimit %q: %v", ErrInvalidConfig, cfg.Prlimit, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return Config{}, fmt.Errorf("%w: prlimit %q is not an executable file", ErrInvalidConfig, cfg.Prlimit)
	}

	return cfg, nil
}

// newShellCommand builds the process that will run the script. When limits are
// configured, prlimit is the process that execs Bash; prlimit applies the
// limits to itself and keeps the same pid, so process-group signalling and
// exit status still describe the shell.
func newShellCommand(cfg Config, args ...string) (*exec.Cmd, error) {
	if cfg.Limits.IsZero() != (cfg.Prlimit == "") {
		return nil, fmt.Errorf("%w: resource limits and prlimit must be configured together", ErrInvalidConfig)
	}

	executable := cfg.Executable
	commandArgs := args
	if cfg.Prlimit != "" {
		limitArgs := cfg.Limits.args()
		limitArgs = append(limitArgs, "--", cfg.Executable)
		limitArgs = append(limitArgs, args...)
		executable = cfg.Prlimit
		commandArgs = limitArgs
	}

	var command *exec.Cmd
	if cfg.Sandbox.Enabled {
		sandboxed, err := sandboxArgs(cfg, executable, commandArgs)
		if err != nil {
			return nil, err
		}
		command = exec.Command(cfg.Sandbox.Bubblewrap, sandboxed...)
	} else {
		command = exec.Command(executable, commandArgs...)
	}

	command.Dir = cfg.WorkDir
	command.Env = environ(cfg.Env)
	if err := configureProcess(command, cfg); err != nil {
		return nil, err
	}
	return command, nil
}

// credentialFor returns the credential needed to run as runAs. A nil credential
// means the requested identity is already the current one.
//
// Switching is only possible when the service is root. The alternative — run as
// the service account while the contract says otherwise — is exactly the silent
// downgrade this function exists to prevent.
func credentialFor(runAs string) (*syscall.Credential, error) {
	account, err := lookupAccount(runAs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityUnavailable, err)
	}

	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return nil, fmt.Errorf("%w: user %q has unusable uid %q", ErrIdentityUnavailable, runAs, account.Uid)
	}
	if uid == 0 {
		return nil, fmt.Errorf("%w: root is not an allowed shell identity", ErrIdentityUnavailable)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return nil, fmt.Errorf("%w: user %q has unusable gid %q", ErrIdentityUnavailable, runAs, account.Gid)
	}

	if uid == os.Geteuid() {
		return nil, nil
	}
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("%w: %q requires uid %d but the service runs as uid %d",
			ErrIdentityUnavailable, runAs, uid, os.Geteuid())
	}

	groupIDs, err := account.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("%w: supplementary groups for %q: %v", ErrIdentityUnavailable, runAs, err)
	}
	groups := make([]uint32, 0, len(groupIDs))
	for _, raw := range groupIDs {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: group %q for user %q is not numeric", ErrIdentityUnavailable, raw, runAs)
		}
		groups = append(groups, uint32(value))
	}

	return &syscall.Credential{
		Uid:    uint32(uid),
		Gid:    uint32(gid),
		Groups: groups,
	}, nil
}

// lookupAccount accepts a login name or a numeric uid.
func lookupAccount(runAs string) (*user.User, error) {
	if account, err := user.Lookup(runAs); err == nil {
		return account, nil
	}
	if _, err := strconv.Atoi(runAs); err == nil {
		if account, err := user.LookupId(runAs); err == nil {
			return account, nil
		}
	}
	return nil, fmt.Errorf("unknown user %q", runAs)
}

// prepareConsoleDirectory gives the persistent shell's scratch directory to the
// identity that will write into it. Session does not need this: it captures
// output through pipes, not files.
func prepareConsoleDirectory(dir string, cfg Config) error {
	if cfg.RunAs == "" {
		return nil
	}
	credential, err := credentialFor(cfg.RunAs)
	if err != nil {
		return err
	}
	if credential == nil {
		return nil
	}
	if err := os.Chown(dir, int(credential.Uid), int(credential.Gid)); err != nil {
		return fmt.Errorf("console scratch directory owner: %w", err)
	}
	return nil
}
