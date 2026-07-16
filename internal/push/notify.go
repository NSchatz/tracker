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
}

// NewEventNotifier wires a notifier over the database (for the subscription fan-out) and a dispatcher
// (for delivery).
func NewEventNotifier(database db.Querier, disp *Dispatcher, logger *slog.Logger) *EventNotifier {
	return &EventNotifier{db: database, disp: disp, logger: logger}
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

	subs, err := store.PushSubscriptionsForFamily(ctx, n.db, dev.FamilyID)
	if err != nil {
		n.logger.ErrorContext(ctx, "look up push subscriptions", "error", err, "family_id", dev.FamilyID)
		return
	}
	if len(subs) == 0 {
		return
	}

	var deliveries []Delivery
	for _, e := range events {
		note := buildNotification(dev, e)
		for _, s := range subs {
			deliveries = append(deliveries, Delivery{
				Sub:  Subscription{Provider: s.Provider, Token: s.Token},
				Note: note,
			})
		}
	}
	n.disp.Enqueue(deliveries...)
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
