//go:build linux

package shell

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the shell in its own process group so that the whole
// group can be signalled later.
func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
