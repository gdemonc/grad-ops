package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gdemonc/grad-ops/internal/fileops"
)

// executeFileTarget performs a contract's bound file operation through the
// opened fd. The path is never handed back to a shell.
func (s *Service) executeFileTarget(ctx context.Context, proposal storedProposal, actor string) (Outcome, error) {
	target := proposal.Contract.FileTarget
	started := s.now().UTC()
	outcome := Outcome{
		ProposalID: proposal.ID,
		TaskID:     proposal.TaskID,
		Status:     StatusSucceeded,
	}

	// A file operation has no shell credential switch. Refuse rather than
	// writing as the service account while the contract names another user.
	if !identityMatches(proposal.Contract.RunAs) {
		outcome.Status = StatusFailed
		outcome.Stderr = fmt.Sprintf("file target requires run_as %q, current process is not that identity", proposal.Contract.RunAs)
		finished := s.now().UTC()
		outcome.Duration = finished.Sub(started)
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := s.record(recordCtx, proposal, outcome, started, finished, actor); err != nil {
			return outcome, err
		}
		return outcome, nil
	}

	bound := fileops.Target{
		Root:         target.Root,
		RootIdentity: fileops.Identity{Dev: target.RootDevice, Ino: target.RootInode},
		Relative:     target.Relative,
		Identity:     fileops.Identity{Dev: target.Device, Ino: target.Inode},
	}
	var runErr error
	switch target.Operation {
	case "append":
		runErr = bound.Append([]byte(target.Data))
		outcome.Stdout = fmt.Sprintf("appended %d bytes to %s/%s", len(target.Data), target.Root, target.Relative)
	default:
		runErr = fmt.Errorf("unsupported file target operation %q", target.Operation)
	}
	finished := s.now().UTC()
	outcome.Duration = finished.Sub(started)

	switch {
	case runErr == nil:
		// Succeeded.
	case errors.Is(runErr, fileops.ErrIdentityMismatch), errors.Is(runErr, fileops.ErrUnsafePath),
		errors.Is(runErr, fileops.ErrUnsupportedFileType):
		// Nothing was written: the target identity or path constraints failed
		// before the append. This is a definite failure, not an unknown side
		// effect.
		outcome.Status = StatusFailed
		outcome.Stderr = runErr.Error()
	default:
		// The write may have partially completed; do not claim success or a
		// clean failure.
		outcome.Status = StatusUnknown
		outcome.Stderr = runErr.Error()
	}

	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := s.record(recordCtx, proposal, outcome, started, finished, actor); err != nil {
		return outcome, err
	}
	return outcome, nil
}
