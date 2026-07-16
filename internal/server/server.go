// Package server holds tracker's HTTP surface.
//
// S0 served exactly /healthz. S2 adds the first data-carrying surface: token-authenticated
// ingestion. Two write routes sit behind a device-token gate — the first-party POST /v1/fixes
// (roadmap §5.2) and the interim, deprecatable POST /owntracks adapter that lets the stock
// OwnTracks app drive the server today. The read, stream and geofencing surfaces arrive later.
//
// The wire contract itself is documented in tracker/SPEC.md; this file is its implementation.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Pinger is the health check's view of the database. It stays a narrow interface so the
// "database is down" branch of /healthz remains testable (see server_test.go).
type Pinger interface {
	Ping(ctx context.Context) error
}

// DB is everything the server needs from the database: the health ping plus the query surface the
// ingestion handlers hand to the store. *pgxpool.Pool satisfies it. Keeping it an interface (rather
// than the concrete pool) is what lets the handler tests drive real ingestion against a
// testcontainer while the health tests use a bare stub.
type DB interface {
	Pinger
	db.Querier
}

// Notifier is the server's view of S6 push delivery: hand it the crossings the evaluator just
// recorded and it fans them out to the family's registered phones. It is deliberately fire-and-forget
// — no return value — because a push must NEVER block or fail ingestion (§5.3): the implementation
// (internal/push.EventNotifier) enqueues onto a bounded, retrying worker and returns immediately.
//
// Keeping it an interface is what lets the ingestion tests run without a real push backend (a
// capturing stub stands in) and lets a deployment with push disabled drop in a no-op.
type Notifier interface {
	NotifyGeofenceEvents(ctx context.Context, dev store.Device, events []store.GeofenceEvent)
}

// noopNotifier is the Notifier a deployment with push disabled (or a test that does not care) gets. It
// does nothing, so a crossing is still recorded to the event log and readable — there is simply no
// push. New substitutes it for a nil Notifier so no call site has to nil-check.
type noopNotifier struct{}

func (noopNotifier) NotifyGeofenceEvents(context.Context, store.Device, []store.GeofenceEvent) {}

// healthTimeout bounds the health check's database ping. A /healthz that hangs is worse than one
// that fails: an orchestrator waiting on it cannot tell "slow" from "wedged".
const healthTimeout = 2 * time.Second

// maxBodyBytes caps a fix payload. A location report is a few hundred bytes; anything past 64 KiB is
// a mistake or an attack, and reading it into memory before rejecting it would be the attack
// working. http.MaxBytesReader turns an over-large body into a clean 400, not an OOM.
const maxBodyBytes = 64 << 10

// New builds the HTTP handler. notifier delivers S6 push alerts for the crossings ingestion records;
// a nil notifier means push is disabled (a no-op is substituted), so a deployment without a push
// backend still ingests, evaluates and serves the event log — it just sends no alerts.
func New(database DB, notifier Notifier, logger *slog.Logger) http.Handler {
	if notifier == nil {
		notifier = noopNotifier{}
	}

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// Deliberately NOT middleware.RealIP — it rewrites RemoteAddr from client-controlled headers
	// (GHSA-3fxj-6jh8-hvhx). Nothing here consumes the client IP; when something does it needs a
	// trusted-proxy configuration, not this.

	r.Get("/healthz", healthz(database, logger))

	// The write surface. Every route in this group requires a valid device token, and the
	// authenticated device — never a field in the body — is the identity a fix is stored under
	// (§7: a device writes only its own fixes). That is authz by construction: there is no
	// device_id in either payload for a caller to forge.
	r.Group(func(r chi.Router) {
		r.Use(requireDevice(database, logger))
		r.Post("/v1/fixes", postFix(database, notifier, logger))
		r.Post("/owntracks", postOwnTracks(database, notifier, logger))
	})

	// The read surface (S3). Every route requires a valid VIEWER token — a separate credential from
	// the device token above (§7: a device writes its own fixes, a viewer reads its family, never the
	// reverse) — and every query is scoped to the viewer's own family. A cross-family read is a 403,
	// an empty result is [], and another family's data never leaves this boundary.
	r.Group(func(r chi.Router) {
		r.Use(requireViewer(database, logger))
		r.Get("/v1/positions", getPositions(database, logger))
		r.Get("/v1/devices/{id}/history", getDeviceHistory(database, logger))
		r.Get("/v1/near", getNear(database, logger))

		// The S5 geofencing read surface: the family's Places and the append-only enter/exit log.
		// Read-only under the viewer credential; Places are created/edited by the operator CLI, not
		// over HTTP (§7 — see geofence.go).
		r.Get("/v1/places", getPlaces(database, logger))
		r.Get("/v1/geofence-events", getGeofenceEvents(database, logger))

		// The S6 push-registration surface: a viewer registers the push endpoint of its phone so a
		// family's crossings can reach it. It is a WRITE by the viewer credential — a viewer registers
		// only under itself (the viewer id comes from the token, never the body) — which is why it sits
		// in the viewer group rather than behind the device token.
		r.Post("/v1/push-subscriptions", registerPushSubscription(database, logger))
	})

	// The live-map surface (S4). GET /v1/stream is an SSE feed of a family's position updates; it
	// authenticates the viewer INSIDE the handler rather than via the middleware above, because the
	// browser EventSource that consumes it cannot set an Authorization header and must pass the token
	// in the query string (see getStream / SPEC.md). /map and /static are the minimal Leaflet page and
	// its vendored assets — a static shell that carries no family data itself; the data behind it is
	// still the authenticated stream.
	r.Get("/v1/stream", getStream(database, logger))
	r.Get("/map", serveMapPage(logger))
	r.Handle("/static/*", mapAssets())

	return r
}

