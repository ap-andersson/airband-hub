// Package activity serves the stored transmission history, summed per frequency, day and hour
// in the viewer's time zone, for the Activity page.
package activity

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the viewer's zone arrives as an IANA name; distroless has no zoneinfo

	"github.com/ap-andersson/airband-hub/internal/airband"
	"github.com/ap-andersson/airband-hub/internal/store"
)

// MaxDays is how far back the history goes, and so the longest range the page can ask for.
const MaxDays = 90

type Handlers struct {
	Store *store.Store
	Log   *slog.Logger
}

// Register adds:
//
//	GET /api/activity?days=30&tz=Europe/Stockholm               sums per frequency, day and hour
//	GET /api/activity/transmissions?date=2026-11-20&tz=...      one day's transmissions, newest first
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/activity", h.activity)
	mux.HandleFunc("GET /api/activity/transmissions", h.transmissions)
}

// Hours holds one value per hour of the day.
type Hours [24]float64

// Activity is the history over Days, oldest first. Hours are the viewer's local hours.
type Activity struct {
	Days        []string          `json:"days"` // YYYY-MM-DD
	Frequencies []store.Frequency `json:"frequencies"`
	// per frequency, then per day: transmissions and seconds on air in each hour
	Counts  map[string][]Hours `json:"counts"`
	Airtime map[string][]Hours `json:"airtime"`
	// per day: seconds in each hour when the hub had working metrics from rtl_airband
	Uptime []Hours `json:"uptime"`
	// HistorySince is when the stored transmissions begin: earlier hours have no data at all.
	// UptimeSince is when uptime began to be recorded: hours between the two count as up.
	// Either is omitted when there is nothing yet.
	HistorySince *time.Time `json:"history_since,omitempty"`
	UptimeSince  *time.Time `json:"uptime_since,omitempty"`
}

func location(r *http.Request) (*time.Location, bool) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		return time.UTC, true
	}
	loc, err := time.LoadLocation(tz)
	return loc, err == nil
}

func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// Summarise sums txs and uptime into the days starting at first (local midnight in loc).
func Summarise(first time.Time, days int, loc *time.Location, freqs []store.Frequency, txs []airband.Transmission, uptime map[time.Time]float64) Activity {
	a := Activity{Frequencies: freqs, Counts: map[string][]Hours{}, Airtime: map[string][]Hours{}, Uptime: make([]Hours, days)}
	index := map[string]int{}
	for i := range days {
		d := first.AddDate(0, 0, i).Format(time.DateOnly)
		a.Days = append(a.Days, d)
		index[d] = i
	}
	for _, f := range freqs {
		a.Counts[f.Freq] = make([]Hours, days)
		a.Airtime[f.Freq] = make([]Hours, days)
	}
	slot := func(t time.Time) (int, int, bool) {
		lt := t.In(loc)
		day, ok := index[lt.Format(time.DateOnly)]
		return day, lt.Hour(), ok
	}
	for _, t := range txs {
		day, hour, ok := slot(t.Start)
		if _, known := a.Counts[t.Freq]; !ok || !known {
			continue
		}
		a.Counts[t.Freq][day][hour]++
		a.Airtime[t.Freq][day][hour] += t.DurationSeconds
	}
	for start, sec := range uptime {
		if day, hour, ok := slot(start); ok {
			// a local hour that occurs twice (the end of summer time) still has at most 3600 s
			a.Uptime[day][hour] = min(3600, a.Uptime[day][hour]+sec)
		}
	}
	return a
}

func (h *Handlers) activity(w http.ResponseWriter, r *http.Request) {
	loc, ok := location(r)
	if !ok {
		http.Error(w, "unknown time zone", http.StatusBadRequest)
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 || days > MaxDays {
		http.Error(w, "days must be 1 to "+strconv.Itoa(MaxDays), http.StatusBadRequest)
		return
	}
	first := midnight(time.Now(), loc).AddDate(0, 0, 1-days)
	end := midnight(time.Now(), loc).AddDate(0, 0, 1)
	freqs, err := h.Store.Frequencies(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	txs, err := h.Store.TransmissionsBetween(r.Context(), first, end)
	if err != nil {
		h.fail(w, err)
		return
	}
	// whole UTC hours that overlap the range; zones with half-hour offsets get the overlapping hour
	uptime, err := h.Store.Uptime(r.Context(), first.Truncate(time.Hour).Add(-time.Hour), end)
	if err != nil {
		h.fail(w, err)
		return
	}
	a := Summarise(first, days, loc, freqs, txs, uptime)
	txSince, upSince, err := h.Store.HistoryStart(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	if !txSince.IsZero() {
		a.HistorySince = &txSince
	}
	if !upSince.IsZero() {
		a.UptimeSince = &upSince
	}
	writeJSON(w, r, a)
}

func (h *Handlers) transmissions(w http.ResponseWriter, r *http.Request) {
	loc, ok := location(r)
	if !ok {
		http.Error(w, "unknown time zone", http.StatusBadRequest)
		return
	}
	day, err := time.ParseInLocation(time.DateOnly, r.URL.Query().Get("date"), loc)
	if err != nil {
		http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	txs, err := h.Store.TransmissionsBetween(r.Context(), day, day.AddDate(0, 0, 1))
	if err != nil {
		h.fail(w, err)
		return
	}
	sort.SliceStable(txs, func(i, j int) bool { return txs[i].Start.After(txs[j].Start) })
	writeJSON(w, r, txs)
}

func (h *Handlers) fail(w http.ResponseWriter, err error) {
	h.Log.Error("activity", "err", err)
	http.Error(w, "cannot read the transmission history", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Add("Vary", "Accept-Encoding")
	var out io.Writer = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		out = zw
	}
	json.NewEncoder(out).Encode(v)
}
