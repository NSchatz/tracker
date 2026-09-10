// Package push delivers geofence-crossing alerts to a family's phones (roadmap S6, risk path #5).
//
// The shape is three layers that the acceptance pins independently:
//
//   - a SENDER per backend (fcm.go, unifiedpush.go) - the wire format of one push to one endpoint.
//     FCM HTTP v1 with high priority and a collapse key; UnifiedPush a plain POST to an endpoint URL.
//   - a DISPATCHER - a bounded, retrying, non-blocking queue in front of the senders, so a slow or
//     failing push backend can NEVER block or fail ingestion (§5.3 fail-safe). Best-effort delivery is
//     the contract FCM itself gives us, and the dispatcher does not pretend to more.
//   - an EventNotifier (notify.go) - the glue the server calls after a crossing is recorded.
//
// THE PII BOUNDARY (§5.3, "no PII in the body beyond what the family opted into"): a notification
// carries only the family's OWN labels - the device's name, the Place's name - and the direction of
// the crossing. It never carries a coordinate, an accuracy or any raw fix data. buildNotification
// (notify.go) is the single place that decides this, so the boundary is auditable in one spot.
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

// Delivery is one Notification bound for one Subscription: the unit the dispatcher queues and a
// Sender sends.
//
// Bound carries the collapse-bound classification the ledger made (A19). It is decided at ENQUEUE,
// where the pending set is known, and only ever DOWNGRADED afterwards - a delivery the dispatcher
// never hands over is recorded `dropped` whatever it was classified as. Nothing upgrades it, and a
// backend accepting the message in particular does not (A5). The zero value is the under-claiming
// default, OutcomeHandedOver.
type Delivery struct {
	Sub   Subscription
	Note  Notification
	Bound DeliveryOutcome
}

// boundOrDefault reads the classification off a Delivery, defaulting an unset one to handed-over.
func (d Delivery) boundOrDefault() DeliveryOutcome {
	if d.Bound == "" {
		return OutcomeHandedOver
	}
	return d.Bound
}

// Sender delivers one Delivery to one endpoint, returning an error if the backend refused it. An
// error is the dispatcher's signal to retry-then-drop; it is never propagated back to ingestion.
type Sender interface {
	Send(ctx context.Context, d Delivery) error
}

// DefaultMaxPending is the depth of the dispatcher's queue, the "100-pending cap" (roadmap S6). Past
// it, new deliveries are dropped with a log rather than growing the queue without limit. It mirrors
// FCM's own behaviour, so the cap is the same "best-effort, bounded" contract the backend gives us.
const DefaultMaxPending = 100

// defaultMaxAttempts is how many times the dispatcher tries one delivery. Small on purpose: delivery
// is best-effort and the freshest state arrives on the next fix, so retrying forever would only
// deepen the queue behind a dead backend.
const defaultMaxAttempts = 3

// defaultRetryBackoff is the pause between attempts. Short, because the worker is single-threaded and
// a long sleep stalls every delivery behind it: the retry rides out a blip, not an outage.
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
	// ledger records what became of every delivery. Never nil - NewDispatcher builds one - so
	// accounting cannot be silently switched off by forgetting an option.
	ledger *DeliveryLedger

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
		ledger:      NewDeliveryLedger(logger),
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
			// Never handed over at all. NotifyGeofenceEvents classifies every (crossing, endpoint)
			// pair before it enqueues any of them, so the pending record has to be taken back here or
			// a key nothing was handed over for keeps counting toward the next crossing's bound.
			d.notHandedOver(del)
			d.ledger.Record(context.Background(), del, OutcomeDropped, "queue full (pending cap reached)")
		}
	}
}

// Ledger exposes the delivery accounting so the registration path can tell it an endpoint has
// re-presented itself (A20) and so the accounting tests can read the pending counts. It never
// returns nil.
func (d *Dispatcher) Ledger() *DeliveryLedger { return d.ledger }

// notHandedOver takes back the pending record a delivery's classification made, on every path that
// ends in `dropped`. Pending means handed to the backend, and no drop path hands anything over, so a
// key left counting there would report a bound that nothing earned. Only a delivery classified
// OutcomeHandedOver ever added a key, which is what the guard says.
func (d *Dispatcher) notHandedOver(del Delivery) {
	if del.boundOrDefault() != OutcomeHandedOver {
		return
	}
	d.ledger.NotHandedOver(endpointKey{provider: del.Sub.Provider, token: del.Sub.Token}, del.Note.CollapseKey)
}

// Dropped reports how many deliveries have been refused because the queue was full. It exists for
// observability and for the cap test to assert the drop happened rather than the delivery blocking.
func (d *Dispatcher) Dropped() int64 { return d.dropped.Load() }

// Close stops accepting deliveries and blocks until the worker has drained everything already queued,
// so a clean shutdown does not silently discard pushes that were already accepted.
//
// The drain is bounded by the SENDERS, not by Close: each remaining delivery is retried with the
// sender's own finite HTTP timeout, so with a dead backend and a full queue Close can take up to
// roughly pending x maxAttempts x (timeout+backoff). Acceptable at shutdown, and it runs after the
// HTTP server has already drained, so it never delays serving.
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

// deliver sends one delivery, retrying up to maxAttempts, and - the fail-safe - swallowing a final
// failure as a log rather than a panic or a propagated error. A push that cannot be delivered is a
// missed alert, never a crash and never anything ingestion sees.
func (d *Dispatcher) deliver(ctx context.Context, del Delivery) {
	sender, ok := d.senders[del.Sub.Provider]
	if !ok {
		// A subscription for a backend this deployment did not configure. Not fatal — log and skip, so
		// one stray unifiedpush endpoint on an fcm-only server does not take down the worker.
		d.logger.Warn("no push sender for provider — dropping delivery",
			"provider", del.Sub.Provider, "collapse_key", del.Note.CollapseKey)
		d.notHandedOver(del)
		d.ledger.Record(ctx, del, OutcomeDropped, "no sender configured for provider")
		return
	}

	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		err := sender.Send(ctx, del)
		if err == nil {
			// The backend TOOK it. That is hand-over and nothing more: the outcome recorded is the
			// classification made at enqueue, never an upgrade to a delivery claim (A5).
			d.ledger.Record(ctx, del, del.boundOrDefault(), "")
			return
		}
		if attempt == d.maxAttempts {
			// Logged, not crashed (§6: "delivery-failure handling logged, not crashed"). The freshest
			// state will be pushed again on the device's next crossing.
			d.logger.Error("push delivery failed after retries — dropping",
				"provider", del.Sub.Provider, "collapse_key", del.Note.CollapseKey,
				"attempts", attempt, "error", err)
			d.notHandedOver(del)
			d.ledger.Record(ctx, del, OutcomeDropped, "retries exhausted")
			return
		}
		d.logger.Warn("push delivery failed — retrying",
			"provider", del.Sub.Provider, "attempt", attempt, "error", err)
		if d.backoff > 0 {
			time.Sleep(d.backoff)
		}
	}
}