type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

// healthz reports whether tracker can actually serve. It PINGS THE DATABASE rather than returning a
// bare 200: tracker cannot do anything useful without PostGIS, so an unreachable database is a 503.
func healthz(pinger Pinger, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()

		resp := healthResponse{Status: "ok", Database: "up"}
		code := http.StatusOK

		if err := pinger.Ping(ctx); err != nil {
			logger.WarnContext(ctx, "health check: database unreachable", "error", err)
			resp = healthResponse{Status: "unavailable", Database: "down"}
			code = http.StatusServiceUnavailable
		}

		writeJSON(w, logger, code, resp)
	}
}

// deviceCtxKey carries the authenticated device down to the handler. An unexported type makes the
// key uncollidable with any other package's context values.
type deviceCtxKey struct{}

// requireDevice authenticates the bearer token and puts the resolved device in the request context,
// or answers 401 and stops. A request that reaches a handler in this group has, by construction, a
// known device behind it.
func requireDevice(database DB, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := auth.FromRequest(r)
			if !ok {
				unauthorized(w, logger, "missing or malformed bearer token")
				return
			}
			hash := tok.Hash()
			dev, err := store.AuthenticateDevice(r.Context(), database, hash[:])
			if err != nil {
				if errors.Is(err, store.ErrUnknownToken) {
					unauthorized(w, logger, "unknown or revoked token")
					return
				}
				// A real database error is ours, not the caller's. Do not leak it; do log it.
				logger.ErrorContext(r.Context(), "authenticate device", "error", err)
				writeError(w, logger, http.StatusInternalServerError, "internal", "could not verify the token")
				return
			}
			ctx := context.WithValue(r.Context(), deviceCtxKey{}, dev)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// deviceFrom returns the device requireDevice placed in the context. It is only ever called from a
// handler inside the authenticated group, so the assertion cannot fail in practice; if it somehow
// did, the zero Device's empty id would be rejected by the fixes' foreign key rather than mis-stored.
func deviceFrom(ctx context.Context) store.Device {
	dev, _ := ctx.Value(deviceCtxKey{}).(store.Device)
	return dev
}

// fixRequest is the first-party report schema (§5.2). Every field is a POINTER so that ABSENT is
// distinguishable from ZERO: lat/lon of 0,0 is Null Island, a real place, and a battery of 0% is a
// real reading — none of them may be confused with "the client did not send this".
type fixRequest struct {
	Lat      *float64 `json:"lat"`      // required, degrees
	Lon      *float64 `json:"lon"`      // required, degrees
	TS       *int64   `json:"ts"`       // required, epoch SECONDS of the fix (device clock)
	Accuracy *float64 `json:"accuracy"` // optional, metres
	Battery  *int16   `json:"battery"`  // optional, percent 0..100
	Speed    *float64 `json:"speed"`    // optional, metres/second
	Trigger  *string  `json:"trigger"`  // optional, what prompted the report
	MsgID    *string  `json:"msg_id"`   // optional, client-generated idempotency correlator
}

// postFix ingests a first-party report. Strict: unknown fields and trailing data are rejected, so a
// typo'd or malformed payload is a typed 400 that stores nothing, never a silently half-read guess.
func postFix(database DB, notifier Notifier, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fixRequest
		if err := decodeStrict(w, r, &req); err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}

		if req.Lat == nil || req.Lon == nil || req.TS == nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", "lat, lon and ts are required")
			return
		}
		if err := validateOptionalMetrics(req.Accuracy, req.Battery, req.Speed); err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}

		f := store.Fix{
			DeviceID:      deviceFrom(r.Context()).ID,
			TS:            time.Unix(*req.TS, 0).UTC(),
			Lon:           *req.Lon,
			Lat:           *req.Lat,
			AccuracyM:     req.Accuracy,
			BatteryPct:    req.Battery,
			SpeedMPS:      req.Speed,
			TriggerReason: req.Trigger,
			MsgID:         req.MsgID,
		}

		inserted, ok := ingest(w, r, database, notifier, logger, f)
		if !ok {
			return
		}
		// A newly stored fix is 201; an absorbed replay is 200. Both are success — idempotency
		// means a replay is not an error — and the distinction lets a client tell "you have it now"
		// from "you already had it".
		code, status := http.StatusOK, "duplicate"
		if inserted {
			code, status = http.StatusCreated, "stored"
		}
		writeJSON(w, logger, code, map[string]any{"status": status, "deduped": !inserted})
	}
}

