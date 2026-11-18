package icecast

import (
	"encoding/json"
	"net/http"
	"time"
)

// statusSource has the fields of an Icecast 2.4 /status-json.xsl source that players read,
// such as tar1090's airband player (title, server_name, listenurl).
type statusSource struct {
	AudioInfo         string `json:"audio_info,omitempty"`
	Genre             string `json:"genre,omitempty"`
	Listeners         int    `json:"listeners"`
	ListenURL         string `json:"listenurl"`
	ServerDescription string `json:"server_description,omitempty"`
	ServerName        string `json:"server_name,omitempty"`
	ServerType        string `json:"server_type"`
	StreamStart       string `json:"stream_start_iso8601"`
	Title             string `json:"title,omitempty"`
}

// serveStatusJSON answers like Icecast's status-json.xsl, so clients written for Icecast
// keep showing the current title. Like Icecast, it lists only mounts with a source, gives a
// single source as an object rather than a one-element array, and leaves "source" out when
// there is none.
func (h *Handler) serveStatusJSON(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	var sources []statusSource
	for _, m := range h.Registry.List() {
		if !m.SourceConnected {
			continue
		}
		sources = append(sources, statusSource{
			AudioInfo:         m.Info.AudioInfo,
			Genre:             m.Info.Genre,
			Listeners:         m.Listeners,
			ListenURL:         scheme + "://" + r.Host + m.Mount,
			ServerDescription: m.Info.Description,
			ServerName:        m.Info.Name,
			ServerType:        m.Info.ContentType,
			StreamStart:       m.SourceSince.UTC().Format(time.RFC3339),
			Title:             m.Title,
		})
	}
	stats := map[string]any{"server_id": "airband-hub", "host": r.Host}
	switch len(sources) {
	case 0:
	case 1:
		stats["source"] = sources[0]
	default:
		stats["source"] = sources
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]any{"icestats": stats})
}
