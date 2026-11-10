//go:build !linux

package shell

import (
	"fmt"
	"os/exec"
)

// This file is a development-host stub, not a supported implementation. Asking
// for an identity or a resource limit here fails instead of quietly running
// without the boundary the caller requested.
func prepareRuntimeConfig(cfg Config) (Config, error) {
	if cfg.RunAs != "" || !cfg.Limits.IsZero() {
		return Config{}, fmt.Errorf("%w: identity and resource limits require Linux", ErrRuntimeUnsupported)
	}
	return cfg, nil
}

func newShellCommand(cfg Config, args ...string) (*exec.Cmd, error) {
	command := exec.Command(cfg.Executable, args...)
	command.Dir = cfg.WorkDir
	command.Env = environ(cfg.Env)
	if err := configureProcess(command, cfg); err != nil {
		return nil, err
	}
	return command, nil
}

func prepareConsoleDirectory(string, Config) error { return nil }
