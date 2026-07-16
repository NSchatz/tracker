package store

import (
	"context"
	"errors"
	"testing"
)

// TestGeofenceCRUD covers the Places CRUD surface (list / get / update / delete; CreateGeofence's own
// validation is proven by TestInvalidGeofenceNeverLands in spatial_test.go). Every operation is
// family-scoped, and the family boundary is the point of half these assertions: a Place in one family
// is invisible and immutable to another.
func TestGeofenceCRUD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)

	famA, _ := seedDevice(ctx, t, pool, "crud-a")
	famB, _ := seedDevice(ctx, t, pool, "crud-b")

	// Two Places in family A, one in B.
	homeA, err := CreateGeofence(ctx, pool, famA, "Home", []Point{
		{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 42}, {Lon: 12, Lat: 42},
	})
	if err != nil {
		t.Fatalf("CreateGeofence Home: %v", err)
	}
	if _, err := CreateGeofence(ctx, pool, famA, "Alpha", []Point{
		{Lon: 0, Lat: 0}, {Lon: 1, Lat: 0}, {Lon: 1, Lat: 1}, {Lon: 0, Lat: 1},
	}); err != nil {
		t.Fatalf("CreateGeofence Alpha: %v", err)
	}
	placeB, err := CreateGeofence(ctx, pool, famB, "OtherFamily", []Point{
		{Lon: 20, Lat: 20}, {Lon: 21, Lat: 20}, {Lon: 21, Lat: 21}, {Lon: 20, Lat: 21},
	})
	if err != nil {
		t.Fatalf("CreateGeofence OtherFamily: %v", err)
	}

	t.Run("list is family-scoped and name-ordered", func(t *testing.T) {
		list, err := ListGeofences(ctx, pool, famA)
		if err != nil {
			t.Fatalf("ListGeofences(A): %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("family A has %d places, want 2 (never B's)", len(list))
		}
		if list[0].Name != "Alpha" || list[1].Name != "Home" {
			t.Fatalf("places not name-ordered: %q, %q", list[0].Name, list[1].Name)
		}
		// The ring comes back as GeoJSON a map can draw.
		if list[1].AreaGeoJSON == "" || list[1].AreaGeoJSON[0] != '{' {
			t.Fatalf("Home area is not GeoJSON: %q", list[1].AreaGeoJSON)
		}
	})

	t.Run("get resolves a place and its family", func(t *testing.T) {
		g, err := GeofenceByID(ctx, pool, homeA)
		if err != nil {
			t.Fatalf("GeofenceByID: %v", err)
		}
		if g.FamilyID != famA || g.Name != "Home" {
			t.Fatalf("got family=%s name=%q, want family=%s Home", g.FamilyID, g.Name, famA)
		}
		if _, err := GeofenceByID(ctx, pool, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrUnknownGeofence) {
			t.Fatalf("GeofenceByID(absent) = %v, want ErrUnknownGeofence", err)
		}
	})

	t.Run("update rewrites name and ring, family-scoped", func(t *testing.T) {
		ok, err := UpdateGeofence(ctx, pool, famA, homeA, "Casa", []Point{
			{Lon: 12, Lat: 41}, {Lon: 12.5, Lat: 41}, {Lon: 12.5, Lat: 41.5}, {Lon: 12, Lat: 41.5},
		})
		if err != nil {
			t.Fatalf("UpdateGeofence: %v", err)
		}
		if !ok {
			t.Fatal("UpdateGeofence reported no row updated")
		}
		g, _ := GeofenceByID(ctx, pool, homeA)
		if g.Name != "Casa" {
			t.Fatalf("name after update = %q, want Casa", g.Name)
		}

		// Family B cannot update A's place: no row matches, ok=false, and A's place is untouched.
		ok, err = UpdateGeofence(ctx, pool, famB, homeA, "Hijacked", []Point{
			{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 42}, {Lon: 12, Lat: 42},
		})
		if err != nil {
			t.Fatalf("cross-family UpdateGeofence errored: %v", err)
		}
		if ok {
			t.Fatal("family B updated family A's place — the update is not family-scoped")
		}
		if g, _ := GeofenceByID(ctx, pool, homeA); g.Name != "Casa" {
			t.Fatalf("A's place name is now %q; a cross-family update reshaped it", g.Name)
		}

		// An invalid ring is refused, and nothing changes.
		if _, err := UpdateGeofence(ctx, pool, famA, homeA, "Casa", []Point{
			{Lon: 12, Lat: 41}, {Lon: 13, Lat: 42}, {Lon: 13, Lat: 41}, {Lon: 12, Lat: 42}, // bowtie
		}); !errors.Is(err, ErrInvalidGeofence) {
			t.Fatalf("UpdateGeofence with a bowtie = %v, want ErrInvalidGeofence", err)
		}
	})

	t.Run("delete is family-scoped", func(t *testing.T) {
		// B cannot delete A's place.
		ok, err := DeleteGeofence(ctx, pool, famB, homeA)
		if err != nil {
			t.Fatalf("cross-family DeleteGeofence errored: %v", err)
		}
		if ok {
			t.Fatal("family B deleted family A's place — delete is not family-scoped")
		}
		if _, err := GeofenceByID(ctx, pool, homeA); err != nil {
			t.Fatalf("A's place vanished after a cross-family delete: %v", err)
		}

		// A deletes its own.
		ok, err = DeleteGeofence(ctx, pool, famA, homeA)
		if err != nil {
			t.Fatalf("DeleteGeofence: %v", err)
		}
		if !ok {
			t.Fatal("DeleteGeofence reported no row deleted")
		}
		if _, err := GeofenceByID(ctx, pool, homeA); !errors.Is(err, ErrUnknownGeofence) {
			t.Fatalf("place still present after delete: %v", err)
		}
		// B's place is entirely unaffected throughout.
		if _, err := GeofenceByID(ctx, pool, placeB); err != nil {
			t.Fatalf("family B's place was collateral damage: %v", err)
		}
	})
}
