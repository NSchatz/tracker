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
// ENDPOINT it belongs to (see below), the collapse key, the crossing's (device, place, transition,
// ts) and the outcome itself. That is a deployment's ordinary log stream: `docker compose logs
// tracker | grep push.delivery.outcome` answers "what became of that crossing" with no debugger,
// and - because the log sink outlives the process - an outcome recorded before a restart is still
// readable after one. A query ROUTE was rejected deliberately: /v1 takes exactly the three additive
// changes this phase documents in SPEC.md, and a fourth would be a wire surface nobody reviewed.
//
// # Per-endpoint, and what identifies an endpoint in the log
//
// A7 asks for the PER-ENDPOINT outcome, and an endpoint is (provider, routing address) - one phone.
// A family normally has more than one phone under one provider, so a record carrying only the
// provider answers "one of your phones lost this alert" and never "which one", which is the exact
// question this phase exists to answer.
//
// The routing address itself does not reach the log stream. It is the address a third party's
// delivery network routes on - "losing it leaks 'this endpoint can be pushed to'" is how migration
// 00004 puts it, which is why it is stored in the clear and not hashed - and a log line is the
// most-copied artefact a deployment has: it goes into a bug report, a screenshot and a shipped log
// aggregator without anyone deciding that it should. The repo already takes that stance for the
// credentials it holds (THREAT-MODEL.md: tokens are never stored, only their SHA-256 hashes), and
// nothing about the accounting needs the address. So the record carries EndpointDigest - a short,
// stable SHA-256 digest of "provider:routing address" - which is different for every endpoint,
// identical across every record for one endpoint, and reveals nothing about the address it stands
// for.
//
// An operator maps a digest back to a phone with one query against the registry they already own:
//
//	SELECT id, viewer_id, provider,
//	       encode(substring(sha256(convert_to(provider::text || ':' || token, 'UTF8')) from 1 for 8), 'hex')
//	         AS endpoint
//	  FROM push_subscriptions;
//
// which is the same construction as EndpointDigest, so the join is exact rather than approximate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// endpointDigestBytes is how much of the SHA-256 digest the log record carries. Eight bytes is
// sixteen hex characters: short enough to read and compare by eye in a log stream, and 64 bits of
// it, so two of a family's endpoints colliding is not a thing that happens. It is a PREFIX of the
// full digest rather than a different hash, so the SQL in the package note (which takes the same
// prefix) produces the same string.
const endpointDigestBytes = 8

// EndpointDigest names one endpoint - (provider, routing address) - in the operator-readable output
// without putting the routing address itself in the log stream.
//
// Stable: the same endpoint digests to the same string in every record and across restarts, which is
// what lets an operator group a phone's outcomes. Distinct: two endpoints digest differently, which
// is what makes the per-endpoint outcome A7 asks for actually attributable. Opaque: the input is a
// high-entropy routing address, so the digest identifies without disclosing.
//
// The separator is ":" and not a NUL byte on purpose. A provider is a closed enum ('fcm',
// 'unifiedpush') and contains no colon, so "provider:address" is still unambiguous - and Postgres
// text cannot hold a NUL, so a NUL separator would make the registry-side query in the package note
// impossible to write.
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
// every outcome. It is safe for concurrent use: the dispatcher's worker records outcomes while the
// HTTP registration handler resets endpoints.
type DeliveryLedger struct {
	logger *slog.Logger

	mu sync.Mutex
	// pending maps an endpoint to the distinct collapse keys handed over for it since the last
	// evidence that its app has run. A registration is such evidence; a backend accepting a message
	// is NOT.
	//
	// The value is a COUNT of in-flight hand-overs per key, not a set member, so NotHandedOver can
	// take one back without racing a concurrent crossing that re-added the same key: two crossings
	// for one key and one of them dropped leaves the key pending, exactly once; one crossing dropped
	// leaves the key not pending at all. The distinct-key count A19 reads is still len(keys) - the
	// counts are bookkeeping under it and are never themselves a bound.
	pending map[endpointKey]map[string]int
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
		pending: make(map[endpointKey]map[string]int),
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
// A delivery that Classify counted as pending and that the dispatcher then never manages to hand
// over is taken back by NotHandedOver - "pending" is defined as HANDED to the backend, and a queue
// that was full or a provider with no sender handed nothing over at all.
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
// no backend at all: the queue was full, the retries were exhausted, or no sender is configured for
// the provider. All three are the `dropped` outcome, and Definitions is explicit that pending means
// "a crossing the system has HANDED to the push backend" - so a crossing that was never handed over
// is not pending and must not count toward the bound for the next one.
//
// It is a decrement rather than a delete because a second crossing for the same (endpoint, key) may
// have been classified in between on another goroutine; deleting would take that one's pending
// record with it. At zero the key leaves the set, which is what shrinks the distinct-key count.
//
// Calling it for a key that was never counted is a no-op, which is what makes it safe on the
// beyond-collapse-bound path (Classify deliberately adds no key there) and for a Delivery that some
// other caller built without classifying it at all.
//
// Direction check (A21): this can only ever LOWER the pending count, so the outcome it can change is
// a later crossing's, from `beyond-collapse-bound` to `handed-over`. That is the "we know less"
// direction A21 permits, and `handed-over` is not a delivery claim. Nothing here can move an outcome
// toward one.
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

// Record writes one crossing's outcome at one endpoint to the operator-readable log.
//
// `reason` is free text for the `dropped` outcome (which of the three drop paths it was) and empty
// otherwise. The record carries the crossing's ids and labels-free fields only: no coordinate, no
// accuracy, no raw fix datum ever reaches a log line here, the same boundary buildNotification holds
// for the wire - and no routing address either, which is why `endpoint` is a digest (A7; see the
// package note).
func (l *DeliveryLedger) Record(ctx context.Context, d Delivery, outcome DeliveryOutcome, reason string) {
	attrs := []any{
		"outcome", string(outcome),
		"provider", d.Sub.Provider,
		// The other half of the endpoint key. Without it two phones in one family under one
		// provider produce records identical apart from the outcome, and the operator can read
		// that ONE phone lost the alert but never which - which is not a per-endpoint outcome.
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
	// INFO, not DEBUG: this is the record an operator is expected to be able to read on a default
	// log level. A delivery record nobody can see without turning the volume up is not "readable
	// without a debugger".
	l.logger.InfoContext(ctx, DeliveryOutcomeMessage, attrs...)
}

// DeliveryOutcomeMessage is the exact log message every outcome record carries, so an operator (and
// the tests) can select them with one grep and nothing else in the log collides with it.
const DeliveryOutcomeMessage = "push.delivery.outcome"
