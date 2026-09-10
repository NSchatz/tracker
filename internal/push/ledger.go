package push

// Honest delivery accounting (ALERT-2, A4/A5/A7/A19/A20/A21).
//
// THERE IS NO "delivered". FCM reports that it ACCEPTED a message, which is a statement about a queue
// in Google's datacentre, not about the person the alert is for. So the outcomes are three, and none
// of them asserts arrival:
//
//   - handed-over            - given to the backend, with no observed evidence of arrival.
//   - beyond-collapse-bound  - handed over while four or more distinct collapse keys were already
//     pending for that endpoint. FCM "can simultaneously store four different collapsible messages
//     per device, each with a different collapse key"; past that it discards and does not say which,
//     so this outcome is a SURPLUS and never asserts which message was lost.
//   - dropped                - never handed over: the queue was full, the retries were exhausted, or
//     no sender is configured for that provider.
//
// Under-claiming is the point: a fourth outcome asserting delivery would make the in-app crossing
// list, the fail-safe for a push the backend dropped, look redundant when it is not.
//
// The pending-key set is process state and is not persisted, so a restart FORGETS it and the next
// crossing for an endpoint that had four keys pending is recorded `handed-over`. That direction of
// loss is permitted because it moves toward "we know less", never toward a delivery claim; nothing
// here can upgrade an outcome, and eviction loses the same way.
//
// Every outcome is one structured log record, `push.delivery.outcome`, carrying the endpoint, the
// collapse key, the crossing's (device, place, transition, ts) and the outcome. A query ROUTE was
// rejected: /v1 takes exactly the three additive changes SPEC.md documents for this phase.
//
// An endpoint is (provider, routing address) - one phone - because a record carrying only the
// provider never says WHICH of a family's phones lost the alert. The routing address does not reach
// the log, since a log line is the most-copied artefact a deployment has; the record carries
// EndpointDigest, and a registry query built the same way maps one back to a phone exactly:
//
//	SELECT id, viewer_id, provider,
//	       encode(substring(sha256(convert_to(provider::text || ':' || token, 'UTF8')) from 1 for 8), 'hex')
//	         AS endpoint
//	  FROM push_subscriptions;

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
)

// DeliveryOutcome is what the server records for one crossing at one endpoint. Exactly three of them.
type DeliveryOutcome string

const (
	// OutcomeHandedOver - the backend took it. It says nothing about arrival.
	OutcomeHandedOver DeliveryOutcome = "handed-over"
	// OutcomeBeyondCollapseBound - handed over while the endpoint already had CollapseKeyBound or
	// more distinct keys pending, so the backend's own store cannot hold it alongside them.
	OutcomeBeyondCollapseBound DeliveryOutcome = "beyond-collapse-bound"
	// OutcomeDropped - never handed over: queue full, retries exhausted, or no sender.
	OutcomeDropped DeliveryOutcome = "dropped"
)

// CollapseKeyBound is FCM's documented limit, quoted: "The FCM server can simultaneously store four
// different collapsible messages per device, each with a different collapse key." A constant rather
// than a knob, because it is not tracker's number to choose.
const CollapseKeyBound = 4

// maxTrackedEndpoints bounds the ledger's memory: endpoints accumulate over a long uptime, and
// unbounded per-endpoint state in a long-lived server is a leak (A21).
const maxTrackedEndpoints = 4096

// endpointKey identifies one registered push endpoint: a provider plus the routing address for one
// phone. It is the unit A19's bound counts against, and what a rotated FCM registration token changes.
type endpointKey struct {
	provider string
	token    string
}

// endpointDigestBytes is how much of the SHA-256 digest the log record carries: sixteen hex
// characters, readable by eye, 64 bits against collision. A PREFIX of the full digest rather than a
// different hash, so the SQL in the package note produces the same string.
const endpointDigestBytes = 8

// EndpointDigest names one endpoint - (provider, routing address) - in the operator-readable output
// without putting the routing address itself in the log stream. Stable across restarts, distinct per
// endpoint, and opaque because the input is a high-entropy routing address.
//
// The separator is ":" rather than a NUL byte because Postgres text cannot hold a NUL, which would
// make the registry-side query impossible to write.
//
// An empty routing address is not an endpoint, and digesting one would manufacture an identifier for
// something that cannot be delivered to; it returns "" so the record shows the absence instead.
func EndpointDigest(provider, token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider + ":" + token))
	return hex.EncodeToString(sum[:endpointDigestBytes])
}

// DeliveryLedger is the per-endpoint collapse-key accounting plus the operator-readable record of
// every outcome. Safe for concurrent use: the dispatcher's worker records outcomes while the HTTP
// registration handler resets endpoints.
type DeliveryLedger struct {
	logger *slog.Logger

	mu sync.Mutex
	// pending maps an endpoint to the distinct collapse keys handed over for it since the last
	// evidence that its app has run. A registration is such evidence; a backend accepting a message
	// is NOT.
	//
	// The value is a COUNT of in-flight hand-overs per key, not a set member, so NotHandedOver can
	// take one back without racing a concurrent crossing that re-added the same key. The distinct-key
	// count A19 reads is still len(keys); the counts are bookkeeping under it, never a bound.
	pending map[endpointKey]map[string]int
	// order is insertion order over `pending`, so eviction drops the least recently ADDED endpoint
	// rather than a random map key, which map iteration order would make untestable.
	order []endpointKey
}

