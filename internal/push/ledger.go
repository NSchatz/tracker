package push

// Honest delivery accounting (ALERT-2, A4/A5/A7/A19/A20/A21).
//
// # There is no "delivered"
//
// FCM does not tell tracker that a phone received a message. It tells tracker that FCM ACCEPTED one,
// which is a statement about a queue in Google's datacentre, not about the person the alert is for.
// So the outcomes recorded here are deliberately three, and none of them is "delivered":
//
//   - handed-over            - given to the backend, with no observed evidence of arrival.
//   - beyond-collapse-bound  - handed over while four or more distinct collapse keys were already
//     pending for that endpoint. FCM "can simultaneously store four different collapsible messages
//     per device, each with a different collapse key"; past that it discards, and it does not say
//     which. So this outcome is recorded as a SURPLUS, and it never asserts which message was lost.
//   - dropped                - never handed over at all: the pending queue was full, the retries were
//     exhausted, or no sender is configured for that provider.
//
// Under-claiming is safe and is the point. A fourth outcome that asserted delivery would be the
// confident wrong answer this repo refuses everywhere else, and it would make the in-app crossing
// list - the fail-safe for a push the backend dropped - look redundant when it is not.
//
// # Why the pending accounting is in memory, and what a restart may do to it
//
// The pending-key set is process state and is not persisted: no schema, no migration. A restart
// therefore FORGETS it, and the next crossing for an endpoint that had four keys pending is recorded
// `handed-over` where an unrestarted server would have said `beyond-collapse-bound`. That direction
// of loss is permitted, because it moves an outcome toward "we know less", never toward a delivery
// claim. The opposite direction is forbidden and cannot happen here: nothing in this file can
// upgrade an outcome, and a successful Send writes no outcome at all beyond the one already decided.
//
// The same rule governs eviction: the ledger is bounded (endpoints are not, over a long uptime), and
// an evicted endpoint reads as "no keys pending" - the same safe direction as a restart.
//
// # What "operator-readable" means here (A7)
//
// Every outcome is written as one structured log record, `push.delivery.outcome`, carrying the
// endpoint's provider, the collapse key, the crossing's (device, place, transition, ts) and the
// outcome itself. That is a deployment's ordinary log stream: `docker compose logs tracker |
// grep push.delivery.outcome` answers "what became of that crossing" with no debugger, and - because
// the log sink outlives the process - an outcome recorded before a restart is still readable after
// one. A query ROUTE was rejected deliberately: /v1 takes exactly the three additive changes this
// phase documents in SPEC.md, and a fourth would be a wire surface nobody reviewed.

import (
	"context"
	"log/slog"
	"sync"
)

// DeliveryOutcome is what the server records for one crossing at one endpoint. There are exactly
// three; see the package note above for why none of them is "delivered".
type DeliveryOutcome string

const (
	// OutcomeHandedOver - the crossing was given to the backend and the backend took it. It says
	// nothing about arrival.
	OutcomeHandedOver DeliveryOutcome = "handed-over"
	// OutcomeBeyondCollapseBound - handed over while the endpoint already had CollapseKeyBound or
	// more distinct keys pending, so the backend's own store cannot hold it alongside them.
	OutcomeBeyondCollapseBound DeliveryOutcome = "beyond-collapse-bound"
	// OutcomeDropped - never handed over: queue full, retries exhausted, or no sender for the
	// provider.
	OutcomeDropped DeliveryOutcome = "dropped"
)

// CollapseKeyBound is FCM's documented limit: "The FCM server can simultaneously store four
// different collapsible messages per device, each with a different collapse key." tracker's collapse
// key is per (device, Place), so four keys is four device-and-Place pairs pending for one phone.
//
// It is a constant rather than a knob because it is not tracker's number to choose - it is the
// backend's, quoted.
const CollapseKeyBound = 4

// maxTrackedEndpoints bounds the ledger's memory. Endpoints accumulate over a long uptime (every
// phone that ever registered), and unbounded per-endpoint state in a long-lived server is a leak.
// Eviction loses an endpoint's pending set, which reads as "no keys pending" - the same safe
// direction a restart loses in (A21).
const maxTrackedEndpoints = 4096

// endpointKey identifies one registered push endpoint: a provider plus the routing address for one
// phone. It is the unit A19's bound counts against, and it is what a rotated FCM registration token
// changes (which is why registration can name the address it replaces - see D8).
type endpointKey struct {
	provider string
	token    string
}

// DeliveryLedger is the per-endpoint collapse-key accounting plus the operator-readable record of
// every outcome. It is safe for concurrent use: the dispatcher's worker records outcomes while the
// HTTP registration handler resets endpoints.
type DeliveryLedger struct {
	logger *slog.Logger

	mu sync.Mutex
	// pending maps an endpoint to the distinct collapse keys handed over for it since the last
	// evidence that its app has run. A registration is such evidence; a backend accepting a message
	// is NOT.
	pending map[endpointKey]map[string]struct{}
	// order is insertion order over `pending`, so eviction drops the least recently ADDED endpoint
	// rather than a random map key (map iteration order would make eviction untestable).
	order []endpointKey
}

