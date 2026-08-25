package server

import (
	"time"

	"github.com/NSchatz/tracker/internal/presentation"
)

// SetStreamPollInterval lets the SSE tests shrink the stream's database-poll cadence so a
// reconnect/resume assertion does not wait a production second per poll. It returns a restore func.
//
// It is only ever called ONCE, before any streaming goroutine starts, and restored at test cleanup —
// so there is no concurrent write to race the poll goroutine's read under `go test -race`. Only the
// stream tests touch the interval; the ingest and read suites never read it.
func SetStreamPollInterval(d time.Duration) func() {
	old := streamPollInterval
	streamPollInterval = d
	return func() { streamPollInterval = old }
}

// StreamPollTick exposes the per-connection poll cadence so a test can assert the property the
// contract actually needs — that the cadence leaves room INSIDE the sweep bound B for the query that
// follows it — rather than eyeballing two constants that happen to be equal.
func StreamPollTick(w presentation.Windows) time.Duration { return streamPollTick(w) }
