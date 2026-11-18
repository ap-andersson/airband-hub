// Package hub wires the hub's HTTP endpoints together.
package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ap-andersson/airband-hub/internal/icecast"
	"github.com/ap-andersson/airband-hub/internal/relay"
)

type Config struct {
	SourceUser     string
	SourcePassword string
	SourceTimeout  time.Duration
}

// NewHandler returns the hub's HTTP handler:
//
//	GET  /healthz          liveness
//	GET  /api/mounts       JSON status of every mount
//	GET  /admin/metadata   Icecast metadata updates (for rtl_airband)
//	SOURCE|PUT /<mount>    Icecast source push (for rtl_airband)
//	GET|HEAD /<mount>      live stream for players
func NewHandler(cfg Config, reg *relay.Registry, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	(&icecast.Handler{
		Registry:      reg,
		User:          cfg.SourceUser,
		Password:      cfg.SourcePassword,
		SourceTimeout: cfg.SourceTimeout,
		Log:           log,
	}).Register(mux)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/mounts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reg.List())
	})
	return mux
}