// NewDeliveryLedger builds a ledger that writes its outcomes to logger. A nil logger gets the
// default: accounting nobody can read is not accounting (A7).
func NewDeliveryLedger(logger *slog.Logger) *DeliveryLedger {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeliveryLedger{
		logger:  logger,
		pending: make(map[endpointKey]map[string]int),
	}
}

// Classify decides the collapse-bound outcome for one crossing about to be handed to the backend for
// one endpoint, and records its key as pending. OutcomeBeyondCollapseBound when the endpoint ALREADY
// has CollapseKeyBound or more distinct keys pending - the literal reading of A19, which draws no
// exception for a repeat of a pending key. The key is deliberately NOT added there: the backend
// discards something and does not say what, so growing the set would assert which message survived.
func (l *DeliveryLedger) Classify(e endpointKey, collapseKey string) DeliveryOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()

	keys, ok := l.pending[e]
	if !ok {
		keys = make(map[string]int)
		l.pending[e] = keys
		l.order = append(l.order, e)
		l.evictLocked()
	}
	if len(keys) >= CollapseKeyBound {
		return OutcomeBeyondCollapseBound
	}
	keys[collapseKey]++
	return OutcomeHandedOver
}

// NotHandedOver takes back the pending record Classify made, for a delivery that in the end reached
// no backend at all. Pending is defined as HANDED to the push backend, so a crossing that was never
// handed over must not count toward the bound for the next one.
//
// It is a decrement rather than a delete because a second crossing for the same (endpoint, key) may
// have been classified in between on another goroutine, and deleting would take that one's pending
// record with it. For a key that was never counted it is a no-op, which is what makes it safe on the
// beyond-collapse-bound path.
//
// Direction check (A21): this can only LOWER the pending count, so the outcome it can change is a
// later crossing's, from `beyond-collapse-bound` to `handed-over` - the "we know less" direction.
func (l *DeliveryLedger) NotHandedOver(e endpointKey, collapseKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	keys, ok := l.pending[e]
	if !ok {
		return
	}
	n, ok := keys[collapseKey]
	if !ok {
		return
	}
	if n <= 1 {
		delete(keys, collapseKey)
		return
	}
	keys[collapseKey] = n - 1
}

// EndpointRegistered is A20: an endpoint that re-presents itself through registration has proved its
// app is running, which is evidence that whatever was pending has been seen, so its distinct-key
// count returns to zero. A backend accepting a message is explicitly NOT such evidence, which is why
// nothing in the send path calls this.
func (l *DeliveryLedger) EndpointRegistered(provider, token string) {
	l.forget(endpointKey{provider: provider, token: token})
}

// EndpointRemoved forgets an endpoint the server has just deleted (D8/A24): a slow leak otherwise.
func (l *DeliveryLedger) EndpointRemoved(provider, token string) {
	l.forget(endpointKey{provider: provider, token: token})
}

func (l *DeliveryLedger) forget(e endpointKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.pending[e]; !ok {
		return
	}
	delete(l.pending, e)
	for i, k := range l.order {
		if k == e {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
}

// PendingKeys reports how many distinct collapse keys are pending. No route exposes it.
func (l *DeliveryLedger) PendingKeys(provider, token string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending[endpointKey{provider: provider, token: token}])
}

// Reset drops the whole pending accounting, as a process restart does. It makes the restart rule
// (A21) testable without restarting a process, and is never called in production.
func (l *DeliveryLedger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = make(map[endpointKey]map[string]int)
	l.order = nil
}

// evictLocked keeps the tracked-endpoint count under maxTrackedEndpoints. Caller holds the lock.
func (l *DeliveryLedger) evictLocked() {
	for len(l.order) > maxTrackedEndpoints {
		oldest := l.order[0]
		l.order = l.order[1:]
		delete(l.pending, oldest)
	}
}

// Record writes one crossing's outcome at one endpoint to the operator-readable log. `reason` is free
// text for the `dropped` outcome and empty otherwise. No coordinate, accuracy or raw fix datum
// reaches a log line here, and no routing address, which is why `endpoint` is a digest (A7).
func (l *DeliveryLedger) Record(ctx context.Context, d Delivery, outcome DeliveryOutcome, reason string) {
	attrs := []any{
		"outcome", string(outcome),
		"provider", d.Sub.Provider,
		// The other half of the endpoint key. Without it two phones in one family under one provider
		// produce records identical apart from the outcome, which is not a per-endpoint outcome.
		"endpoint", EndpointDigest(d.Sub.Provider, d.Sub.Token),
		"collapse_key", d.Note.CollapseKey,
		"device_id", d.Note.Data["device_id"],
		"place_id", d.Note.Data["place_id"],
		"transition", d.Note.Data["transition"],
		"ts", d.Note.Data["ts"],
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	// INFO, not DEBUG: a delivery record nobody can see without turning the volume up is not
	// readable without a debugger.
	l.logger.InfoContext(ctx, DeliveryOutcomeMessage, attrs...)
}

// DeliveryOutcomeMessage is the exact log message every outcome record carries, so one grep gets them.
const DeliveryOutcomeMessage = "push.delivery.outcome"