// owntracksRequest is the subset of the OwnTracks `_type:location` payload tracker maps onto its own
// schema (roadmap §5.2, interim adapter). It is LENIENT by design — the real app sends a dozen more
// fields (tid, alt, vac, cog, conn, …) — so unknown fields are ignored rather than rejected. Only a
// location message carries a fix; other `_type`s are acknowledged and dropped.
type owntracksRequest struct {
	Type *string  `json:"_type"`
	Lat  *float64 `json:"lat"`
	Lon  *float64 `json:"lon"`
	Tst  *int64   `json:"tst"`  // epoch seconds
	Acc  *float64 `json:"acc"`  // accuracy, metres
	Batt *int16   `json:"batt"` // battery, percent
	Vel  *float64 `json:"vel"`  // velocity, KILOMETRES PER HOUR — converted below
	T    *string  `json:"t"`    // trigger source
}

// postOwnTracks is the interim adapter. It authenticates and stores exactly like /v1/fixes — same
// validation, same idempotent upsert, same partition mitigation — but speaks OwnTracks' wire format
// and honours its response contract: a 2xx with a JSON array body (here always `[]`, "nothing to
// send back"), because the app retries on anything else.
func postOwnTracks(database DB, notifier Notifier, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		var req owntracksRequest
		// Lenient decode: OwnTracks payloads carry many fields we do not model.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", "invalid OwnTracks JSON: "+err.Error())
			return
		}

		// Only location messages carry a fix. A transition/waypoint/lwt message is acknowledged and
		// dropped — dropping it with a non-2xx would make the app retry it forever.
		if req.Type == nil || *req.Type != "location" {
			writeOwnTracksAck(w, logger)
			return
		}

		if req.Lat == nil || req.Lon == nil || req.Tst == nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", "an OwnTracks location needs lat, lon and tst")
			return
		}
		if err := validateOptionalMetrics(req.Acc, req.Batt, nil); err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}

		// OwnTracks reports velocity in km/h; tracker stores m/s. Convert, keeping absent absent.
		var speed *float64
		if req.Vel != nil {
			if *req.Vel < 0 || isNotFinite(*req.Vel) {
				writeError(w, logger, http.StatusBadRequest, "malformed", "vel must be a finite, non-negative speed")
				return
			}
			mps := *req.Vel / 3.6
			speed = &mps
		}

		f := store.Fix{
			DeviceID:      deviceFrom(r.Context()).ID,
			TS:            time.Unix(*req.Tst, 0).UTC(),
			Lon:           *req.Lon,
			Lat:           *req.Lat,
			AccuracyM:     req.Acc,
			BatteryPct:    req.Batt,
			SpeedMPS:      speed,
			TriggerReason: req.T,
		}

		if _, ok := ingest(w, r, database, notifier, logger, f); !ok {
			return
		}
		writeOwnTracksAck(w, logger)
	}
}