// NewDeliveryLedger builds a ledger that writes its outcomes to logger. A nil logger gets the
// default - accounting that silently discarded its own output would be accounting nobody can read,
// which is the whole of A7.
func NewDeliveryLedger(logger *slog.Logger) *DeliveryLedger {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeliveryLedger{
		logger:  logger,
		pending: make(map[endpointKey]map[string]struct{}),
	}
}

// Classify decides the collapse-bound outcome for one crossing about to be handed to the backend for
// one endpoint, and records its key as pending.
//
// It returns OutcomeBeyondCollapseBound when the endpoint ALREADY has CollapseKeyBound or more
// distinct keys pending - the literal reading of A19, which draws no exception for a repeat of a key
// that is already pending. In that case the key is deliberately NOT added: the backend discards
// something at this point and does not say what, so growing the set here would be asserting which
// message survived. Leaving it fixed keeps the ledger's claim to exactly "there were already four".
//
// One imprecision, recorded rather than hidden: a delivery classified here and then `dropped` by the
// dispatcher (queue full, retries exhausted, no sender) leaves its key in the pending set, even
// though nothing was handed over. The key is NOT removed, deliberately. Removing it would need to
// distinguish "this key is pending only because of the delivery that just failed" from "a later
// crossing re-added it", which is a race with the worker; and both errors are inside the envelope
// A21 already permits, because neither can assert delivery. Leaving it errs toward reporting the
// bound, which is the more conservative claim of the two.
func (l *DeliveryLedger) Classify(e endpointKey, collapseKey string) DeliveryOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()

	keys, ok := l.pending[e]
	if !ok {
		keys = make(map[string]struct{})
		l.pending[e] = keys
		l.order = append(l.order, e)
		l.evictLocked()
	}
	if len(keys) >= CollapseKeyBound {
		return OutcomeBeyondCollapseBound
	}
	keys[collapseKey] = struct{}{}
	return OutcomeHandedOver
}

// EndpointRegistered is A20: an endpoint that re-presents itself through registration has proved its
// app is running, which is evidence that whatever was pending has been seen (or superseded). Its
// distinct-key count therefore returns to zero.
//
// A backend accepting a message is explicitly NOT such evidence, which is why nothing in the send
// path calls this.
func (l *DeliveryLedger) EndpointRegistered(provider, token string) {
	l.forget(endpointKey{provider: provider, token: token})
}

// EndpointRemoved forgets an endpoint the server has just deleted (a rotated routing address that a
// registration superseded - D8/A24). Nothing will ever be delivered to it again, so keeping its
// pending set would only be a slow leak.
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

// PendingKeys reports how many distinct collapse keys are pending for an endpoint. It exists for the
// accounting tests and for nothing else - there is no route that exposes it.
func (l *DeliveryLedger) PendingKeys(provider, token string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending[endpointKey{provider: provider, token: token}])
}

// Reset drops the whole pending accounting, which is exactly what a process restart does to it. It
// exists so the restart rule (A21) is testable without restarting a process, and it is never called
// in production - a real restart gets a fresh ledger for free.
func (l *DeliveryLedger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = make(map[endpointKey]map[string]struct{})
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

// Record writes one crossing's outcome at one endpoint to the operator-readable log.
//
// `reason` is free text for the `dropped` outcome (which of the three drop paths it was) and empty
// otherwise. The record carries the crossing's ids and labels-free fields only: no coordinate, no
// accuracy, no raw fix datum ever reaches a log line here, the same boundary buildNotification holds
// for the wire.
func (l *DeliveryLedger) Record(ctx context.Context, d Delivery, outcome DeliveryOutcome, reason string) {
	attrs := []any{
		"outcome", string(outcome),
		"provider", d.Sub.Provider,
		"collapse_key", d.Note.CollapseKey,
		"device_id", d.Note.Data["device_id"],
		"place_id", d.Note.Data["place_id"],
		"transition", d.Note.Data["transition"],
		"ts", d.Note.Data["ts"],
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	// INFO, not DEBUG: this is the record an operator is expected to be able to read on a default
	// log level. A delivery record nobody can see without turning the volume up is not "readable
	// without a debugger".
	l.logger.InfoContext(ctx, DeliveryOutcomeMessage, attrs...)
}

// DeliveryOutcomeMessage is the exact log message every outcome record carries, so an operator (and
// the tests) can select them with one grep and nothing else in the log collides with it.
const DeliveryOutcomeMessage = "push.delivery.outcome"
