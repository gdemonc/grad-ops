package domain

import (
	"fmt"
	"time"
)

// timeLayout is the only timestamp encoding used at the persistence and API
// boundaries.
//
// The action-contract work in a later phase needs a canonical encoding shared
// across languages, so timestamps are always UTC and always use this layout
// rather than relying on differences in default formatting.
const timeLayout = time.RFC3339Nano

// FormatTime renders t as canonical UTC text.
func FormatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// ParseTime reads text produced by FormatTime.
func ParseTime(raw string) (time.Time, error) {
	t, err := time.Parse(timeLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid timestamp %q", ErrInvalidRequest, raw)
	}
	return t.UTC(), nil
}
