// Package hub wires the hub's HTTP endpoints together.
package hub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ap-andersson/airband-hub/internal/airband"
	"github.com/ap-andersson/airband-hub/internal/icecast"
	"github.com/ap-andersson/airband-hub/internal/relay"
)

type Config struct {
	SourceUser     string
	SourcePassword string
	SourceTimeout  time.Duration
	// Tracker holds rtl_airband's live state; nil when no metrics URL is configured.
	Tracker *airband.Tracker
	// LiveInterval is how often /api/live/stream sends an update.
	LiveInterval time.Duration
	// UI serves the web UI for paths that are not a live mount.
	UI http.Handler
}

// Live is what the dashboard shows.
type Live struct {
	Time           time.Time      `json:"time"`
	Mounts         []relay.Status `json:"mounts"`
	MetricsEnabled bool           `json:"metrics_enabled"`
	airband.State
}

// NewHandler returns the hub's HTTP handler:
//
//	GET  /healthz            liveness
//	GET  /api/mounts         JSON status of every mount
//	GET  /api/live           JSON snapshot for the dashboard
//	GET  /api/live/stream    the same, pushed every LiveInterval as server-sent events
//	GET  /admin/metadata     Icecast metadata updates (for rtl_airband)
//	SOURCE|PUT /<mount>      Icecast source push (for rtl_airband)
//	GET|HEAD /<mount>        live stream for players
//	GET  /...                the web UI
func NewHandler(cfg Config, reg *relay.Registry, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	(&icecast.Handler{
		Registry:      reg,
		User:          cfg.SourceUser,
		Password:      cfg.SourcePassword,
		SourceTimeout: cfg.SourceTimeout,
		Log:           log,
		Fallback:      cfg.UI,
	}).Register(mux)

	live := func() Live {
		l := Live{Time: time.Now(), Mounts: reg.List(), MetricsEnabled: cfg.Tracker != nil}
		if cfg.Tracker != nil {
			l.State = cfg.Tracker.State()
		} else {
			l.State = airband.State{Frequencies: []airband.FreqState{}, Recent: []airband.Transmission{}}
		}
		return l
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/mounts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, reg.List())
	})
	mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, live())
	})
	mux.HandleFunc("GET /api/live/stream", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		ticker := time.NewTicker(cfg.LiveInterval)
		defer ticker.Stop()
		for {
			b, err := json.Marshal(live())
			if err != nil {
				return
			}
			rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(v)
}
