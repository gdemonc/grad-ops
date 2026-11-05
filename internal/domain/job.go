package domain

// JobStatus is the lifecycle of a durable work item.
//
// A job is claimed under a lease. The lease is what makes a crash recoverable:
// an expired lease proves the previous owner stopped renewing, but on its own it
// proves nothing about whether the work completed.
type JobStatus string

const (
	// JobStatusPending is available to be claimed.
	JobStatusPending JobStatus = "PENDING"
	// JobStatusLeased is claimed by one worker until the lease expires.
	JobStatusLeased JobStatus = "LEASED"
	// JobStatusDone completed successfully.
	JobStatusDone JobStatus = "DONE"
	// JobStatusFailed ended in an error and will not be retried.
	JobStatusFailed JobStatus = "FAILED"
	// JobStatusUnknown records a job whose outcome could not be established,
	// for example a lease that expired while a side-effecting handler ran.
	// An UNKNOWN job is never replayed automatically.
	JobStatusUnknown JobStatus = "UNKNOWN"
)

// Valid reports whether s is a defined job status.
func (s JobStatus) Valid() bool {
	switch s {
	case JobStatusPending, JobStatusLeased, JobStatusDone, JobStatusFailed, JobStatusUnknown:
		return true
	default:
		return false
	}
}

// JobKind identifies what a job does.
type JobKind string

// JobKindInvestigate runs a bounded read-only investigation.
const JobKindInvestigate JobKind = "investigate"

// Retryable reports whether a job of this kind may be run again after its
// outcome is uncertain.
//
// Only read-only work qualifies. A job that changes host state must never be
// replayed on a guess: its real outcome has to be established from the recorded
// execution and the observed resource state first.
func (k JobKind) Retryable() bool {
	return k == JobKindInvestigate
}
