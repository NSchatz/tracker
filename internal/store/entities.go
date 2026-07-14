package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/NSchatz/tracker/internal/db"
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

// CreateViewer registers a human who can watch the family's map.
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

// CreateGeofence stores a Place as a geography(Polygon,4326).
//
// The ring is closed for you if it is not already closed (OGC requires a polygon's ring to
// return to its first point). That is not a guess about where the caller meant the boundary to
// be — it is the only closure that exists.
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
	if len(ring) < 3 {
		return "", fmt.Errorf("a polygon needs at least 3 points, got %d", len(ring))
	}
	for i, p := range ring {
		if err := ValidateLonLat(p.Lon, p.Lat); err != nil {
			return "", fmt.Errorf("point %d: %w", i, err)
		}
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
