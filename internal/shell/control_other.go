//go:build !linux

package shell

import "os/exec"

// This file exists only so the session logic can be exercised on a development
// machine. The product targets Linux: the real isolation, process-group
// signalling and identity enforcement live in the linux-tagged implementation.
// Nothing here should be read as support for another platform.

func configureProcess(command *exec.Cmd, _ Config) error {
	return nil
}

func killProcess(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	// Development hosts cannot signal a group portably; killing the direct
	// child is the best available approximation and is deliberately weaker
	// than the Linux behaviour.
	_ = command.Process.Kill()
}
