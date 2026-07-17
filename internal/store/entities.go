package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/jackc/pgx/v5"
)

// TokenHashLen is the length of the SHA-256 digest stored in devices.token_hash and
// viewers.token_hash. The schema pins it with a CHECK; this pins it on the way in, so the
// error a caller gets is a sentence rather than a constraint violation.
const TokenHashLen = 32

// ErrInvalidTokenHash is returned when a credential is not a SHA-256 digest.
//
// The failure this guards is not a typo. It is a caller passing the RAW TOKEN where the hash
// belongs — which "works" (bytea takes anything), and quietly turns the database into a file
// of live credentials for every family it holds.
var ErrInvalidTokenHash = errors.New("invalid token hash")

// Point is a lon/lat pair. Longitude first, in the field order, in the constructor, in the SQL
// — everywhere, so there is no seam for the axes to get swapped at.
type Point struct {
	Lon float64
	Lat float64
}

// CreateFamily inserts a family and returns its id.
func CreateFamily(ctx context.Context, q db.Querier, name string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create family %q: %w", name, err)
	}
	return id, nil
}

// CreateDevice enrolls a phone into a family. tokenHash is a SHA-256 digest of the bearer
// token — never the token itself (see ErrInvalidTokenHash).
func CreateDevice(ctx context.Context, q db.Querier, familyID, name string, tokenHash []byte) (string, error) {
	if len(tokenHash) != TokenHashLen {
		return "", fmt.Errorf("%w: got %d bytes, want a %d-byte SHA-256 digest", ErrInvalidTokenHash, len(tokenHash), TokenHashLen)
	}
	var id string
	err := q.QueryRow(ctx,
		`INSERT INTO devices (family_id, name, token_hash) VALUES ($1, $2, $3) RETURNING id`,
		familyID, name, tokenHash).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create device %q: %w", name, err)
	}
	return id, nil
}

// Device is an enrolled phone, as much of it as authorization needs: its id, and — the reason this
// type exists rather than a bare id — the family it belongs to. Every write is scoped by FamilyID
// (§7), and carrying it out of the single token lookup means the ingestion path never has to ask the
// database "whose device is this" a second time, nor trust the network to tell it.
type Device struct {
	ID       string
	FamilyID string
	Name     string
}

// ErrUnknownToken is returned when no device matches the presented credential. It is deliberately
// indistinguishable from a device that does not exist: the caller answers 401 either way, and
// telling "wrong token" apart from "no such device" only helps an attacker enumerate.
var ErrUnknownToken = errors.New("no device matches the presented token")

// AuthenticateDevice looks up the device whose token_hash is the given digest.
//
// tokenHash is a SHA-256 digest (auth.Token.Hash) — never a raw token. The lookup is an indexed
// equality on token_hash, which is both fast and the correct shape of comparison here: what is being
// matched is a 256-bit digest, not a short secret, so a preimage attack is the only way to forge a
// match and a timing side-channel on the index buys an attacker nothing they could act on. The guard
// on tokenHash's length is the same one CreateDevice applies on the way in — a caller that passes a
// raw token (the wrong length) is refused rather than silently never matching.
func AuthenticateDevice(ctx context.Context, q db.Querier, tokenHash []byte) (Device, error) {
	if len(tokenHash) != TokenHashLen {
		return Device{}, fmt.Errorf("%w: got %d bytes, want a %d-byte SHA-256 digest", ErrInvalidTokenHash, len(tokenHash), TokenHashLen)
	}
	var d Device
	err := q.QueryRow(ctx,
		// The expiry test is part of the WHERE, not a field read back and checked in Go, so an
		// expired token matches NO row and is indistinguishable from an unknown one — the same
		// uniform ErrUnknownToken, the same 401, nothing for an attacker to tell apart. now() is
		// the DATABASE's clock: expiry is a server-side decision, never one a client could move by
		// lying about the time. A NULL expires_at (the default, and every pre-S7 token) never expires.
		`SELECT id, family_id, name FROM devices
		 WHERE token_hash = $1 AND (expires_at IS NULL OR expires_at > now())`, tokenHash).
		Scan(&d.ID, &d.FamilyID, &d.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrUnknownToken
	}
	if err != nil {
		return Device{}, fmt.Errorf("authenticate device: %w", err)
	}
	return d, nil
}

