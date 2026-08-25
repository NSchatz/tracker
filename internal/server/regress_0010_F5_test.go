// Refuter artifact for S0010-tracker-server-1, impl gate ordinal 2, finding F5.
//
// AC15: "WHEN a fresh stream connection is opened (no resume cursor), THE SYSTEM SHALL deliver an
// initial snapshot containing every device in the family, each carrying the presentation value
// computed at delivery time: one `position` event per device holding a fix, one `presentation`
// event carrying the unlocated entry per device holding none".
//
// AC16: "the existing resumable-cursor guarantee SHALL be unchanged: a client reconnecting with the
// last cursor it saw still receives every position that arrived strictly after it, with no gap".
//
// streamConn.poll advances its cursor INSIDE the emit loop and re-tests every later row against the
// advanced value:
//
//	if s.Current == nil || !s.Current.ReceivedAt.After(c.since) { continue }
//	... writeEvent(...)
//	c.since = s.Current.ReceivedAt
//
// So when two devices' CURRENT positions share one `received_at` - `received_at` defaults to
// Postgres now(), the TRANSACTION timestamp, at microsecond resolution - the first advances the
// cursor onto that instant and the second is dropped: not a `position` event (the After test now
// fails) and not a `presentation` event either (snapshotUnlocated speaks only for devices that
// evaluate to no-position). The device is simply absent from the snapshot, and stays absent on this
// connection for as long as it does not report again.
//
// The pre-S0010 stream did not have this hole: it filtered `received_at > since` ONCE, in SQL
// (store.PositionsSince at origin/main), against the cursor as it stood when the poll began, so a
// tie emitted both rows.
//
// This test documents the defect; it is not the fix. It needs no database: it drives the real
// streamConn.poll against a fake Querier returning the two colliding rows.
package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDeviceRow is one row of familyDeviceStatesSQL: id, name, then the current position's ts / lon
// / lat / received_at and the aggregate last_contact.
type fakeDeviceRow struct {
	id, name    string
	ts          time.Time
	lon, lat    float64
	receivedAt  time.Time
	lastContact time.Time
}

type fakeRows struct {
	rows []fakeDeviceRow
	i    int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }

func (r *fakeRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

// Scan fills the seven destinations FamilyDeviceStates passes, positionally.
func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	ts, receivedAt, lastContact := row.ts, row.receivedAt, row.lastContact
	lon, lat := row.lon, row.lat
	vals := []any{row.id, row.name, &ts, &lon, &lat, &receivedAt, &lastContact}
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = vals[i].(string)
		case **time.Time:
			*d = vals[i].(*time.Time)
		case **float64:
			*d = vals[i].(*float64)
		}
	}
	return nil
}

// fakeDB answers every query with the same two rows and satisfies the server's DB interface.
type fakeDB struct{ rows []fakeDeviceRow }

func (f fakeDB) Ping(context.Context) error { return nil }
func (f fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (f fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (f fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &fakeRows{rows: f.rows}, nil
}

// sseSink is a ResponseWriter that is also a Flusher, capturing what the stream wrote.
type sseSink struct {
	b      strings.Builder
	header http.Header
}

func (s *sseSink) Header() http.Header {
	if s.header == nil {
		s.header = http.Header{}
	}
	return s.header
}
func (s *sseSink) Write(p []byte) (int, error) { return s.b.Write(p) }
func (s *sseSink) WriteHeader(int)             {}
func (s *sseSink) Flush()                      {}

// TestRegress0010F5SnapshotDropsADeviceOnAReceivedAtTie opens the equivalent of a fresh stream
// connection over a family whose two devices' current positions arrived at the SAME microsecond,
// and asserts the snapshot describes BOTH of them.
func TestRegress0010F5SnapshotDropsADeviceOnAReceivedAtTie(t *testing.T) {
	// One instant, shared by both rows: `received_at` defaults to now(), the transaction timestamp,
	// so two ingest transactions that begin in the same microsecond store the same value. It is a
	// moment ago, so BOTH devices are plainly `live` - the one dropped here is a phone that has just
	// reported, not an aged-out one.
	shared := time.Now().UTC().Truncate(time.Microsecond)

	db := fakeDB{rows: []fakeDeviceRow{
		{id: "device-a", name: "a-phone", ts: shared, lon: 12.4964, lat: 41.9028, receivedAt: shared, lastContact: shared},
		{id: "device-b", name: "b-phone", ts: shared, lon: 9.19, lat: 45.4642, receivedAt: shared, lastContact: shared},
	}}

	sink := &sseSink{}
	conn := &streamConn{
		database: db,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		familyID: "family-1",
		windows:  presentation.Windows{LiveSeconds: 120, StaleSeconds: 900},
		w:        sink,
		flusher:  sink,
	}

	// A FRESH connection: no resume cursor, so `since` is the zero time and every device holding a
	// fix is owed one `position` event (AC15).
	if err := conn.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	// A second poll, so the omission is shown to be PERMANENT rather than deferred: the cursor now
	// sits on the shared instant, so device-b's position never passes the strictly-after test again,
	// and diff owes it nothing because its presentation value has not changed.
	if err := conn.poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	body := sink.b.String()
	for _, id := range []string{"device-a", "device-b"} {
		if !strings.Contains(body, id) {
			t.Errorf("the stream never described %s. AC15 requires an initial snapshot containing "+
				"EVERY device in the family - one position event per device holding a fix - and AC16 "+
				"keeps the inherited cursor guarantee gap-free. poll advances c.since inside the emit "+
				"loop and re-tests later rows against it, so a device whose current position shares a "+
				"received_at microsecond with an earlier one is dropped from the stream entirely.\n"+
				"wrote:\n%s", id, body)
		}
	}
}
