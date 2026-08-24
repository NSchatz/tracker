package store

// S5 — server-side geofencing. This file is two things that meet at the `geofences` table:
//
//   1. Places CRUD (list / get / update / delete; CreateGeofence lives in entities.go with the WKT
//      helpers it shares). Family-scoped, like every other read/write in this package.
//   2. The STREAM EVALUATOR — EvaluateDeviceGeofences — which computes enter/exit transitions per
//      (device, place) from the incoming fix stream and appends them to geofence_events.
//
// The evaluator is the hard part, and its contract is worth stating plainly because the roadmap's
// §5.3 fail-safe pins every clause of it:
//
//   * "against prior state, debounced against GPS jitter" — a transition is DWELL-confirmed: a flip
//     of containment is only recorded once it has PERSISTED for GeofenceDebounce. A single fix that
//     jitters across the boundary and back never becomes an event.
//   * "derive from ts order, not arrival" — the log is a deterministic projection of the stored
//     fixes: the evaluator re-derives transitions from the device's fixes in `ts` order, seeded by
//     the last recorded event, so a replayed or out-of-order fix converges the log to the same
//     result rather than duplicating or contradicting it.
//   * "on-boundary resolves by ST_Covers (inclusive)" — containment is ST_Covers, exactly as
//     GeofencesContaining uses (store.go). A fix on the edge counts as inside.
//   * "a missed fix delays but never fabricates" — dwell confirmation LOOKS FORWARD: an enter is not
//     recorded until later fixes prove the device stayed inside. If the boundary-crossing fix is
//     missing, the enter is attributed to a later fix (delayed) — it is never invented.
//
// The one boundary of that contract, stated so it is not discovered in production: the evaluator
// advances a (device, place)'s state FORWARD in ts. A fix that arrives out of order and is OLDER
// than the latest already-recorded transition for that pair is kept as history but does not splice a
// new event into the past — appending an enter that predates a recorded exit would corrupt the log's
// order, so it is refused. In practice a boundary crossing is a moving, frequently-reporting phone,
// and reordered fixes ahead of the last transition are handled exactly by ts.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/jackc/pgx/v5"
)

// GeofenceDebounce is how long a change of containment must PERSIST before it is recorded as a
// transition. It is the jitter filter (§5.3): GPS noise near a boundary flips ST_Covers for a fix or
// two, and without a dwell requirement every flap would be a spurious enter/exit — an alert storm on
// a phone sitting still at the edge of "home".
//
// It is a product decision, not a tuning knob: 90 seconds is long enough to swallow the second-scale
// jitter of a stationary phone and short enough that a real arrival alerts promptly. It is a
// parameter of EvaluateDeviceGeofences (not a global) so the tests can pin the jitter/dwell boundary
// deterministically without racing a shared var; the server passes this value.
const GeofenceDebounce = 90 * time.Second

// geofenceEvalNeighbours bounds the evaluator's work: each evaluation looks at most this many fixes
// on either side of the triggering fix (in ts order), not the device's whole history. A boundary
// crossing and its dwell confirmation are local in time, so a bounded neighbourhood is all the
// evaluator needs — and a COUNT bound (rather than a time window) is robust to irregular fix cadence:
// the immediate ts-neighbours a transition is decided against are always included, however far apart
// in time they happen to be.
const geofenceEvalNeighbours = 128

// GeofenceDetail is a Place as the CRUD surface returns it: identity, name, and the ring rendered as
// GeoJSON so a caller (the map, an API client) can draw it without a second round-trip. FamilyID is
// carried for the same reason DeviceByID carries it — an authz decision (whose Place is this?) is made
// before the query that acts on it.
type GeofenceDetail struct {
	ID          string
	FamilyID    string
	Name        string
	AreaGeoJSON string // ST_AsGeoJSON of the polygon; longitude-first coordinates, as GeoJSON requires
}

// listGeofencesSQL returns a family's Places, ordered by name for a stable, human-meaningful listing.
// Scoped by family in the SQL, never by the caller — the same discipline as every read in read.go.
const listGeofencesSQL = `
	SELECT id, family_id, name, ST_AsGeoJSON(area::geometry)
	FROM geofences
	WHERE family_id = $1
	ORDER BY name, id`