// RotateDeviceToken replaces a device's credential: it writes a new token_hash and a new expiry in
// ONE statement, so the previous token stops authenticating the instant the new one is minted —
// there is no window in which both are live. expiresAt is nil for a token that never expires, or a
// future instant for a short-lived one (§7). It returns false (found) if no device has that id, so
// the caller can tell "rotated" from "no such device" rather than silently succeeding over nothing.
//
// newHash is a SHA-256 digest (auth.Token.Hash), never a raw token — the same length guard
// CreateDevice applies, for the same reason: a raw token stored where the hash belongs turns the
// row into a live credential.
func RotateDeviceToken(ctx context.Context, q db.Querier, deviceID string, newHash []byte, expiresAt *time.Time) (bool, error) {
	if len(newHash) != TokenHashLen {
		return false, fmt.Errorf("%w: got %d bytes, want a %d-byte SHA-256 digest", ErrInvalidTokenHash, len(newHash), TokenHashLen)
	}
	tag, err := q.Exec(ctx,
		`UPDATE devices SET token_hash = $2, expires_at = $3 WHERE id = $1`, deviceID, newHash, expiresAt)
	if err != nil {
		return false, fmt.Errorf("rotate device token: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ErrUnknownDevice is returned when no device has the given id. It is the read-side counterpart
// to ErrUnknownToken: the history endpoint uses it to answer 404 for a device that does not exist,
// distinct from the 403 a device in ANOTHER family gets (see DeviceByID's callers).
var ErrUnknownDevice = errors.New("no device has that id")

// DeviceByID looks up a device by its id, for the read API's family-scoped authorization.
//
// It exists so the history endpoint can make the authz decision the roadmap's §7 requires BEFORE
// it runs a query: a device in the caller's own family is readable, a device in a DIFFERENT family
// is a 403, and a device that does not exist is a 404. That three-way split needs the device's
// family_id, which is exactly what this returns. The history query is ALSO scoped by family in its
// own SQL (defence in depth — see DeviceHistory), so this lookup decides the status code, not
// whether the data leaks.
func DeviceByID(ctx context.Context, q db.Querier, deviceID string) (Device, error) {
	var d Device
	err := q.QueryRow(ctx,
		`SELECT id, family_id, name FROM devices WHERE id = $1`, deviceID).
		Scan(&d.ID, &d.FamilyID, &d.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrUnknownDevice
	}
	if err != nil {
		return Device{}, fmt.Errorf("look up device %s: %w", deviceID, err)
	}
	return d, nil
}

// Viewer is a human who watches the map, as much of them as authorization needs: their id and the
// family they may read. It is the READ-side counterpart to Device, and a SEPARATE type on purpose
// (§7): a device token may only WRITE its own fixes and a viewer token may only READ its family,
// and collapsing the two into one identity is how that separation gets eroded into a single
// privilege by accident. A viewer credential authenticates only the read routes; presented to a
// write route it matches no device and is a 401.
type Viewer struct {
	ID          string
	FamilyID    string
	Email       string
	DisplayName string
}

// CreateViewer enrolls a human account into a family. tokenHash is a SHA-256 digest of the
// viewer's bearer token — never the token itself, for the same reason CreateDevice takes a digest
// (see ErrInvalidTokenHash): a database that is read must not hand the reader a live credential.
func CreateViewer(ctx context.Context, q db.Querier, familyID, email, displayName string, tokenHash []byte) (string, error) {
	if len(tokenHash) != TokenHashLen {
		return "", fmt.Errorf("%w: got %d bytes, want a %d-byte SHA-256 digest", ErrInvalidTokenHash, len(tokenHash), TokenHashLen)
	}
	var id string
	err := q.QueryRow(ctx,
		`INSERT INTO viewers (family_id, email, display_name, token_hash) VALUES ($1, $2, $3, $4) RETURNING id`,
		familyID, email, displayName, tokenHash).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create viewer %q: %w", email, err)
	}
	return id, nil
}

// AuthenticateViewer looks up the viewer whose token_hash is the given digest. It is the read-side
// mirror of AuthenticateDevice — same indexed equality on a 256-bit digest, same length guard, same
// deliberately-uniform ErrUnknownToken so "wrong token" cannot be told from "no such account" — and
// it queries the viewers table, so a DEVICE token presented to a read route matches nothing here and
// is refused. That is §7's read/write separation, enforced by which table the credential lives in.
func AuthenticateViewer(ctx context.Context, q db.Querier, tokenHash []byte) (Viewer, error) {
	if len(tokenHash) != TokenHashLen {
		return Viewer{}, fmt.Errorf("%w: got %d bytes, want a %d-byte SHA-256 digest", ErrInvalidTokenHash, len(tokenHash), TokenHashLen)
	}
	var v Viewer
	err := q.QueryRow(ctx,
		// Same expiry enforcement as AuthenticateDevice: the (expires_at IS NULL OR expires_at >
		// now()) test is in the WHERE, so an expired viewer token matches nothing and is the same
		// uniform ErrUnknownToken as an unknown one, decided by the database's clock.
		`SELECT id, family_id, email, display_name FROM viewers
		 WHERE token_hash = $1 AND (expires_at IS NULL OR expires_at > now())`, tokenHash).
		Scan(&v.ID, &v.FamilyID, &v.Email, &v.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Viewer{}, ErrUnknownToken
	}
	if err != nil {
		return Viewer{}, fmt.Errorf("authenticate viewer: %w", err)
	}
	return v, nil
}

// RotateViewerToken is the read-side mirror of RotateDeviceToken: a new token_hash and expiry for a
// viewer, written in one statement so the old token dies as the new one is born. expiresAt is nil for
// no expiry or a future instant for a short-lived token; it returns false (found) for an unknown id.
func RotateViewerToken(ctx context.Context, q db.Querier, viewerID string, newHash []byte, expiresAt *time.Time) (bool, error) {
	if len(newHash) != TokenHashLen {
		return false, fmt.Errorf("%w: got %d bytes, want a %d-byte SHA-256 digest", ErrInvalidTokenHash, len(newHash), TokenHashLen)
	}
	tag, err := q.Exec(ctx,
		`UPDATE viewers SET token_hash = $2, expires_at = $3 WHERE id = $1`, viewerID, newHash, expiresAt)
	if err != nil {
		return false, fmt.Errorf("rotate viewer token: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ErrInvalidGeofence is returned when a ring does not describe a usable Place.
//
// The failure it prevents is silent. A ring that crosses itself (a "bowtie") is not rejected by
// PostGIS: it parses, raises a NOTICE nobody reads, and produces a polygon of ZERO AREA that
// ST_Covers reports as containing nothing — forever. The row lands, the Place looks real in
// every listing, and the arrival alert simply never fires. Nothing errors, so nothing is ever
// investigated.
//
// So an invalid ring is a typed error here, and a CHECK constraint on the table catches any
// other writer that tries the same thing (see 00002_core_schema.sql).
var ErrInvalidGeofence = errors.New("invalid geofence area")

// CreateGeofence stores a Place as a geography(Polygon,4326).
//
// The ring is closed for you if it is not already closed (OGC requires a polygon's ring to
// return to its first point). That is not a guess about where the caller meant the boundary to
// be — it is the only closure that exists.
//
// The ring's VALIDITY is checked before anything is stored — see ErrInvalidGeofence.
//
// Ring ORIENTATION is deliberately not fussed over: PostGIS's geography type interprets a ring
// as the SMALLER of the two areas it divides the globe into, so clockwise and anticlockwise
// give the same Place at family scale. (A Place larger than a hemisphere would be ambiguous.
// Nobody's school is.)
func CreateGeofence(ctx context.Context, q db.Querier, familyID, name string, ring []Point) (string, error) {
	wkt, err := polygonWKT(ring)
	if err != nil {
		return "", fmt.Errorf("create geofence %q: %w", name, err)
	}

	// Ask PostGIS whether this is a real polygon BEFORE storing it, so the caller gets a
	// sentence naming the problem rather than a constraint violation — and so a bowtie can
	// never become a Place that quietly covers nothing.
	var valid bool
	var reason string
	err = q.QueryRow(ctx,
		`SELECT ST_IsValid(g::geometry), ST_IsValidReason(g::geometry)
		 FROM (SELECT ST_GeogFromText($1) AS g) s`, wkt).Scan(&valid, &reason)
	if err != nil {
		return "", fmt.Errorf("create geofence %q: check the area is a valid polygon: %w", name, err)
	}
	if !valid {
		// ST_IsValidReason names the actual cause. Do not paraphrase it: a self-intersection is
		// the common case but not the only one — a collinear ring and a zero-width spike are
		// also invalid and do not cross themselves. What every case shares is the consequence,
		// and that is what is worth spelling out.
		return "", fmt.Errorf("%w: %s — such a ring encloses no area, so the Place would never "+
			"contain anybody", ErrInvalidGeofence, reason)
	}

	var id string
	err = q.QueryRow(ctx,
		// ST_GeogFromText parses WKT straight into geography at 4326 — no geometry stage in
		// between to lose the SRID at.
		`INSERT INTO geofences (family_id, name, area) VALUES ($1, $2, ST_GeogFromText($3)) RETURNING id`,
		familyID, name, wkt).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create geofence %q: %w", name, err)
	}
	return id, nil
}

// polygonWKT renders a validated ring as WKT.
//
// Every coordinate is range-checked first, for exactly the reason InsertFix checks its own:
// PostGIS would COERCE an out-of-range vertex into range rather than reject it, silently
// reshaping the Place (see ErrCoordinateOutOfRange). The formatting is Go's own float
// rendering of already-validated numbers, so there is no user text reaching the SQL.
func polygonWKT(ring []Point) (string, error) {
	for i, p := range ring {
		if err := ValidateLonLat(p.Lon, p.Lat); err != nil {
			return "", fmt.Errorf("point %d: %w", i, err)
		}
	}

	// Count DISTINCT corners, not points. A ring of {A, B, A} has three entries but only two
	// corners, and it encloses nothing — a bare len() >= 3 would wave it through to PostGIS,
	// which rejects it with a parse error from three layers down instead of the typed error the
	// fail-safe stance promises the caller.
	open := ring
	if n := len(open); n > 1 && open[0] == open[n-1] {
		open = open[:n-1] // drop an explicit closing point before counting
	}
	corners := make(map[Point]struct{}, len(open))
	for _, p := range open {
		corners[p] = struct{}{}
	}
	if len(corners) < 3 {
		return "", fmt.Errorf("%w: a polygon needs at least 3 distinct corners, got %d",
			ErrInvalidGeofence, len(corners))
	}

	closed := ring
	if first, last := ring[0], ring[len(ring)-1]; first != last {
		closed = append(append(make([]Point, 0, len(ring)+1), ring...), first)
	}

	coords := make([]string, len(closed))
	for i, p := range closed {
		// Longitude first, as in WKT, as in ST_Point, as everywhere else.
		coords[i] = f(p.Lon) + " " + f(p.Lat)
	}
	return "SRID=4326;POLYGON((" + strings.Join(coords, ", ") + "))", nil
}

func f(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
