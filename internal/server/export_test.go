package server

import "time"

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
