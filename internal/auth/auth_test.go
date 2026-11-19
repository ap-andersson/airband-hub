package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func server(t *testing.T, a *Auth) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("DELETE /thing", a.Require(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func login(t *testing.T, url, phrase string) (*http.Response, *http.Cookie) {
	t.Helper()
	resp := do(t, "POST", url+"/api/login", `{"passphrase":"`+phrase+`"}`, nil)
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return resp, c
		}
	}
	return resp, nil
}

func TestWritesNeedLogin(t *testing.T) {
	a, err := New("correct horse", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := server(t, a)
	if resp := do(t, "DELETE", srv.URL+"/thing", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without login: %d", resp.StatusCode)
	}
	if resp, c := login(t, srv.URL, "wrong"); resp.StatusCode != http.StatusUnauthorized || c != nil {
		t.Fatalf("wrong passphrase: %d %v", resp.StatusCode, c)
	}
	resp, c := login(t, srv.URL, "correct horse")
	if resp.StatusCode != http.StatusNoContent || c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("login: %d %+v", resp.StatusCode, c)
	}
	if resp := do(t, "DELETE", srv.URL+"/thing", "", c); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logged in: %d", resp.StatusCode)
	}
	forged := *c
	forged.Value = "99999999999." + strings.SplitN(c.Value, ".", 2)[1]
	if resp := do(t, "DELETE", srv.URL+"/thing", "", &forged); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged expiry accepted: %d", resp.StatusCode)
	}
}

func TestLoginSurvivesRestartButNotANewPassphrase(t *testing.T) {
	dir := t.TempDir()
	a, _ := New("one", dir)
	_, c := login(t, server(t, a).URL, "one")
	if fi, err := os.Stat(filepath.Join(dir, "session.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	same, _ := New("one", dir)
	if resp := do(t, "DELETE", server(t, same).URL+"/thing", "", c); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("after restart: %d", resp.StatusCode)
	}
	changed, _ := New("two", dir)
	if resp := do(t, "DELETE", server(t, changed).URL+"/thing", "", c); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after changing the passphrase: %d", resp.StatusCode)
	}
}

func TestNoPassphraseMeansNoWrites(t *testing.T) {
	a, _ := New("", "")
	srv := server(t, a)
	if resp, _ := login(t, srv.URL, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login with no passphrase set: %d", resp.StatusCode)
	}
	if resp := do(t, "DELETE", srv.URL+"/thing", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write with no passphrase set: %d", resp.StatusCode)
	}
}

func TestGuessingIsLimited(t *testing.T) {
	a, _ := New("secret", "")
	srv := server(t, a)
	for range maxFailures {
		a.fail(time.Now()) // as if guessed already, without waiting for the pauses
	}
	if resp, _ := login(t, srv.URL, "secret"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", maxFailures, resp.StatusCode)
	}
}
