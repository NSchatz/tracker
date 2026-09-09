package push

import (
	"context"
	"log/slog"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/store"
)

// EventNotifier turns recorded geofence crossings into pushes. It is what the server calls after the
// evaluator appends events: read the family's registered push endpoints, build one notification per
// crossing, and hand every (endpoint × crossing) delivery to the dispatcher — which sends them
// without blocking the caller.
//
// It satisfies the server's Notifier interface structurally; the server never imports this package.
type EventNotifier struct {
	db     db.Querier
	disp   *Dispatcher
	logger *slog.Logger
	subs   subscriptionLookup
}

// subscriptionLookup is the fan-out read: every push endpoint a family's crossing should reach.
//
// It is a field rather than a direct call to store.PushSubscriptionsForFamily so the delivery
// accounting (ALERT-2 A4/A5/A19/A20/A21) can be proved end to end - notify → classify → enqueue →
// send → outcome - with no database in the loop. That accounting is pure process state, and a real
// PostGIS would prove nothing about it that a stub does not. The half of the same acceptance that IS
// about the database (a crossing recorded undelivered still comes back from the crossings route
// unchanged, A6) is proved against a real PostGIS, in internal/server.
type subscriptionLookup func(ctx context.Context, familyID string) ([]store.PushSubscription, error)

// NewEventNotifier wires a notifier over the database (for the subscription fan-out) and a dispatcher
// (for delivery).
func NewEventNotifier(database db.Querier, disp *Dispatcher, logger *slog.Logger) *EventNotifier {
	n := &EventNotifier{db: database, disp: disp, logger: logger}
	n.subs = func(ctx context.Context, familyID string) ([]store.PushSubscription, error) {
		return store.PushSubscriptionsForFamily(ctx, n.db, familyID)
	}
	return n
}

// NotifyGeofenceEvents fans the family's push endpoints out over the given crossings and enqueues the
// deliveries. It NEVER blocks on delivery and NEVER returns an error: the subscription lookup is a
// quick indexed read, and the actual sends happen on the dispatcher's worker — so a slow or dead push
// backend cannot slow, fail, or crash the ingestion path that called this (§5.3 fail-safe). A lookup
// failure is logged and swallowed for the same reason: a missed alert is acceptable; a failed fix
// ingest over a push hiccup is not.
//
// `dev` is the crossing device (its family scopes the fan-out; its name is the notification's title).
// Empty `events` is a no-op — no lookup, no delivery — which is the common case on most fixes.
func (n *EventNotifier) NotifyGeofenceEvents(ctx context.Context, dev store.Device, events []store.GeofenceEvent) {
	if len(events) == 0 {
		return
	}

	subs, err := n.subs(ctx, dev.FamilyID)
	if err != nil {
		n.logger.ErrorContext(ctx, "look up push subscriptions", "error", err, "family_id", dev.FamilyID)
		return
	}
	if len(subs) == 0 {
		return
	}

	ledger := n.disp.Ledger()
	var deliveries []Delivery
	for _, e := range events {
		note := buildNotification(dev, e)
		for _, s := range subs {
			// Classify BEFORE handing over: the bound is a statement about what was already pending
			// for this endpoint at this moment, and it has to be read before this crossing's own key
			// joins the set. Doing it here rather than in the dispatcher also keeps the accounting in
			// the order crossings actually happened - the worker drains asynchronously.
			bound := ledger.Classify(endpointKey{provider: s.Provider, token: s.Token}, note.CollapseKey)
			deliveries = append(deliveries, Delivery{
				Sub:   Subscription{Provider: s.Provider, Token: s.Token},
				Note:  note,
				Bound: bound,
			})
		}
	}
	n.disp.Enqueue(deliveries...)
}

// EndpointRegistered tells the delivery accounting that an endpoint has re-presented itself through
// registration, so its distinct-key count returns to zero (A20). The server calls it after a
// registration is accepted and stored.
//
// It is on the notifier rather than on the dispatcher's ledger directly because the server holds a
// Notifier and deliberately does not import this package.
func (n *EventNotifier) EndpointRegistered(_ context.Context, provider, token string) {
	n.disp.Ledger().EndpointRegistered(provider, token)
}

// EndpointRemoved tells the delivery accounting that an endpoint has been superseded and deleted
// (D8/A24), so its pending set can go with it. Nothing will be delivered to that routing address
// again.
func (n *EventNotifier) EndpointRemoved(_ context.Context, provider, token string) {
	n.disp.Ledger().EndpointRemoved(provider, token)
}

// buildNotification is the SINGLE place that decides what a crossing alert says — and, load-bearing,
// what it does NOT say. It carries only the family's own labels: the device's name (its title), the
// Place's name and the direction (its body and data). It never carries a coordinate, an accuracy, or
// any raw fix data (§5.3: "no PII in the body beyond what the family opted into" — the operator chose
// these names; a latitude is not something they opted to broadcast).
//
// The collapse key is per (device, Place), so a rapid enter→exit for one device at one Place collapses
// to the latest state rather than delivering a stale enter behind a fresh exit.
func buildNotification(dev store.Device, e store.GeofenceEvent) Notification {
	verb := "left"
	if e.Transition == "enter" {
		verb = "arrived at"
	}
	return Notification{
		Title:       dev.Name,
		Body:        dev.Name + " " + verb + " " + e.GeofenceName,
		CollapseKey: "gf:" + dev.ID + ":" + e.GeofenceID,
		Data: map[string]string{
			"type":       "geofence",
			"device_id":  dev.ID,
			"place_id":   e.GeofenceID,
			"place_name": e.GeofenceName,
			"transition": e.Transition,
			"ts":         e.FixTS.UTC().Format("2006-01-02T15:04:05Z"),
		},
	}
}
