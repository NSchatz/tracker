package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// S3 read surface. Three family-scoped read routes sit behind a VIEWER-token gate, distinct from
// the device-token gate on the write routes: a viewer reads its family, a device writes its own
// fixes, and never the other way around (§7). Every route answers only the caller's own family —
// an empty result is an empty list, never a leak of another family's data, and a request for a
// device in another family is a 403.

// defaultHistoryLimit is the page size when a caller does not specify one. Small enough to be a
// cheap default, large enough to be a useful page; a caller wanting more asks with ?limit=, capped
// at store.MaxHistoryLimit.
const defaultHistoryLimit = 100

// viewerCtxKey carries the authenticated viewer to the read handlers. An unexported key type keeps
// it uncollidable with any other package's context values, exactly as deviceCtxKey does for writes.
type viewerCtxKey struct{}

// requireViewer authenticates the bearer token against the VIEWERS table and puts the resolved
// viewer in the request context, or answers 401 and stops. A device token authenticates no viewer
// here, so it cannot reach a read handler — that is the read/write separation §7 requires, enforced
// by which table the credential is looked up in.
func requireViewer(database DB, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := auth.FromRequest(r)
			if !ok {
				unauthorized(w, logger, "missing or malformed bearer token")
				return
			}
			hash := tok.Hash()
			v, err := store.AuthenticateViewer(r.Context(), database, hash[:])
			if err != nil {
				if errors.Is(err, store.ErrUnknownToken) {
					unauthorized(w, logger, "unknown or revoked token")
					return
				}
				logger.ErrorContext(r.Context(), "authenticate viewer", "error", err)
				writeError(w, logger, http.StatusInternalServerError, "internal", "could not verify the token")
				return
			}
			ctx := context.WithValue(r.Context(), viewerCtxKey{}, v)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// viewerFrom returns the viewer requireViewer placed in the context. Called only from a handler
// inside the viewer-gated group, so the assertion holds by construction; a zero Viewer's empty
// FamilyID would match no family's rows rather than leak, if it somehow did not.
func viewerFrom(ctx context.Context) store.Viewer {
	v, _ := ctx.Value(viewerCtxKey{}).(store.Viewer)
	return v
}

// positionResponse is one device's latest whereabouts on the wire. Timestamps are epoch seconds, to
// match the ingestion protocol's `ts` (SPEC.md) — one time convention across the API.
type positionResponse struct {
	DeviceID   string  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	TS         int64   `json:"ts"`
	ReceivedAt int64   `json:"received_at"`
}

// getPositions serves GET /v1/positions: the latest fix per device in the caller's family.
func getPositions(database DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := viewerFrom(r.Context())

		positions, err := store.LatestPositions(r.Context(), database, v.FamilyID)
		if err != nil {
			logger.ErrorContext(r.Context(), "latest positions", "error", err, "family_id", v.FamilyID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not read positions")
			return
		}

		// Always a JSON array, never null: an empty family is [], the fail-safe answer.
		out := make([]positionResponse, 0, len(positions))
		for _, p := range positions {
			out = append(out, positionResponse{
				DeviceID:   p.DeviceID,
				DeviceName: p.DeviceName,
				Lat:        p.Lat,
				Lon:        p.Lon,
				TS:         p.TS.Unix(),
				ReceivedAt: p.ReceivedAt.Unix(),
			})
		}
		writeJSON(w, logger, http.StatusOK, out)
	}
}

// historyResponse is one fix in a device's history. Optional metrics are pointers with omitempty:
// an absent reading is omitted from the JSON, never fabricated as a zero (§5.2).
type historyResponse struct {
	TS         int64    `json:"ts"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	Accuracy   *float64 `json:"accuracy,omitempty"`
	Battery    *int16   `json:"battery,omitempty"`
	Speed      *float64 `json:"speed,omitempty"`
	Trigger    *string  `json:"trigger,omitempty"`
	ReceivedAt int64    `json:"received_at"`
}

// getDeviceHistory serves GET /v1/devices/{id}/history: a time-windowed, paginated page of one
// device's fixes, newest first — but only if that device is in the caller's family.
//
// The authorization is the point of this handler. It resolves the device FIRST and decides on the
// status code the roadmap's authz matrix specifies:
//
//   - the device is in the caller's family  → 200 (its history, possibly empty)
//   - the device is in a DIFFERENT family    → 403 (cross-family read, refused — §7)
//   - no such device                         → 404
//   - the id is not a UUID                    → 400
//
// An empty window is a 200 with [], never another family's data.
func getDeviceHistory(database DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := viewerFrom(r.Context())

		deviceID := chi.URLParam(r, "id")
		if _, err := uuid.Parse(deviceID); err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", "device id must be a UUID")
			return
		}

		dev, err := store.DeviceByID(r.Context(), database, deviceID)
		if err != nil {
			if errors.Is(err, store.ErrUnknownDevice) {
				writeError(w, logger, http.StatusNotFound, "not_found", "no such device")
				return
			}
			logger.ErrorContext(r.Context(), "look up device", "error", err, "device_id", deviceID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not read the device")
			return
		}
		if dev.FamilyID != v.FamilyID {
			// A viewer may read only its own family. Refuse a device in another family — do not
			// silently return that family's history, and do not pretend the device is absent.
			writeError(w, logger, http.StatusForbidden, "forbidden", "that device belongs to another family")
			return
		}

		from, to, err := parseWindow(r)
		if err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}
		limit, offset, err := parsePage(r)
		if err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}

		fixes, err := store.DeviceHistory(r.Context(), database, v.FamilyID, deviceID, from, to, limit, offset)
		if err != nil {
			logger.ErrorContext(r.Context(), "device history", "error", err, "device_id", deviceID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not read history")
			return
		}

		out := make([]historyResponse, 0, len(fixes))
		for _, f := range fixes {
			out = append(out, historyResponse{
				TS:         f.TS.Unix(),
				Lat:        f.Lat,
				Lon:        f.Lon,
				Accuracy:   f.AccuracyM,
				Battery:    f.BatteryPct,
				Speed:      f.SpeedMPS,
				Trigger:    f.TriggerReason,
				ReceivedAt: f.ReceivedAt.Unix(),
			})
		}
		writeJSON(w, logger, http.StatusOK, out)
	}
}

// nearbyResponse is one device found near the queried point, with the distance that ranked it.
type nearbyResponse struct {
	DeviceID   string  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	TS         int64   `json:"ts"`
	DistanceM  float64 `json:"distance_m"`
}

// getNear serves GET /v1/near?lat=&lon=&m=: the caller's family's fixes within m metres of the
// point, nearest first. It is the §5.1 proximity template (store.FixesNear — ST_DWithin to
// pre-filter on the GiST index, ST_Distance to rank), family-scoped to the viewer.
func getNear(database DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := viewerFrom(r.Context())

		lat, err := floatParam(r, "lat")
		if err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}
		lon, err := floatParam(r, "lon")
		if err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}
		radiusM, err := floatParam(r, "m")
		if err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}
		// A radius is a caller-supplied distance; reject a nonsensical one HERE as a 400 rather than
		// letting FixesNear return a plain error we would have to treat as a 500. (FixesNear guards it
		// too — this just keeps the status code honest about whose fault it is.)
		if radiusM < 0 || isNotFinite(radiusM) {
			writeError(w, logger, http.StatusBadRequest, "malformed", "m must be a finite, non-negative number of metres")
			return
		}

		near, err := store.FixesNear(r.Context(), database, v.FamilyID, lon, lat, radiusM)
		if err != nil {
			// A bad coordinate is the caller's to fix (ValidateLonLat) — a 400 with the reason;
			// anything else is ours.
			if errors.Is(err, store.ErrCoordinateOutOfRange) {
				writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
				return
			}
			logger.ErrorContext(r.Context(), "fixes near", "error", err, "family_id", v.FamilyID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not read nearby fixes")
			return
		}

		out := make([]nearbyResponse, 0, len(near))
		for _, n := range near {
			out = append(out, nearbyResponse{
				DeviceID:   n.DeviceID,
				DeviceName: n.DeviceName,
				Lat:        n.Lat,
				Lon:        n.Lon,
				TS:         n.TS.Unix(),
				DistanceM:  n.DistanceM,
			})
		}
		writeJSON(w, logger, http.StatusOK, out)
	}
}

// parseWindow reads the optional ?from= and ?to= epoch-seconds bounds. Absent means unbounded on
// that side (the zero time.Time store.DeviceHistory treats as NULL). Epoch seconds match the
// ingestion protocol's `ts`, so one time convention spans read and write.
func parseWindow(r *http.Request) (from, to time.Time, err error) {
	from, err = epochParam(r, "from")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err = epochParam(r, "to")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from, to, nil
}

// parsePage reads the optional ?limit= and ?offset= pagination controls. The store clamps limit to
// [1, MaxHistoryLimit] and a negative offset to 0; this only rejects values that are not integers
// at all, so a garbage page control is a loud 400 rather than a silently ignored one.
func parsePage(r *http.Request) (limit, offset int, err error) {
	limit = defaultHistoryLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, errors.New("limit must be an integer")
		}
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		offset, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, errors.New("offset must be an integer")
		}
	}
	return limit, offset, nil
}

// epochParam reads an optional epoch-seconds query parameter into a time.Time, or the zero time if
// it is absent. A present-but-unparseable value is an error, never a silent default.
func epochParam(r *http.Request, name string) (time.Time, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return time.Time{}, nil
	}
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, errors.New(name + " must be an epoch-seconds integer")
	}
	return time.Unix(secs, 0).UTC(), nil
}

// floatParam reads a REQUIRED float query parameter, erroring on absence or garbage so a missing
// coordinate is a typed 400, never a defaulted 0 that would silently query the wrong place.
func floatParam(r *http.Request, name string) (float64, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return 0, errors.New(name + " is required")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errors.New(name + " must be a number")
	}
	return f, nil
}
