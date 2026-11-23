package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ap-andersson/airband-hub/internal/airband"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestBookmarksRoundTrip(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 16, 12, 30, 500e6, time.UTC)
	b, err := s.AddBookmark(ctx, Bookmark{Name: "Go-around", Path: "essl_20261118_16.mp3", Offset: 750.5, At: at, Freq: "118.805"})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == 0 || b.Created.IsZero() {
		t.Fatalf("id or created not set: %+v", b)
	}
	if _, err := s.AddBookmark(ctx, Bookmark{Name: "earlier", Path: "essl_20261118_15.mp3", At: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Bookmark(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != b {
		t.Fatalf("got %+v, want %+v", got, b)
	}

	list, err := s.Bookmarks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b.ID {
		t.Fatalf("list not newest moment first: %+v", list)
	}

	if err := s.RenameBookmark(ctx, b.ID, "Missed approach"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Bookmark(ctx, b.ID); got.Name != "Missed approach" {
		t.Fatalf("rename not stored: %q", got.Name)
	}

	if err := s.DeleteBookmark(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bookmark(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteBookmark(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDataSurvivesReopen(t *testing.T) {
	s, path := open(t)
	if _, err := s.AddBookmark(context.Background(), Bookmark{Name: "x", Path: "a.mp3", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path) // migrations must not run again
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, err := s2.Bookmarks(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("after reopen: %v %+v", err, list)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	s, path := open(t)
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s2, err := Open(path); err == nil {
		s2.Close()
		t.Fatal("opened a database from a newer hub")
	}
}

func TestIDsAreNotReused(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := s.AddBookmark(ctx, Bookmark{Name: "a", Path: "a.mp3", At: time.Now()})
	if err := s.DeleteBookmark(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := s.AddBookmark(ctx, Bookmark{Name: "b", Path: "a.mp3", At: time.Now()})
	if b.ID == a.ID {
		t.Fatalf("id %d reused; a shared link to the deleted bookmark would open another", a.ID)
	}
}

func TestPragmasApply(t *testing.T) {
	s, _ := open(t)
	var mode string
	var timeout, fk int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if mode != "wal" || timeout != 5000 || fk != 1 {
		t.Fatalf("journal_mode %q, busy_timeout %d, foreign_keys %d", mode, timeout, fk)
	}
}

func TestTransmissionsRoundTripAndPrune(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tx := func(end time.Time, freq string) airband.Transmission {
		return airband.Transmission{Start: end.Add(-2 * time.Second), End: end, Freq: freq, Label: "TWR " + freq, DurationSeconds: 2, SNRMinDB: 10, SNRMaxDB: 15}
	}
	old, recent := tx(t0.Add(-40*time.Hour), "118.805"), tx(t0, "129.800")
	recent.Approximate = true
	if err := s.AddTransmissions(ctx, []airband.Transmission{old, recent}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Transmissions(ctx, t0.Add(-36*time.Hour))
	if err != nil || len(got) != 1 || got[0] != recent {
		t.Fatalf("got %+v, %v; want only %+v", got, err, recent)
	}
	n, err := s.PruneTransmissions(ctx, t0.Add(-36*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if all, _ := s.Transmissions(ctx, time.Time{}); len(all) != 1 {
		t.Fatalf("left %+v", all)
	}
}

func TestUptimeAddsUpPerHourAndCaps(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	h := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, sec := range []float64{60, 60, 30} {
		if err := s.AddUptime(ctx, h, sec); err != nil {
			t.Fatal(err)
		}
	}
	s.AddUptime(ctx, h.Add(time.Hour), 5000) // more than an hour is capped
	up, err := s.Uptime(ctx, h, h.Add(2*time.Hour))
	if err != nil || up[h] != 150 || up[h.Add(time.Hour)] != 3600 {
		t.Fatalf("uptime %v %v", up, err)
	}
	if err := s.PruneUptime(ctx, h.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if up, _ := s.Uptime(ctx, h, h.Add(2*time.Hour)); len(up) != 1 {
		t.Fatalf("after prune %v", up)
	}
}

func TestTransmissionsBetween(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var txs []airband.Transmission
	for i := range 5 {
		start := t0.Add(time.Duration(i) * time.Hour)
		txs = append(txs, airband.Transmission{Start: start, End: start.Add(2 * time.Second), Freq: "118.805", DurationSeconds: 2})
	}
	s.AddTransmissions(ctx, txs)
	got, err := s.TransmissionsBetween(ctx, t0.Add(time.Hour), t0.Add(3*time.Hour))
	if err != nil || len(got) != 2 || !got[0].Start.Equal(t0.Add(time.Hour)) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestHistoryStart(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if tx, up, err := s.HistoryStart(ctx); err != nil || !tx.IsZero() || !up.IsZero() {
		t.Fatalf("empty: %v %v %v", tx, up, err)
	}
	t0 := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	s.AddTransmissions(ctx, []airband.Transmission{{Start: t0.Add(time.Hour), End: t0.Add(time.Hour), Freq: "1"}, {Start: t0, End: t0, Freq: "1"}})
	s.AddUptime(ctx, t0.Add(30*time.Hour), 10)
	tx, up, err := s.HistoryStart(ctx)
	if err != nil || !tx.Equal(t0) || !up.Equal(t0.Add(30*time.Hour)) {
		t.Fatalf("got %v %v %v", tx, up, err)
	}
}

func TestFrequenciesTakeTheLatestLabel(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	s.AddTransmissions(ctx, []airband.Transmission{
		{Start: t0, End: t0.Add(time.Second), Freq: "129.800", Label: "OLD NAME"},
		{Start: t0.Add(time.Hour), End: t0.Add(time.Hour + time.Second), Freq: "129.800", Label: "MALMEN TOWER"},
		{Start: t0.Add(30 * time.Minute), End: t0.Add(30*time.Minute + time.Second), Freq: "129.800", Label: "MIDDLE NAME"},
		{Start: t0, End: t0.Add(time.Second), Freq: "118.805", Label: "SAAB TOWER"},
	})
	got, err := s.Frequencies(ctx)
	if err != nil || len(got) != 2 || got[0] != (Frequency{"118.805", "SAAB TOWER"}) || got[1] != (Frequency{"129.800", "MALMEN TOWER"}) {
		t.Fatalf("got %+v %v", got, err)
	}
}