// ingest runs the shared store write for both adapters and translates a FAILURE into an HTTP error
// response itself. It returns (inserted, ok): on ok=false it has already written the error and the
// caller must stop; on ok=true it has written NOTHING, leaving the success response to the caller
// (first-party JSON vs the OwnTracks array), which differ.
func ingest(w http.ResponseWriter, r *http.Request, database DB, notifier Notifier, logger *slog.Logger, f store.Fix) (inserted, ok bool) {
	inserted, err := store.IngestFix(r.Context(), database, f, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCoordinateOutOfRange), errors.Is(err, store.ErrTimestampOutOfWindow):
			// A validation failure is the caller's to fix, and — the fail-safe — nothing was stored.
			writeError(w, logger, http.StatusBadRequest, "invalid_fix", err.Error())
		default:
			logger.ErrorContext(r.Context(), "ingest fix", "error", err, "device_id", f.DeviceID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not store the fix")
		}
		return false, false
	}

	// S5: drive the geofence evaluator off the freshly-stored fix, only for a genuinely new one — a
	// replay was already evaluated when it first landed, and re-evaluating is a no-op anyway. A
	// geofence-evaluation failure does NOT fail the request: the fix itself is safely stored, and the
	// evaluator is a deterministic projection that self-heals on the next fix (§5.3 "a missed fix
	// delays but never fabricates"). Failing the client's POST over a downstream projection hiccup —
	// when its retry would find the fix already stored and skip evaluation again — would be the wrong
	// trade. So it is logged, loudly, and the fix's success stands.
	if inserted {
		events, err := store.EvaluateDeviceGeofences(r.Context(), database, f.DeviceID, f.TS, store.GeofenceDebounce)
		if err != nil {
			logger.ErrorContext(r.Context(), "evaluate geofences", "error", err, "device_id", f.DeviceID)
		}
		// S6: hand the newly-recorded crossings to push delivery. The device carries its own family and
		// name (from the auth lookup), which is all the notifier needs to fan out and title the alert.
		// This is fire-and-forget by contract — the notifier enqueues onto a bounded worker and returns
		// at once — so a push backend can never block or fail the fix ingest. Events that failed to
		// derive above are simply absent here, so a partial evaluation still pushes what it did record.
		if len(events) > 0 {
			notifier.NotifyGeofenceEvents(r.Context(), deviceFrom(r.Context()), events)
		}
	}
	return inserted, true
}

// validateOptionalMetrics rejects out-of-range optional readings in Go, before the SQL, matching the
// coordinate rule: a bad value is a typed 400, never a stored guess or a raw constraint violation.
// The table also CHECK-constrains these, but a caller deserves a sentence, not a driver error.
func validateOptionalMetrics(accuracy *float64, battery *int16, speed *float64) error {
	if accuracy != nil && (*accuracy < 0 || isNotFinite(*accuracy)) {
		return errors.New("accuracy must be a finite, non-negative number of metres")
	}
	if speed != nil && (*speed < 0 || isNotFinite(*speed)) {
		return errors.New("speed must be a finite, non-negative number")
	}
	if battery != nil && (*battery < 0 || *battery > 100) {
		return errors.New("battery must be a percentage between 0 and 100")
	}
	return nil
}

func isNotFinite(f float64) bool {
	return math.IsNaN(f) || math.IsInf(f, 0)
}

// decodeStrict reads a JSON body with a size cap, unknown-field rejection, and a no-trailing-data
// check. Strictness here is the fail-safe: a payload the server cannot fully account for is refused
// rather than partially believed.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	if dec.More() {
		return errors.New("request body has trailing data after the JSON object")
	}
	return nil
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func unauthorized(w http.ResponseWriter, logger *slog.Logger, message string) {
	// Advertise the scheme so a client knows how to authenticate; the OwnTracks app also accepts
	// Basic, but Bearer is the canonical answer.
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, logger, http.StatusUnauthorized, "unauthorized", message)
}

func writeError(w http.ResponseWriter, logger *slog.Logger, code int, errCode, message string) {
	writeJSON(w, logger, code, errorResponse{Error: errCode, Message: message})
}

// writeOwnTracksAck writes the OwnTracks success response: 200 with an empty JSON array. The app
// expects an array (of friend cards / commands); an empty one means "nothing for you".
func writeOwnTracksAck(w http.ResponseWriter, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("[]")); err != nil {
		logger.Error("write OwnTracks ack", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		logger.Error("write response", "error", err)
	}
}
