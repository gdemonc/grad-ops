package domain

import "time"

// Task is a unit of work requested by an operator.
//
// It holds the durable facts of the request. Derived views (evidence,
// proposals, approvals) live in their own types and reference a task by ID.
type Task struct {
	ID           int64
	RequestText  string
	Source       string
	Status       Status
	Summary      *string
	FailureStage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
