//go:build linux

package shell

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the shell in its own process group so that the whole
// group can be signalled later, and applies the configured identity when the
// process is allowed to switch to it.
func configureProcess(command *exec.Cmd, cfg Config) error {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if cfg.RunAs != "" {
		credential, err := credentialFor(cfg.RunAs)
		if err != nil {
			return err
		}
		attr.Credential = credential
	}
	command.SysProcAttr = attr
	return nil
}

// killProcess terminates the shell and everything it started.
//
// Signalling the negative pid addresses the process group. Killing only the
// shell would leave background jobs running after their turn ended, which is
// exactly the escape this function exists to prevent.
func killProcess(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	_ = command.Process.Kill()
}
