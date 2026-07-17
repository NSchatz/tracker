// Package retention drops a family's location history once it is older than the configured window.
//
// # Why a background job, and why it drops partitions
//
// Location is maximally sensitive and includes minors (roadmap §3, §7). GDPR Art. 5(1)(e)'s
// storage-limitation principle — treated here as sound design guidance, not legal advice — says data
// is kept no longer than it is needed. tracker honours that by DROPPING whole monthly partitions of
// `fixes` once their entire range is past the retention window, on a timer.
//
// A DROP, never a DELETE, is the whole point (see internal/db.DropPartitionsBefore): expiring a month
// of a family's trail must be instant and ALL-OR-NOTHING. A row-precise DELETE can be interrupted
// half-way and leave a corrupted, partially-purged trail; dropping a partition either takes the whole
// month or it does not run at all. Because purging is coarse (monthly), up to a month of history
// survives past its exact retention date — the deliberate trade for a purge that can never
// half-finish.
//
// The window is configurable (TRACKER_RETENTION_DAYS) and 0 disables the job entirely: a deployment
// that wants to keep history forever runs no purge at all rather than one with a silently-chosen
// window. main.go only starts a Job when the window is positive.
package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/NSchatz/tracker/internal/db"
)

// Job periodically purges `fixes` partitions older than Window. It owns no goroutine until Run is
// called; RunOnce is the single purge pass Run repeats.
type Job struct {
	// db is exactly db.DropPartitionsBefore's dependency; *pgxpool.Pool satisfies it. The purge runs
	// against a real PostGIS (that is what internal/db.DropPartitionsBefore's tests exercise), so
	// there is no in-memory stand-in here.
	db       db.Querier
	window   time.Duration
	interval time.Duration
	logger   *slog.Logger

	// now is the clock, injectable so a test can pin "today" and assert exactly which month the
	// cutoff takes. Production leaves it nil and gets time.Now.
	now func() time.Time
}

// NewJob builds a purge job. window is how long history is kept (must be positive — a non-positive
// window is a disabled job, and main.go simply does not construct one). interval is how often the
// purge runs; a constant daily tick in production, a short one in tests.
func NewJob(database db.Querier, window, interval time.Duration, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	return &Job{db: database, window: window, interval: interval, logger: logger, now: time.Now}
}

// RunOnce performs a single purge: it drops every `fixes` partition whose entire range is older than
// (now - window) and returns the names dropped. An unreadable partition bound stops the purge with an
// error rather than reporting success over a partial job (see db.ErrUnrecognizedPartition) — silently
// purging too LITTLE is the failure that matters, because it leaves history alive past its promise.
func (j *Job) RunOnce(ctx context.Context) ([]string, error) {
	cutoff := j.clock().Add(-j.window)
	dropped, err := db.DropPartitionsBefore(ctx, j.db, cutoff)
	if err != nil {
		return dropped, fmt.Errorf("retention purge (cutoff %s): %w", cutoff.UTC().Format(time.RFC3339), err)
	}
	if len(dropped) > 0 {
		j.logger.Info("retention purge dropped expired history",
			"cutoff", cutoff.UTC().Format(time.RFC3339),
			"window_days", int(j.window.Hours()/24),
			"partitions", dropped)
	}
	return dropped, nil
}

// Run purges once immediately, then on every tick of interval, until ctx is cancelled.
//
// The immediate first pass matters: a server that has been down while history aged past the window
// should purge it on the next start, not wait a full interval. A purge error is LOGGED and the loop
// continues — a transient database blip must not kill the only thing enforcing retention — but the
// error is never swallowed silently, so an operator watching the logs sees a purge that keeps failing.
func (j *Job) Run(ctx context.Context) {
	j.logger.Info("retention purge started", "window_days", int(j.window.Hours()/24), "interval", j.interval)

	purge := func() {
		if _, err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
			j.logger.Error("retention purge failed; will retry next interval", "error", err)
		}
	}

	purge()

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			j.logger.Info("retention purge stopped")
			return
		case <-ticker.C:
			purge()
		}
	}
}

func (j *Job) clock() time.Time {
	if j.now != nil {
		return j.now()
	}
	return time.Now()
}
