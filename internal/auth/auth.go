// Package auth guards the hub's write actions with one admin passphrase. Reading stays open.
//
// Logging in with the passphrase sets a cookie holding an expiry time and an HMAC of it. The
// HMAC key is kept in the data directory, so logins survive restarts, and the key is mixed with
// the passphrase, so changing the passphrase logs everyone out.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cookieName  = "hub_session"
	sessionLife = 90 * 24 * time.Hour
	// failed logins: at most maxFailures per failureWindow, for everyone together. A reverse
	// proxy hides the client's address, so the limit is not per client.
	maxFailures   = 10
	failureWindow = 10 * time.Minute
	failurePause  = time.Second
)

type Auth struct {
	phrase [32]byte // SHA-256 of the passphrase; zero when none is set
	set    bool
	key    []byte

	mu       sync.Mutex
	failures []time.Time
}

// New sets up auth with the passphrase. With an empty passphrase every write is refused. The
// signing key is read from (or created in) dataDir; without one it lasts until the hub stops.
func New(passphrase, dataDir string) (*Auth, error) {
	a := &Auth{}
	if passphrase != "" {
		a.phrase, a.set = sha256.Sum256([]byte(passphrase)), true
	}
	key, err := loadKey(dataDir)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, key)
	m.Write(a.phrase[:])
	a.key = m.Sum(nil)
	return a, nil
}

func loadKey(dataDir string) ([]byte, error) {
	if dataDir == "" {
		return randomKey()
	}
	path := filepath.Join(dataDir, "session.key")
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key, err := randomKey()
	if err != nil {
		return nil, err
	}
	return key, os.WriteFile(path, key, 0o600)
}

func randomKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
}

// Enabled says whether a passphrase is set, so writes are possible at all. A nil *Auth is a
// hub without a passphrase.
func (a *Auth) Enabled() bool { return a != nil && a.set }

func (a *Auth) sign(expiry string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(expiry))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// LoggedIn says whether the request carries a valid login cookie.
func (a *Auth) LoggedIn(r *http.Request) bool {
	if !a.Enabled() {
		return false
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	expiry, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(a.sign(expiry))) {
		return false
	}
	unix, err := strconv.ParseInt(expiry, 10, 64)
	return err == nil && time.Now().Unix() < unix
}

// Require wraps a handler that changes something: it answers 401 unless the request is logged in.
func (a *Auth) Require(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() {
			http.Error(w, "Changes are off: set HUB_ADMIN_PASSPHRASE to allow them", http.StatusForbidden)
			return
		}
		if !a.LoggedIn(r) {
			http.Error(w, "Log in to do this", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// Register adds POST /api/login {"passphrase"} and POST /api/logout.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
}

func (a *Auth) tooManyFailures(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.failures) > 0 && now.Sub(a.failures[0]) > failureWindow {
		a.failures = a.failures[1:]
	}
	return len(a.failures) >= maxFailures
}

func (a *Auth) fail(now time.Time) {
	a.mu.Lock()
	a.failures = append(a.failures, now)
	a.mu.Unlock()
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled() {
		http.Error(w, "Changes are off: set HUB_ADMIN_PASSPHRASE to allow them", http.StatusForbidden)
		return
	}
	if a.tooManyFailures(time.Now()) {
		http.Error(w, "Too many wrong passphrases; try again in a few minutes", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "the body must be JSON", http.StatusBadRequest)
		return
	}
	got := sha256.Sum256([]byte(req.Passphrase))
	if subtle.ConstantTimeCompare(got[:], a.phrase[:]) != 1 {
		a.fail(time.Now())
		time.Sleep(failurePause)
		http.Error(w, "Wrong passphrase", http.StatusUnauthorized)
		return
	}
	expiry := strconv.FormatInt(time.Now().Add(sessionLife).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    expiry + "." + a.sign(expiry),
		Path:     "/",
		MaxAge:   int(sessionLife.Seconds()),
		HttpOnly: true,                    // page scripts cannot read it
		SameSite: http.SameSiteStrictMode, // other sites cannot make the browser send it
		Secure:   secure(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r)})
	w.WriteHeader(http.StatusNoContent)
}

// secure: the browser reached us over https, directly or through a reverse proxy.
func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
