package server

// S5 read surface for server-side geofencing. Two viewer-scoped read routes let a family SEE the
// Places defined for it and the crossings recorded against them — the enter/exit log that S6 will
// deliver as a push and that, until then, is observable here.
//
// Why READ-only, and why VIEWER-scoped: creating and editing Places is privileged family
// configuration, issued by the operator out of band exactly as device enrollment and viewer accounts
// are (there is no admin login yet — see cmd/tracker `add-place`). So the network surface a viewer
// gets is the same read capability §7 grants everywhere else: a viewer reads its own family's Places
// and events, and only its own. The write side of "Places CRUD" lives in the store (create / update /
// delete) and is driven by the operator CLI, not by a viewer token — keeping the read/write
// credential separation §7 turns on.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/NSchatz/tracker/internal/store"
)

// placeResponse is one Place on the wire: identity, name, and its ring as a GeoJSON geometry so a map
// can draw it directly. The GeoJSON is emitted as raw JSON (it already IS JSON from ST_AsGeoJSON), not
// re-encoded as a string.
type placeResponse struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Area json.RawMessage `json:"area"`
}

// getPlaces serves GET /v1/places: every Place in the caller's family, ordered by name. An empty
// family is [], never null and never another family's Places.
func getPlaces(database DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := viewerFrom(r.Context())

		places, err := store.ListGeofences(r.Context(), database, v.FamilyID)
		if err != nil {
			logger.ErrorContext(r.Context(), "list places", "error", err, "family_id", v.FamilyID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not read places")
			return
		}

		out := make([]placeResponse, 0, len(places))
		for _, p := range places {
			out = append(out, placeResponse{
				ID:   p.ID,
				Name: p.Name,
				Area: json.RawMessage(p.AreaGeoJSON),
			})
		}
		writeJSON(w, logger, http.StatusOK, out)
	}
}

// geofenceEventResponse is one recorded crossing on the wire. `ts` is the crossing fix's device
// event-time in epoch seconds — the same time convention as every other timestamp in the API (§5.3
// "derive from ts order").
type geofenceEventResponse struct {
	DeviceID   string `json:"device_id"`
	PlaceID    string `json:"place_id"`
	PlaceName  string `json:"place_name"`
	Transition string `json:"transition"` // "enter" | "exit"
	TS         int64  `json:"ts"`
}

// defaultGeofenceEventsLimit is the page size when a caller does not ask, mirroring
// defaultHistoryLimit: a useful, cheap default, with ?limit= (clamped in the store) for more.
const defaultGeofenceEventsLimit = 100

// getGeofenceEvents serves GET /v1/geofence-events: the family's recent crossings, newest first. It
// is the read side of the append-only log — a viewer can watch enter/exit happen here before push
// delivery (S6) exists. Scoped to the caller's family in the store SQL; an empty log is [].
func getGeofenceEvents(database DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := viewerFrom(r.Context())

		limit := defaultGeofenceEventsLimit
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				writeError(w, logger, http.StatusBadRequest, "malformed", "limit must be an integer")
				return
			}
			limit = n
		}

		events, err := store.ListGeofenceEvents(r.Context(), database, v.FamilyID, limit)
		if err != nil {
			logger.ErrorContext(r.Context(), "list geofence events", "error", err, "family_id", v.FamilyID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not read geofence events")
			return
		}

		out := make([]geofenceEventResponse, 0, len(events))
		for _, e := range events {
			out = append(out, geofenceEventResponse{
				DeviceID:   e.DeviceID,
				PlaceID:    e.GeofenceID,
				PlaceName:  e.GeofenceName,
				Transition: e.Transition,
				TS:         e.FixTS.Unix(),
			})
		}
		writeJSON(w, logger, http.StatusOK, out)
	}
}
