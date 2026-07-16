// Package push delivers geofence-crossing alerts to a family's phones (roadmap S6, risk path #5).
//
// The shape is three layers that the acceptance pins independently:
//
//   - a SENDER per backend (fcm.go, unifiedpush.go) — the wire format of one push to one endpoint.
//     FCM HTTP v1 with high priority and a collapse key; UnifiedPush a plain POST to an endpoint URL.
//     These are the unit-tested surface (request shape, priority, collapse key, failure handling).
//   - a DISPATCHER — a bounded, retrying, non-blocking queue in front of the senders, so a slow or
//     failing push backend can NEVER block or fail ingestion (§5.3 fail-safe). A send that fails is
//     retried within limits and then dropped-with-a-log; a queue that fills drops the oldest-refused
//     with a log (the 100-pending cap). Best-effort delivery is the contract FCM itself gives us, and
//     the dispatcher does not pretend to more.
//   - an EventNotifier (notify.go) — the glue the server calls after a crossing is recorded: fan the
//     family's registered endpoints out into deliveries and hand them to the dispatcher.
//
// # The PII boundary (§5.3: "no PII in the body beyond what the family opted into")
//
// A notification carries only the family's OWN labels — the device's name and the Place's name, both
// chosen by the operator when enrolling them — and the direction of the crossing. It never carries a
// coordinate, an accuracy, or any raw fix data. "Alice's phone left School" is the family's own
// vocabulary; a latitude is not, and none is ever put on the wire. buildNotification (notify.go) is
// the single place that decides this, so the boundary is enforced in one auditable spot.
package push

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Subscription is where one push goes: which backend, and the routing address for it (an FCM
// registration token, or a UnifiedPush endpoint URL). It mirrors store.PushSubscription — the store
// type is the row, this is the sender's view of it.
type Subscription struct {
	Provider string
	Token    string
}

// Notification is the backend-neutral content of one alert. The senders translate it into their own
// wire format; nothing here is provider-specific.
//
// CollapseKey groups messages that supersede one another: FCM (and ntfy) deliver only the LATEST
// undelivered message per collapse key, so an enter quickly followed by an exit collapses to "the
// latest state" rather than buzzing a phone twice for a crossing it can no longer act on. The key is
// per (device, Place) — see buildNotification.
//
// Data is a small structured map the receiving app can act on without parsing the human strings. It
// holds only the family's own labels and ids (see the package PII note), never a coordinate.
type Notification struct {
	Title       string
	Body        string
	CollapseKey string
	Data        map[string]string
}

// Delivery is one Notification bound for one Subscription — the unit the dispatcher queues and a
// Sender sends.
type Delivery struct {
	Sub  Subscription
	Note Notification
}

// Sender delivers one Delivery to one endpoint, returning an error if the backend refused it. An
// error is the dispatcher's signal to retry-then-drop; it is never propagated back to ingestion.
type Sender interface {
	Send(ctx context.Context, d Delivery) error
}

// DefaultMaxPending is the depth of the dispatcher's queue — the "100-pending cap" (roadmap S6). It
// bounds how many undelivered pushes can back up in memory when a backend is slow or down: past it,
// new deliveries are dropped with a log rather than growing the queue without limit. It mirrors FCM's
// own behaviour (it caps pending messages per device and discards the rest), so the cap is not an
// arbitrary number — it is the same "best-effort, bounded" contract the backend gives us.
const DefaultMaxPending = 100

// defaultMaxAttempts is how many times the dispatcher tries one delivery before giving up and logging
// it. Small on purpose: push delivery is best-effort and the freshest state arrives on the next fix
// anyway, so retrying forever would only deepen the queue behind a dead backend.
const defaultMaxAttempts = 3

// defaultRetryBackoff is the pause between delivery attempts. Short, because the worker is
// single-threaded and a long sleep would stall every delivery behind it; the point of the retry is to
// ride out a momentary blip, not to wait out an outage.
const defaultRetryBackoff = 200 * time.Millisecond

// Dispatcher is the bounded, retrying queue in front of the senders. It owns one worker goroutine
// draining a buffered channel, so Enqueue is non-blocking and a push NEVER blocks the ingestion path
// that called it. It routes each delivery to the Sender registered for its provider.
type Dispatcher struct {
	senders     map[string]Sender
	logger      *slog.Logger
	queue       chan Delivery
	maxAttempts int
	backoff     time.Duration

	wg      sync.WaitGroup
	dropped atomic.Int64 // deliveries refused because the queue was full — observability + tests
}

// DispatcherOption tunes a Dispatcher at construction. The defaults are production values; the
// options exist so tests can pin the retry/cap behaviour deterministically without racing a clock.
type DispatcherOption func(*Dispatcher)

// WithMaxPending sets the queue depth (the pending cap). n < 1 is treated as 1 — a zero-length buffer
// would make every Enqueue that races the worker a drop, which is a footgun, not a configuration.
func WithMaxPending(n int) DispatcherOption {
	return func(d *Dispatcher) {
		if n < 1 {
			n = 1
		}
		d.queue = make(chan Delivery, n)
	}
}

// WithMaxAttempts sets how many times a delivery is tried before it is dropped-with-a-log.
func WithMaxAttempts(n int) DispatcherOption {
	return func(d *Dispatcher) {
		if n < 1 {
			n = 1
		}
		d.maxAttempts = n
	}
}

// WithRetryBackoff sets the pause between attempts (tests set it to 0 so a failure path does not
// sleep).
func WithRetryBackoff(d time.Duration) DispatcherOption {
	return func(disp *Dispatcher) { disp.backoff = d }
}

// NewDispatcher builds a Dispatcher over the given per-provider senders and starts its worker. The
// caller owns its lifetime and MUST Close it to drain in-flight deliveries on shutdown.
func NewDispatcher(senders map[string]Sender, logger *slog.Logger, opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{
		senders:     senders,
		logger:      logger,
		queue:       make(chan Delivery, DefaultMaxPending),
		maxAttempts: defaultMaxAttempts,
		backoff:     defaultRetryBackoff,
	}
	for _, opt := range opts {
		opt(d)
	}
	d.wg.Add(1)
	go d.run()
	return d
}

// Enqueue offers deliveries to the queue WITHOUT BLOCKING. If the queue is full (the pending cap is
// reached), the delivery is dropped and logged rather than blocking the caller — this is the property
// that keeps a stalled push backend from ever slowing ingestion. It is safe to call from any
// goroutine.
func (d *Dispatcher) Enqueue(deliveries ...Delivery) {
	for _, del := range deliveries {
		select {
		case d.queue <- del:
		default:
			n := d.dropped.Add(1)
			d.logger.Warn("push queue full — dropping delivery (pending cap reached)",
				"provider", del.Sub.Provider, "collapse_key", del.Note.CollapseKey, "dropped_total", n)
		}
	}
}

// Dropped reports how many deliveries have been refused because the queue was full. It exists for
// observability and for the cap test to assert the drop happened rather than the delivery blocking.
func (d *Dispatcher) Dropped() int64 { return d.dropped.Load() }

// Close stops accepting deliveries and blocks until the worker has drained everything already queued.
// Closing the channel is what ends the worker loop; the buffered deliveries are still processed, so a
// clean shutdown does not silently discard pushes that were already accepted.
//
// The drain is best-effort and bounded by the SENDERS, not by Close: each remaining delivery is sent
// (and retried) with the sender's own finite HTTP timeout, so with a dead backend and a full queue
// Close can take up to roughly pending × maxAttempts × (timeout+backoff). That is acceptable at
// shutdown — the deliveries were already accepted, the sender timeouts are finite, and an orchestrator
// that wants a hard cap SIGKILLs after its grace period — and it is called after the HTTP server has
// already drained, so it never delays serving.
func (d *Dispatcher) Close() {
	close(d.queue)
	d.wg.Wait()
}

// run is the single worker: pull a delivery, deliver it with bounded retry, repeat until the queue is
// closed and drained.
func (d *Dispatcher) run() {
	defer d.wg.Done()
	for del := range d.queue {
		d.deliver(context.Background(), del)
	}
}

// deliver sends one delivery, retrying up to maxAttempts, and — the fail-safe — swallowing a final
// failure as a log rather than a panic or a propagated error. A push that cannot be delivered is a
// missed alert, which S6 accepts as best-effort; it is never a crash and never anything ingestion
// sees.
func (d *Dispatcher) deliver(ctx context.Context, del Delivery) {
	sender, ok := d.senders[del.Sub.Provider]
	if !ok {
		// A subscription for a backend this deployment did not configure. Not fatal — log and skip, so
		// one stray unifiedpush endpoint on an fcm-only server does not take down the worker.
		d.logger.Warn("no push sender for provider — dropping delivery",
			"provider", del.Sub.Provider, "collapse_key", del.Note.CollapseKey)
		return
	}

	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		err := sender.Send(ctx, del)
		if err == nil {
			return
		}
		if attempt == d.maxAttempts {
			// Logged, not crashed (§6: "delivery-failure handling logged, not crashed"). The freshest
			// state will be pushed again on the device's next crossing.
			d.logger.Error("push delivery failed after retries — dropping",
				"provider", del.Sub.Provider, "collapse_key", del.Note.CollapseKey,
				"attempts", attempt, "error", err)
			return
		}
		d.logger.Warn("push delivery failed — retrying",
			"provider", del.Sub.Provider, "attempt", attempt, "error", err)
		if d.backoff > 0 {
			time.Sleep(d.backoff)
		}
	}
}