// ListGeofences returns every Place in the family, ordered by name. A family with no Places yields an
// empty slice, never another family's Places.
func ListGeofences(ctx context.Context, q db.Querier, familyID string) ([]GeofenceDetail, error) {
	rows, err := q.Query(ctx, listGeofencesSQL, familyID)
	if err != nil {
		return nil, fmt.Errorf("list geofences for family %s: %w", familyID, err)
	}
	defer rows.Close()

	var out []GeofenceDetail
	for rows.Next() {
		var g GeofenceDetail
		if err := rows.Scan(&g.ID, &g.FamilyID, &g.Name, &g.AreaGeoJSON); err != nil {
			return nil, fmt.Errorf("scan geofence: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list geofences for family %s: %w", familyID, err)
	}
	return out, nil
}

// GeofenceByID looks up one Place by id, for the CRUD surface's authorization — a Place in another
// family is a 403, one that does not exist is a 404, and telling those apart needs its family_id.
// It is the geofence counterpart to DeviceByID.
func GeofenceByID(ctx context.Context, q db.Querier, id string) (GeofenceDetail, error) {
	var g GeofenceDetail
	err := q.QueryRow(ctx,
		`SELECT id, family_id, name, ST_AsGeoJSON(area::geometry) FROM geofences WHERE id = $1`, id).
		Scan(&g.ID, &g.FamilyID, &g.Name, &g.AreaGeoJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return GeofenceDetail{}, ErrUnknownGeofence
	}
	if err != nil {
		return GeofenceDetail{}, fmt.Errorf("look up geofence %s: %w", id, err)
	}
	return g, nil
}

// ErrUnknownGeofence is returned when no Place has the given id — the 404 side of the CRUD authz
// split, distinct from the 403 a Place in another family gets.
var ErrUnknownGeofence = errors.New("no geofence has that id")

// UpdateGeofence replaces a Place's name and ring in place, keeping its id (and therefore its event
// history) stable. It validates the new ring exactly as CreateGeofence does — a bowtie or an
// out-of-range vertex is a typed error and nothing is written — because an edit must not be able to
// turn a working Place into one that silently covers nothing. It is scoped by family: an id that
// exists in another family updates nothing and returns false, never reshapes a stranger's Place.
func UpdateGeofence(ctx context.Context, q db.Querier, familyID, id, name string, ring []Point) (bool, error) {
	wkt, err := polygonWKT(ring)
	if err != nil {
		return false, fmt.Errorf("update geofence %q: %w", name, err)
	}

	// Validate through PostGIS before touching the row, same as CreateGeofence: a caller gets a
	// sentence naming the problem, and an invalid ring can never land even via this path.
	var valid bool
	var reason string
	if err := q.QueryRow(ctx,
		`SELECT ST_IsValid(g::geometry), ST_IsValidReason(g::geometry)
		 FROM (SELECT ST_GeogFromText($1) AS g) s`, wkt).Scan(&valid, &reason); err != nil {
		return false, fmt.Errorf("update geofence %q: check the area is a valid polygon: %w", name, err)
	}
	if !valid {
		return false, fmt.Errorf("%w: %s — such a ring encloses no area, so the Place would never "+
			"contain anybody", ErrInvalidGeofence, reason)
	}

	tag, err := q.Exec(ctx,
		`UPDATE geofences SET name = $3, area = ST_GeogFromText($4) WHERE id = $2 AND family_id = $1`,
		familyID, id, name, wkt)
	if err != nil {
		return false, fmt.Errorf("update geofence %q: %w", name, err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteGeofence removes a Place, family-scoped, reporting whether a row was actually deleted (so a
// caller can answer 404 for an id that is not theirs or does not exist). The event log's
// ON DELETE CASCADE takes this Place's geofence_events with it — deleting a Place forgets its
// crossings, which is the intended meaning of removing it.
func DeleteGeofence(ctx context.Context, q db.Querier, familyID, id string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM geofences WHERE id = $1 AND family_id = $2`, id, familyID)
	if err != nil {
		return false, fmt.Errorf("delete geofence %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// GeofenceEvent is one recorded crossing on the wire/read side: which device crossed which Place,
// which way, and the device event-time of the fix that did it.
type GeofenceEvent struct {
	DeviceID     string
	GeofenceID   string
	GeofenceName string
	Transition   string // "enter" | "exit"
	FixTS        time.Time
}

// GeofenceEventRow is one crossing as the READ route serves it: the crossing itself plus the
// family's own name for the device that made it.
//
// The device name is a read-side field and lives here rather than on GeofenceEvent on purpose. The
// evaluator's GeofenceEvent is produced by a caller that ALREADY holds the store.Device (the
// ingestion path passes it straight to the push fan-out, which titles the notification with
// dev.Name), so putting the name on that type would add a field that is empty on every value the
// evaluator returns - a sometimes-populated field is the shape a silent bug hides in. The read has
// no Device in hand and joins for it, so the name belongs to the read's own type.
type GeofenceEventRow struct {
	GeofenceEvent
	// DeviceName is the family's own name for the crossing device, from the devices row the read
	// already joins to scope by family.
	DeviceName string
}

// listGeofenceEventsSQL returns a family's recent crossings, newest first. Scoped by family through
// the device join — a viewer reads only its own family's events, never another's, exactly as the S3
// reads are scoped. Ordered by fix_ts (the crossing's event-time, §5.3), id breaking ties so the
// order is total and pagination is stable.
//
// `d.name` comes off the join that was already there for the family scope: naming the device costs
// no extra query and no extra row, which is why the read route carries the name rather than a client
// resolving it from /v1/positions (that route returns coordinates, which the alert surface must not
// hold).
const listGeofenceEventsSQL = `
	SELECT e.device_id, e.geofence_id, g.name, e.transition::text, e.fix_ts, d.name
	FROM geofence_events e
	JOIN devices   d ON d.id = e.device_id
	JOIN geofences g ON g.id = e.geofence_id
	WHERE d.family_id = $1
	ORDER BY e.fix_ts DESC, e.id DESC
	LIMIT $2`

// MaxGeofenceEventsLimit caps one geofence-events read, the same guard MaxHistoryLimit puts on fix
// history: an uncapped read of an append-only log would let one request pull the whole thing into
// memory.
const MaxGeofenceEventsLimit = 1000

// ListGeofenceEvents returns the family's most recent crossings, newest first, capped. limit is
// clamped to [1, MaxGeofenceEventsLimit]. Scoped to one family in the SQL.
func ListGeofenceEvents(ctx context.Context, q db.Querier, familyID string, limit int) ([]GeofenceEventRow, error) {
	switch {
	case limit < 1:
		limit = 1
	case limit > MaxGeofenceEventsLimit:
		limit = MaxGeofenceEventsLimit
	}

	rows, err := q.Query(ctx, listGeofenceEventsSQL, familyID, limit)
	if err != nil {
		return nil, fmt.Errorf("list geofence events for family %s: %w", familyID, err)
	}
	defer rows.Close()

	var out []GeofenceEventRow
	for rows.Next() {
		var e GeofenceEventRow
		if err := rows.Scan(&e.DeviceID, &e.GeofenceID, &e.GeofenceName, &e.Transition, &e.FixTS, &e.DeviceName); err != nil {
			return nil, fmt.Errorf("scan geofence event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list geofence events for family %s: %w", familyID, err)
	}
	return out, nil
}

// --- the stream evaluator --------------------------------------------------------------------------

// reading is one fix's containment for one Place, in ts order: is the device inside at this instant?
type reading struct {
	ts     time.Time
	inside bool
}

// EvaluateDeviceGeofences recomputes the debounced enter/exit transitions for a device against every
// Place in its family, around the ts of an incoming fix, and appends any newly-confirmed ones to the
// append-only geofence_events log. It returns the events it actually appended — the newly-recorded
// crossings, in evaluation order — which is what S6's push fan-out delivers. An idempotent
// re-evaluation that writes nothing returns an empty slice, so a replayed fix produces no event AND
// no push.
//
// It is called from the ingestion path after a fix is stored (see server.ingest), so the log grows
// straight off the fix stream. It is IDEMPOTENT — re-running it for the same stored fixes appends
// nothing (the (device, geofence, fix_ts) key absorbs re-derived transitions) — which is what makes
// a replayed fix produce no duplicate event and a transient failure self-heal on the next fix.
//
// `around` is the ts of the fix that triggered the evaluation; `debounce` is the dwell the caller
// wants (the server passes GeofenceDebounce). The evaluation is per-geofence and independent, so a
// failure to append one Place's transition does not abandon the others.
func EvaluateDeviceGeofences(ctx context.Context, q db.Querier, deviceID string, around time.Time, debounce time.Duration) ([]GeofenceEvent, error) {
	// The Places to evaluate are the device's family's, resolved through the device — the evaluator
	// needs no family_id passed in, and cannot be handed the wrong one. The name is read here so an
	// appended event carries it (S6 puts the Place name in the notification, and reading it once with
	// the id is cheaper than a second lookup per crossing).
	rows, err := q.Query(ctx, `
		SELECT g.id, g.name
		FROM geofences g
		JOIN devices d ON d.family_id = g.family_id
		WHERE d.id = $1
		ORDER BY g.id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list geofences for device %s: %w", deviceID, err)
	}
	type place struct{ id, name string }
	var places []place
	for rows.Next() {
		var p place
		if err := rows.Scan(&p.id, &p.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan geofence id: %w", err)
		}
		places = append(places, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list geofences for device %s: %w", deviceID, err)
	}

	var appended []GeofenceEvent
	for _, p := range places {
		evs, err := evaluateOneGeofence(ctx, q, deviceID, p.id, p.name, around, debounce)
		if err != nil {
			return appended, err
		}
		appended = append(appended, evs...)
	}
	return appended, nil
}

// evaluateOneGeofence is EvaluateDeviceGeofences for a single Place: read the prior state, read the
// local neighbourhood of fixes in ts order, run the dwell state machine, and append what it confirms.
// It returns the events it newly appended (those the INSERT actually wrote — a re-derived transition
// absorbed by ON CONFLICT is not returned), each stamped with the Place's name for the notification.
func evaluateOneGeofence(ctx context.Context, q db.Querier, deviceID, geofenceID, geofenceName string, around time.Time, debounce time.Duration) ([]GeofenceEvent, error) {
	// The prior state: the latest recorded transition for this (device, place). Its type IS the
	// confirmed state (enter → inside, exit → outside), and its fix_ts is the floor below which the
	// forward-only evaluator will not splice new events (see the file header). Absent → the device is
	// presumed OUTSIDE, with a floor at the zero time (everything is "after" it).
	var lastTransition string
	var lastTS time.Time // stays the zero time when there is no prior event — a floor everything is after
	haveEvent := true
	if err := q.QueryRow(ctx, `
		SELECT transition::text, fix_ts
		FROM geofence_events
		WHERE device_id = $1 AND geofence_id = $2
		ORDER BY fix_ts DESC
		LIMIT 1`, deviceID, geofenceID).Scan(&lastTransition, &lastTS); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("read last geofence event for device %s place %s: %w", deviceID, geofenceID, err)
		}
		haveEvent = false
	}
	seedInside := haveEvent && lastTransition == "enter"

	// The neighbourhood: the device's fixes for this Place in ts order, bounded to a window that never
	// reaches behind the last recorded transition (fixes at or before lastTS carry no new information —
	// their state is already summarised by that event, which is the seed). We take up to
	// geofenceEvalNeighbours fixes at/before `around` and the same number after it, so the immediate
	// ts-neighbours a crossing is decided against are always present.
	//
	// ST_Covers is the containment operator (§5.3, inclusive of the boundary) — the same one
	// GeofencesContaining uses, so on-edge behaviour here matches the point-in-place query exactly.
	fixRows, err := q.Query(ctx, `
		SELECT ts, inside FROM (
			(
				SELECT f.ts AS ts, ST_Covers(g.area, f.location) AS inside
				FROM fixes f
				JOIN geofences g ON g.id = $2
				WHERE f.device_id = $1 AND f.ts > $3 AND f.ts <= $4
				ORDER BY f.ts DESC
				LIMIT $5
			)
			UNION ALL
			(
				SELECT f.ts AS ts, ST_Covers(g.area, f.location) AS inside
				FROM fixes f
				JOIN geofences g ON g.id = $2
				WHERE f.device_id = $1 AND f.ts > $3 AND f.ts > $4
				ORDER BY f.ts ASC
				LIMIT $5
			)
		) s
		ORDER BY ts ASC`,
		deviceID, geofenceID, lastTS, around, geofenceEvalNeighbours)
	if err != nil {
		return nil, fmt.Errorf("read fix neighbourhood for device %s place %s: %w", deviceID, geofenceID, err)
	}
	var readings []reading
	for fixRows.Next() {
		var r reading
		if err := fixRows.Scan(&r.ts, &r.inside); err != nil {
			fixRows.Close()
			return nil, fmt.Errorf("scan fix containment: %w", err)
		}
		readings = append(readings, r)
	}
	fixRows.Close()
	if err := fixRows.Err(); err != nil {
		return nil, fmt.Errorf("read fix neighbourhood for device %s place %s: %w", deviceID, geofenceID, err)
	}

	transitions := confirmTransitions(readings, seedInside, haveEvent, debounce)

	var appended []GeofenceEvent
	for _, tr := range transitions {
		kind := "exit"
		if tr.enter {
			kind = "enter"
		}
		// ON CONFLICT DO NOTHING on (device_id, geofence_id, fix_ts): a re-derived transition for a fix
		// that already produced one is absorbed, so replays and overlapping evaluations never duplicate.
		tag, err := q.Exec(ctx, `
			INSERT INTO geofence_events (device_id, geofence_id, transition, fix_ts)
			VALUES ($1, $2, $3::geofence_transition, $4)
			ON CONFLICT (device_id, geofence_id, fix_ts) DO NOTHING`,
			deviceID, geofenceID, kind, tr.ts)
		if err != nil {
			return appended, fmt.Errorf("append %s event for device %s place %s: %w", kind, deviceID, geofenceID, err)
		}
		// Only a row the INSERT actually wrote is a NEW crossing worth a push; a transition absorbed by
		// ON CONFLICT was already recorded (and already delivered) on the fix that first produced it.
		if tag.RowsAffected() == 1 {
			appended = append(appended, GeofenceEvent{
				DeviceID:     deviceID,
				GeofenceID:   geofenceID,
				GeofenceName: geofenceName,
				Transition:   kind,
				FixTS:        tr.ts,
			})
		}
	}
	return appended, nil
}

// transition is one confirmed crossing the state machine emits: its device event-time and direction.
type transition struct {
	ts    time.Time
	enter bool
}

// confirmTransitions is the debounced enter/exit state machine, run over one Place's readings in ts
// order, starting from seedInside (the prior confirmed state). haveSeedEvidence says whether that seed
// came from a RECORDED event (a real prior observation) rather than the default presumed-outside.
//
// Three rules together give the roadmap's §5.3 behaviour:
//
//   - DWELL (jitter debounce). A change of containment is emitted only once it has held for `debounce`:
//     from the onset (the first reading of the new state), the state must persist until some reading at
//     ts ≥ onset+debounce. A reading that flips back before then is jitter — discarded, no event. If the
//     readings run out first, the onset is PENDING: nothing is emitted now, and a later fix re-confirms
//     it. "Wait for proof" IS "a missed fix delays but never fabricates".
//
//   - OBSERVED-FROM-STATE (never fabricate a crossing we did not see, and get the ts right under
//     reordering). A transition is only confirmed once the state it is crossing FROM has actually been
//     observed — either as a recorded event (haveSeedEvidence) or as an earlier reading in this window.
//     This is what makes the log exact regardless of ARRIVAL order: when a device's fixes arrive out of
//     order, a would-be onset whose "from" side has not yet arrived is deferred, not confirmed against a
//     window that is missing its earlier fixes — so the crossing is ultimately attributed to the true
//     onset once its predecessor lands, not to whichever inside fix happened to arrive first. It also
//     means a device first SEEN inside a Place (no outside ever observed) does not get a fabricated
//     enter.
//
//   - ONSET ATTRIBUTION. The transition is stamped with the onset fix's device event-time — when the
//     crossing happened on the device clock, not when the server became sure of it.
func confirmTransitions(readings []reading, seedInside, haveSeedEvidence bool, debounce time.Duration) []transition {
	state := seedInside
	observed := haveSeedEvidence // have we actually observed the current confirmed `state`?
	var out []transition

	for i := 0; i < len(readings); i++ {
		if readings[i].inside == state {
			observed = true // the "from" state is now witnessed in-window
			continue
		}
		if !observed {
			// We have not yet seen the state this would cross FROM, so we cannot assert a crossing out
			// of it — defer. A later fix (or the arrival of the earlier fix) resolves it.
			continue
		}

		// A candidate onset: readings[i] is the first reading of a would-be new state, and its "from"
		// state has been observed.
		newState := readings[i].inside
		onset := readings[i].ts
		confirmed := false
		for j := i; j < len(readings) && readings[j].inside == newState; j++ {
			if !readings[j].ts.Before(onset.Add(debounce)) { // ts ≥ onset + debounce: the dwell is met
				confirmed = true
				break
			}
		}

		if confirmed {
			out = append(out, transition{ts: onset, enter: newState})
			state = newState
			observed = true // the onset reading witnesses the new state
			// Continue from the fix after the onset; the rest of this run matches `state` now.
			continue
		}
		// Not confirmed — jitter or pending. The confirmed state does not move; carry on. (observed is
		// unchanged: an opposite-state reading is not an observation of the current `state`.)
	}
	return out
}
