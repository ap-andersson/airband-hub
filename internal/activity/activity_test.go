package activity

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ap-andersson/airband-hub/internal/airband"
	"github.com/ap-andersson/airband-hub/internal/store"
)

func tx(start time.Time, freq string, dur float64) airband.Transmission {
	return airband.Transmission{Start: start, End: start.Add(time.Duration(dur * float64(time.Second))), Freq: freq, Label: "L " + freq, DurationSeconds: dur}
}

func TestSummariseUsesTheViewersDaysAndHours(t *testing.T) {
	stockholm, _ := time.LoadLocation("Europe/Stockholm")
	first := time.Date(2026, 11, 19, 0, 0, 0, 0, stockholm)
	freqs := []store.Frequency{{Freq: "118.805"}, {Freq: "129.800"}}
	txs := []airband.Transmission{
		tx(time.Date(2026, 11, 18, 23, 30, 0, 0, time.UTC), "118.805", 2), // 00:30 on the 19th in Stockholm
		tx(time.Date(2026, 11, 19, 23, 10, 0, 0, time.UTC), "118.805", 3), // 00:10 on the 20th
		tx(time.Date(2026, 11, 19, 23, 20, 0, 0, time.UTC), "118.805", 4),
		tx(time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), "129.800", 5),
		tx(time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), "999.999", 5), // not a known frequency
		tx(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), "129.800", 5), // outside the range
	}
	uptime := map[time.Time]float64{
		time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC): 3600,
		time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC): 1800,
	}
	a := Summarise(first, 2, stockholm, freqs, txs, uptime)
	if len(a.Days) != 2 || a.Days[0] != "2026-11-19" || a.Days[1] != "2026-11-20" {
		t.Fatalf("days %v", a.Days)
	}
	if a.Counts["118.805"][0][0] != 1 || a.Counts["118.805"][1][0] != 2 || a.Airtime["118.805"][1][0] != 7 {
		t.Fatalf("118.805 %v %v", a.Counts["118.805"], a.Airtime["118.805"])
	}
	if a.Counts["129.800"][0][13] != 1 {
		t.Fatalf("129.800 %v", a.Counts["129.800"][0])
	}
	if _, ok := a.Counts["999.999"]; ok {
		t.Fatal("unknown frequency counted")
	}
	if a.Uptime[0][0] != 3600 || a.Uptime[0][13] != 1800 {
		t.Fatalf("uptime %v", a.Uptime[0])
	}
}

func TestEndpoints(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	db.AddTransmissions(ctx, []airband.Transmission{tx(now.Add(-time.Minute), "118.805", 2), tx(now.Add(-2*time.Minute), "135.855", 3), tx(now.Add(-48*time.Hour), "118.805", 1)})
	h := &Handlers{Store: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var a Activity
	get(t, srv.URL+"/api/activity?days=7&tz=UTC", &a)
	if len(a.Days) != 7 || len(a.Frequencies) != 2 || a.Frequencies[0].Freq != "118.805" || len(a.Counts["118.805"]) != 7 {
		t.Fatalf("activity %+v", a)
	}
	total := 0.0
	for _, d := range a.Counts["118.805"] {
		for _, n := range d {
			total += n
		}
	}
	if total != 2 {
		t.Fatalf("118.805 total %v", total)
	}

	var day []airband.Transmission
	get(t, srv.URL+"/api/activity/transmissions?date="+now.Format(time.DateOnly)+"&tz=UTC", &day)
	if len(day) != 2 || day[0].Freq != "118.805" {
		t.Fatalf("day %+v", day)
	}
	var month SNR
	get(t, srv.URL+"/api/activity/snr?view=month&tz=UTC", &month)
	if len(month.Slots) != 30 || len(month.Median["118.805"]) != 30 {
		t.Fatalf("month %+v", month)
	}
	var hours SNR
	get(t, srv.URL+"/api/activity/snr?view=day&date="+now.Format(time.DateOnly)+"&tz=UTC", &hours)
	if len(hours.Slots) != 24 || hours.Slots[0] != "00" || len(hours.Count["135.855"]) != 24 {
		t.Fatalf("day %+v", hours)
	}
	for _, bad := range []string{"/api/activity?days=0", "/api/activity?days=91", "/api/activity?days=7&tz=Mars/Base", "/api/activity/transmissions?date=yesterday", "/api/activity/snr?view=week", "/api/activity/snr?view=day&date=soon"} {
		if resp, _ := http.Get(srv.URL + bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}

func get(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%s: %v %v", url, resp, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestMedianSNR(t *testing.T) {
	freqs := []store.Frequency{{Freq: "118.805"}, {Freq: "129.800"}}
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	snrTx := func(at time.Time, freq string, lo, hi float64) airband.Transmission {
		return airband.Transmission{Start: at, End: at.Add(time.Second), Freq: freq, SNRMinDB: lo, SNRMaxDB: hi}
	}
	txs := []airband.Transmission{
		snrTx(t0, "118.805", 30, 33),                    // 31.5
		snrTx(t0.Add(time.Minute), "118.805", 42, 45),   // 43.5
		snrTx(t0.Add(2*time.Minute), "118.805", 36, 39), // 37.5
		snrTx(t0.Add(3*time.Minute), "118.805", 45, 48), // 46.5: median of four is (37.5+43.5)/2
		snrTx(t0.Add(4*time.Minute), "118.805", 0, 0),   // unknown, skipped
		{Start: t0, Freq: "118.805", Approximate: true}, // unknown, skipped
		snrTx(t0, "129.800", 30, 33),                    // only two: too few for a median
		snrTx(t0.Add(time.Minute), "129.800", 30, 33),
		snrTx(t0.Add(time.Hour), "118.805", 30, 33), // the next slot
	}
	got := MedianSNR([]string{"10", "11"}, freqs, txs, func(at time.Time) int { return at.Hour() - 10 })
	if m := got.Median["118.805"][0]; m == nil || *m != 40.5 || got.Count["118.805"][0] != 4 {
		t.Fatalf("118.805 hour 10: %v %v", m, got.Count["118.805"])
	}
	if got.Median["118.805"][1] != nil || got.Count["118.805"][1] != 1 {
		t.Fatal("one transmission should leave a gap")
	}
	if got.Median["129.800"][0] != nil || got.Count["129.800"][0] != 2 {
		t.Fatal("two transmissions should leave a gap")
	}
}
