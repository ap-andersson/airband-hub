// Package icecast implements the parts of the Icecast 2 protocol rtl_airband uses:
// the source side (SOURCE/PUT with Basic auth, then a raw MP3 body) and metadata
// updates (/admin/metadata?mode=updinfo), plus plain HTTP listeners.
//
// The wire format was captured from rtl_airband with libshout 2.4.6; see PLAN.md, Findings.
package icecast

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ap-andersson/airband-hub/internal/relay"
)

type Handler struct {
	Registry *relay.Registry
	User     string
	Password string
	// SourceTimeout drops a source that sends nothing for this long (Icecast's source-timeout).
	SourceTimeout time.Duration
	Log           *slog.Logger
	// Fallback serves GET/HEAD requests for paths that are not a live mount (the web UI).
	// Without it they get 404.
	Fallback http.Handler
}

// reservedPrefixes are paths the hub serves itself, so they can never be mounts.
var reservedPrefixes = []string{"/api/", "/admin/", "/healthz"}

// Register adds the metadata endpoint and a catch-all for sources and listeners.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/metadata", h.serveMetadata)
	mux.HandleFunc("/", h.serveMount)
}

func validMount(path string) bool {
	if path == "/" || !strings.HasPrefix(path, "/") {
		return false
	}
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}

func (h *Handler) authorized(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(u), []byte(h.User)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(p), []byte(h.Password)) == 1
	return userOK && passOK
}

func challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Icecast2 Server"`)
	http.Error(w, "You need to authenticate", http.StatusUnauthorized)
}

func (h *Handler) serveMount(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "SOURCE", http.MethodPut:
		h.serveSource(w, r)
	case http.MethodGet, http.MethodHead:
		h.serveListener(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, SOURCE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) serveSource(w http.ResponseWriter, r *http.Request) {
	mount := r.URL.Path
	if !validMount(mount) {
		http.Error(w, "invalid mountpoint", http.StatusBadRequest)
		return
	}
	if !h.authorized(r) {
		challenge(w)
		return
	}
	info := relay.StreamInfo{
		Name:        r.Header.Get("ice-name"),
		Genre:       r.Header.Get("ice-genre"),
		Description: r.Header.Get("ice-description"),
		ContentType: r.Header.Get("Content-Type"),
		AudioInfo:   r.Header.Get("ice-audio-info"),
	}
	if info.ContentType == "" {
		info.ContentType = "audio/mpeg"
	}

	// The stream is an HTTP/1.0 request body with no length, which net/http would read as
	// empty, so take over the connection and read the raw bytes after the head ourselves.
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		h.Log.Error("cannot take over source connection", "mount", mount, "err", err)
		return
	}
	defer conn.Close()

	m, err := h.Registry.AttachSource(mount, info, conn)
	if errors.Is(err, relay.ErrMountInUse) {
		fmt.Fprint(rw, "HTTP/1.0 403 Forbidden\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nMountpoint in use\n")
		rw.Flush()
		return
	}
	defer m.DetachSource()

	conn.SetDeadline(time.Time{})
	if _, err := rw.WriteString("HTTP/1.0 200 OK\r\nServer: airband-hub\r\n\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	h.Log.Info("source connected", "mount", mount, "remote", r.RemoteAddr, "name", info.Name)

	buf := make([]byte, 16*1024)
	for {
		conn.SetReadDeadline(time.Now().Add(h.SourceTimeout))
		n, err := rw.Read(buf)
		if n > 0 {
			m.Write(buf[:n])
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				h.Log.Warn("source timed out", "mount", mount, "after", h.SourceTimeout)
			} else {
				h.Log.Info("source disconnected", "mount", mount, "err", err)
			}
			return
		}
	}
}

func (h *Handler) serveListener(w http.ResponseWriter, r *http.Request) {
	m := h.Registry.Get(r.URL.Path)
	if m == nil {
		if h.Fallback != nil {
			h.Fallback.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	info := m.Info()
	hdr := w.Header()
	hdr.Set("Content-Type", info.ContentType)
	hdr.Set("Cache-Control", "no-cache, no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Access-Control-Allow-Origin", "*")
	hdr.Set("icy-name", info.Name)
	hdr.Set("icy-genre", info.Genre)
	hdr.Set("icy-description", info.Description)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	var out io.Writer = w
	if r.Header.Get("Icy-MetaData") == "1" {
		hdr.Set("icy-metaint", strconv.Itoa(icyMetaint))
		out = newICYWriter(w, icyMetaint, m.Title)
	}

	l := m.Subscribe()
	defer m.Unsubscribe(l)
	rc := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	rc.Flush()
	h.Log.Info("listener connected", "mount", m.Name(), "remote", r.RemoteAddr)
	defer h.Log.Info("listener disconnected", "mount", m.Name(), "remote", r.RemoteAddr)
	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-l.C:
			if !ok {
				return
			}
			rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := out.Write(chunk); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func iceResponse(w http.ResponseWriter, status int, ok bool, message string) {
	ret := 0
	if ok {
		ret = 1
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<?xml version=\"1.0\"?>\n<iceresponse><message>%s</message><return>%d</return></iceresponse>\n", message, ret)
}

func (h *Handler) serveMetadata(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		challenge(w)
		return
	}
	q := r.URL.Query()
	if q.Get("mode") != "updinfo" {
		iceResponse(w, http.StatusBadRequest, false, "Unsupported mode")
		return
	}
	m := h.Registry.Get(q.Get("mount"))
	if m == nil {
		iceResponse(w, http.StatusBadRequest, false, "Source does not exist")
		return
	}
	m.SetTitle(q.Get("song"))
	h.Log.Debug("metadata", "mount", m.Name(), "song", q.Get("song"))
	iceResponse(w, http.StatusOK, true, "Metadata update successful")
}
