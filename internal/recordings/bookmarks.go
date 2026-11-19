package recordings

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ap-andersson/airband-hub/internal/store"
)

const maxBookmarkName = 100

// BookmarkView is a bookmark with the recording it points into, or nil once that file is gone.
type BookmarkView struct {
	store.Bookmark
	Recording *Recording `json:"recording"`
}

// registerBookmarks adds:
//
//	GET    /api/bookmarks        every bookmark, the most recent moment first
//	GET    /api/bookmarks/{id}   one bookmark
//	POST   /api/bookmarks        {"name", "path", "offset"} adds one
//	PATCH  /api/bookmarks/{id}   {"name"} renames one
//	DELETE /api/bookmarks/{id}   removes one
//
// The last three change something, so they need a login (see internal/auth).
func (h *Handlers) registerBookmarks(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/bookmarks", h.bookmarks)
	mux.HandleFunc("GET /api/bookmarks/{id}", h.bookmark)
	mux.HandleFunc("POST /api/bookmarks", h.Auth.Require(h.addBookmark))
	mux.HandleFunc("PATCH /api/bookmarks/{id}", h.Auth.Require(h.renameBookmark))
	mux.HandleFunc("DELETE /api/bookmarks/{id}", h.Auth.Require(h.deleteBookmark))
}

// The hour being recorded is a .tmp file that is renamed when the hour ends, so bookmarks
// store the final name and find either.
func canonical(p string) string { return strings.TrimSuffix(p, ".tmp") }

func (h *Handlers) view(b store.Bookmark, byPath map[string]Recording) BookmarkView {
	v := BookmarkView{Bookmark: b}
	for _, p := range []string{b.Path, b.Path + ".tmp"} {
		if rec, ok := byPath[p]; ok {
			if fresh, err := h.Library.refresh(rec); err == nil {
				v.Recording = &fresh
				break
			}
		}
	}
	return v
}

func (h *Handlers) recordingsByPath() (map[string]Recording, error) {
	list, err := h.Library.List()
	if err != nil {
		return nil, err
	}
	m := make(map[string]Recording, len(list))
	for _, r := range list {
		m[r.Path] = r
	}
	return m, nil
}

func (h *Handlers) bookmarks(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.Bookmarks(r.Context())
	if err != nil {
		h.failStore(w, err)
		return
	}
	byPath, err := h.recordingsByPath()
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]BookmarkView, 0, len(list))
	for _, b := range list {
		out = append(out, h.view(b, byPath))
	}
	writeJSON(w, r, out)
}

func (h *Handlers) bookmarkByID(w http.ResponseWriter, r *http.Request) (store.Bookmark, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "no such bookmark", http.StatusNotFound)
		return store.Bookmark{}, false
	}
	b, err := h.Store.Bookmark(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such bookmark", http.StatusNotFound)
		return store.Bookmark{}, false
	}
	if err != nil {
		h.failStore(w, err)
		return store.Bookmark{}, false
	}
	return b, true
}

func (h *Handlers) bookmark(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bookmarkByID(w, r)
	if !ok {
		return
	}
	v := BookmarkView{Bookmark: b}
	// twice: finding the .tmp gone (the hour just ended) makes the library rescan
	for range 2 {
		for _, p := range []string{b.Path, b.Path + ".tmp"} {
			if rec, err := h.Library.Get(p); err == nil {
				v.Recording = &rec
				writeJSON(w, r, v)
				return
			}
		}
	}
	writeJSON(w, r, v)
}

func validName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= maxBookmarkName
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		http.Error(w, "the body must be JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *Handlers) addBookmark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string  `json:"name"`
		Path   string  `json:"path"`
		Offset float64 `json:"offset"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	name, ok := validName(req.Name)
	if !ok {
		http.Error(w, "the name must be 1 to 100 characters", http.StatusBadRequest)
		return
	}
	if req.Offset < 0 || req.Offset > 24*3600 {
		http.Error(w, "offset must be seconds into the recording", http.StatusBadRequest)
		return
	}
	rec, ok := h.recordingAt(w, req.Path)
	if !ok {
		return
	}
	b, err := h.Store.AddBookmark(r.Context(), store.Bookmark{
		Name:   name,
		Path:   canonical(rec.Path),
		Offset: req.Offset,
		At:     rec.Start.Add(time.Duration(req.Offset * float64(time.Second))),
		Freq:   rec.Freq,
	})
	if err != nil {
		h.failStore(w, err)
		return
	}
	h.Log.Info("bookmark added", "id", b.ID, "path", b.Path, "offset", b.Offset)
	w.Header().Set("Location", "/api/bookmarks/"+strconv.FormatInt(b.ID, 10))
	writeJSONStatus(w, r, http.StatusCreated, BookmarkView{Bookmark: b, Recording: &rec})
}

func (h *Handlers) renameBookmark(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bookmarkByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	name, ok := validName(req.Name)
	if !ok {
		http.Error(w, "the name must be 1 to 100 characters", http.StatusBadRequest)
		return
	}
	if err := h.Store.RenameBookmark(r.Context(), b.ID, name); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.failStore(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) deleteBookmark(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bookmarkByID(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteBookmark(r.Context(), b.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.failStore(w, err)
		return
	}
	h.Log.Info("bookmark deleted", "id", b.ID, "name", b.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) failStore(w http.ResponseWriter, err error) {
	h.Log.Error("bookmarks", "err", err)
	http.Error(w, "cannot read or write bookmarks", http.StatusInternalServerError)
}
