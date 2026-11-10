package shell

import (
	"errors"
	"fmt"
	"time"
)

// ErrIdentityUnavailable means the configured identity cannot be assumed by
// this process. Starting the shell would silently run it as the service user,
// so configuration fails closed instead.
var ErrIdentityUnavailable = errors.New("requested identity cannot be assumed")

// ErrRuntimeUnsupported means a runtime restriction was requested on a platform
// that does not implement it. The product targets Linux; this exists so a
// development host cannot pretend the restriction happened.
var ErrRuntimeUnsupported = errors.New("runtime restriction is not supported on this platform")

// Limits are the Linux resource limits applied to every shell process.
//
// A zero field means "leave this limit as the service inherited it". Limits are
// applied by util-linux prlimit, which lowers the limit and then executes the
// shell, so descendants inherit the same bound.
type Limits struct {
	// CPU bounds CPU time (RLIMIT_CPU), rounded up to whole seconds.
	CPU time.Duration
	// AddressSpaceBytes bounds virtual memory (RLIMIT_AS).
	AddressSpaceBytes int64
	// MaxProcesses bounds processes for the service user (RLIMIT_NPROC).
	MaxProcesses int64
	// MaxFileSizeBytes bounds one file a script may create (RLIMIT_FSIZE).
	MaxFileSizeBytes int64
	// MaxOpenFiles bounds open file descriptors (RLIMIT_NOFILE).
	MaxOpenFiles int64
}

// IsZero reports whether no limit is configured.
func (l Limits) IsZero() bool {
	return l.CPU == 0 && l.AddressSpaceBytes == 0 && l.MaxProcesses == 0 &&
		l.MaxFileSizeBytes == 0 && l.MaxOpenFiles == 0
}

// Validate rejects negative and absurd values. It does not decide whether a
// limit is operationally suitable; that belongs to the deployment.
func (l Limits) Validate() error {
	switch {
	case l.CPU < 0:
		return fmt.Errorf("%w: CPU limit must not be negative", ErrInvalidConfig)
	case l.CPU > 24*time.Hour:
		return fmt.Errorf("%w: CPU limit must not exceed 24h", ErrInvalidConfig)
	case l.AddressSpaceBytes < 0:
		return fmt.Errorf("%w: address-space limit must not be negative", ErrInvalidConfig)
	case l.MaxProcesses < 0:
		return fmt.Errorf("%w: process limit must not be negative", ErrInvalidConfig)
	case l.MaxFileSizeBytes < 0:
		return fmt.Errorf("%w: file-size limit must not be negative", ErrInvalidConfig)
	case l.MaxOpenFiles < 0:
		return fmt.Errorf("%w: open-file limit must not be negative", ErrInvalidConfig)
	}
	return nil
}

// args renders the prlimit flags in a stable order.
func (l Limits) args() []string {
	args := make([]string, 0, 5)
	if l.CPU > 0 {
		seconds := int64((l.CPU + time.Second - 1) / time.Second)
		args = append(args, fmt.Sprintf("--cpu=%d", seconds))
	}
	if l.AddressSpaceBytes > 0 {
		args = append(args, fmt.Sprintf("--as=%d", l.AddressSpaceBytes))
	}
	if l.MaxProcesses > 0 {
		args = append(args, fmt.Sprintf("--nproc=%d", l.MaxProcesses))
	}
	if l.MaxFileSizeBytes > 0 {
		args = append(args, fmt.Sprintf("--fsize=%d", l.MaxFileSizeBytes))
	}
	if l.MaxOpenFiles > 0 {
		args = append(args, fmt.Sprintf("--nofile=%d", l.MaxOpenFiles))
	}
	return args
}

// ValidateRuntime checks the identity and resource-limit configuration without
// starting a shell. A deployment should call it at startup so a boundary it
// cannot enforce is a startup failure, not a surprise on the first task.
func ValidateRuntime(runAs string, limits Limits, prlimit string) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	_, err := prepareRuntimeConfig(Config{RunAs: runAs, Limits: limits, Prlimit: prlimit})
	return err
}

// prepareConfig validates the generic session settings and then the
// platform-specific runtime settings.
func prepareConfig(cfg Config) (Config, error) {
	if err := cfg.Limits.Validate(); err != nil {
		return Config{}, err
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return prepareRuntimeConfig(cfg)
}
