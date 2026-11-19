package recordings

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // distroless has no zoneinfo; the browser's zone arrives as an IANA name

	"github.com/ap-andersson/airband-hub/internal/mp3"
	"github.com/ap-andersson/airband-hub/internal/store"
)

type Handlers struct {
	Library  *Library
	Analyzer *Analyzer
	// Store keeps bookmarks; nil when there is no data directory, and then there are none.
	Store *store.Store
	Log   *slog.Logger
}

// Register adds:
//
//	GET /api/recordings/days?tz=Europe/Stockholm   days that have recordings, newest first
//	GET /api/recordings?date=2026-11-18&tz=...      the recordings that start on that day
//	GET /api/recordings/waveform?path=...           peaks and transmission markers
//	GET /api/recordings/audio?path=...&from=12.5    the audio from that time, cut at an exact frame
//	GET /api/recordings/download?path=...           the whole file
//
// and, with a Store, the bookmark endpoints (see registerBookmarks).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/recordings/days", h.days)
	mux.HandleFunc("GET /api/recordings", h.list)
	mux.HandleFunc("GET /api/recordings/waveform", h.waveform)
	mux.HandleFunc("GET /api/recordings/audio", h.audio)
	mux.HandleFunc("GET /api/recordings/download", h.download)
	if h.Store != nil {
		h.registerBookmarks(mux)
	}
}

func location(r *http.Request) (*time.Location, error) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(tz)
}

type day struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

func (h *Handlers) days(w http.ResponseWriter, r *http.Request) {
	loc, err := location(r)
	if err != nil {
		http.Error(w, "unknown time zone", http.StatusBadRequest)
		return
	}
	list, err := h.Library.List()
	if err != nil {
		h.fail(w, err)
		return
	}
	days := []day{}
	for i := len(list) - 1; i >= 0; i-- {
		d := list[i].Start.In(loc).Format(time.DateOnly)
		if n := len(days); n > 0 && days[n-1].Date == d {
			days[n-1].Count++
		} else {
			days = append(days, day{Date: d, Count: 1})
		}
	}
	writeJSON(w, r, days)
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	loc, err := location(r)
	if err != nil {
		http.Error(w, "unknown time zone", http.StatusBadRequest)
		return
	}
	date, err := time.ParseInLocation(time.DateOnly, r.URL.Query().Get("date"), loc)
	if err != nil {
		http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	end := date.AddDate(0, 0, 1)
	list, err := h.Library.List()
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []Recording{}
	for _, rec := range list {
		if !rec.Start.Before(date) && rec.Start.Before(end) {
			if fresh, err := h.Library.refresh(rec); err == nil {
				rec = fresh
			}
			out = append(out, rec)
		}
	}
	writeJSON(w, r, out)
}

func (h *Handlers) recording(w http.ResponseWriter, r *http.Request) (Recording, bool) {
	return h.recordingAt(w, r.URL.Query().Get("path"))
}

func (h *Handlers) recordingAt(w http.ResponseWriter, p string) (Recording, bool) {
	if p == "" || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return Recording{}, false
	}
	rec, err := h.Library.Get(p)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "no such recording", http.StatusNotFound)
		return Recording{}, false
	}
	if err != nil {
		h.fail(w, err)
		return Recording{}, false
	}
	return rec, true
}

func (h *Handlers) waveform(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.recording(w, r)
	if !ok {
		return
	}
	an, err := h.Analyzer.Get(r.Context(), rec)
	if err != nil {
		if r.Context().Err() == nil {
			h.fail(w, err)
		}
		return
	}
	if !rec.Growing {
		w.Header().Set("Cache-Control", "private, max-age=3600")
	}
	writeJSON(w, r, an)
}

func (h *Handlers) audio(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.recording(w, r)
	if !ok {
		return
	}
	from, err := strconv.ParseFloat(r.URL.Query().Get("from"), 64)
	if err != nil || from < 0 {
		from = 0
	}
	data, err := h.Library.ReadFile(rec)
	if err != nil {
		h.fail(w, err)
		return
	}
	ix := mp3.ScanFrames(data)
	if len(ix.Frames) == 0 {
		http.Error(w, "no audio in this recording", http.StatusUnprocessableEntity)
		return
	}
	offset := int64(0)
	if from > 0 {
		i, start := ix.FrameAt(from)
		offset = ix.Frames[i].Offset
		w.Header().Set("X-Audio-Start", strconv.FormatFloat(start, 'f', 3, 64))
	}
	last := ix.Frames[len(ix.Frames)-1]
	body := data[offset : last.Offset+int64(last.Size)]
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

func (h *Handlers) download(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.recording(w, r)
	if !ok {
		return
	}
	f, err := h.Library.Open(rec)
	if err != nil {
		h.fail(w, err)
		return
	}
	defer f.Close()
	name := strings.TrimSuffix(path.Base(rec.Path), ".tmp")
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, rec.Modified, f)
}

func (h *Handlers) fail(w http.ResponseWriter, err error) {
	h.Log.Error("recordings", "err", err)
	http.Error(w, "cannot read recordings", http.StatusInternalServerError)
}

// writeJSON gzips when the client accepts it; waveforms are mostly silence and shrink a lot.
func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	writeJSONStatus(w, r, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Encoding")
	var out io.Writer = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		out = zw
	}
	w.WriteHeader(status)
	json.NewEncoder(out).Encode(v)
}
