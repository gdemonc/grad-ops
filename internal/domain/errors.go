package domain

import "errors"

// Sentinel errors that service and transport layers map to responses.
//
// They are deliberately coarse: a request that cannot be validated and a
// resource that does not exist must be distinguishable without leaking
// internal detail, and every other failure is an internal error.
var (
	// ErrNotFound reports that the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidRequest reports that the input failed validation.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrInvalidTransition reports an attempt to move a task between states
	// that the state machine does not allow.
	ErrInvalidTransition = errors.New("invalid transition")
)
