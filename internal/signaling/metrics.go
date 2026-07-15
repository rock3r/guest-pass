package signaling

import "context"

// Metrics records anonymous lifecycle aggregates; room callers never supply an
// entity identifier or run it on the signaling hot path.
type Metrics interface {
	AddCounter(context.Context, string, int64) error
	BumpMax(context.Context, string, int64) error
}

const (
	counterGuestConnectedSeconds = "guest_connected_seconds"
	counterPeakConcurrent        = "peak_concurrent"
)
