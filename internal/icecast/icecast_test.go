package icecast

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ap-andersson/airband-hub/internal/relay"
)

const (
	user     = "source"
	password = "testpass"
)

func newServer(t *testing.T, sourceTimeout time.Duration) (*httptest.Server, *relay.Registry) {
	t.Helper()
	reg := relay.NewRegistry(relay.Options{BurstBytes: 1024, Grace: time.Minute})
	h := &Handler{
		Registry:      reg,
		User:          user,
		Password:      password,
		SourceTimeout: sourceTimeout,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		reg.Shutdown()
		srv.Close()
	})
	return srv, reg
}

func basic(u, p string) string { return base64.StdEncoding.EncodeToString([]byte(u + ":" + p)) }

// sourceRequest is the request head libshout 2.4.6 sends, as captured from rtl_airband.
func sourceRequest(mount, auth string) string {
	head := "SOURCE " + mount + " HTTP/1.0\r\n"
	if auth != "" {
		head += "Authorization: Basic " + auth + "\r\n"
	}
	head += "Host: 127.0.0.1:8000\r\n" +
		"User-Agent: libshout/2.4.6\r\n" +
		"Content-Type: audio/mpeg\r\n"
	if auth == "" {
		head += "Content-Length: 0\r\nConnection: Keep-Alive\r\n"
	}
	head += "ice-public: 0\r\n" +
		"ice-name: OSTGOTA ATC\r\n" +
		"ice-genre: ATC\r\n" +
		"ice-description: Linkoping and Ostgota ATC\r\n" +
		"ice-audio-info: samplerate=8000;channels=1\r\n" +
		"Prefer: return=minimal\r\n\r\n"
	return head
}

func dial(t *testing.T, srv *httptest.Server) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readResponse(t *testing.T, c net.Conn) *http.Response {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// connectSource performs libshout's full handshake and returns the streaming connection.
func connectSource(t *testing.T, srv *httptest.Server, mount string) net.Conn {
	t.Helper()
	probe := dial(t, srv)
	io.WriteString(probe, sourceRequest(mount, ""))
	if resp := readResponse(t, probe); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("probe without auth: got %d, want 401", resp.StatusCode)
	}
	probe.Close()

	c := dial(t, srv)
	io.WriteString(c, sourceRequest(mount, basic(user, password)))
	if resp := readResponse(t, c); resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated SOURCE: got %d, want 200", resp.StatusCode)
	}
	c.SetReadDeadline(time.Time{})
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUnauthenticatedSourceGets401WithChallenge(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	c := dial(t, srv)
	io.WriteString(c, sourceRequest("/essl.mp3", ""))
	resp := readResponse(t, c)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("missing Basic challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestWrongPasswordIs401(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	c := dial(t, srv)
	io.WriteString(c, sourceRequest("/essl.mp3", basic(user, "wrong")))
	if resp := readResponse(t, c); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestSourceStreamReachesListener(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	src := connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })

	if s := reg.Get("/essl.mp3").Status(); s.Info.Name != "OSTGOTA ATC" || s.Info.ContentType != "audio/mpeg" || s.Info.AudioInfo != "samplerate=8000;channels=1" {
		t.Fatalf("stream info not taken from ice-* headers: %+v", s.Info)
	}

	resp, err := http.Get(srv.URL + "/essl.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" || resp.Header.Get("icy-name") != "OSTGOTA ATC" {
		t.Fatalf("listener response %d %v", resp.StatusCode, resp.Header)
	}

	payload := append([]byte{0xFF, 0xE3}, bytes.Repeat([]byte{7}, 300)...)
	src.Write(payload)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("listener got different bytes")
	}
}

func TestSecondSourceOnSameMountIs403(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	c := dial(t, srv)
	io.WriteString(c, sourceRequest("/essl.mp3", basic(user, password)))
	if resp := readResponse(t, c); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403", resp.StatusCode)
	}
}

func TestSourceDisconnectFreesMountForReconnect(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	src := connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	src.Close()
	waitFor(t, "source detached", func() bool { return !reg.Get("/essl.mp3").Status().SourceConnected })
	connectSource(t, srv, "/essl.mp3")
}

func TestIdleSourceIsDropped(t *testing.T) {
	srv, reg := newServer(t, 50*time.Millisecond)
	connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	waitFor(t, "idle source dropped", func() bool { return !reg.Get("/essl.mp3").Status().SourceConnected })
}

func TestPutIsAcceptedLikeSource(t *testing.T) {
	// Icecast 2.4+ also accepts PUT, which newer libshout versions may use
	srv, reg := newServer(t, time.Minute)
	c := dial(t, srv)
	io.WriteString(c, strings.Replace(sourceRequest("/essl.mp3", basic(user, password)), "SOURCE ", "PUT ", 1))
	if resp := readResponse(t, c); resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d", resp.StatusCode)
	}
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
}

func TestReservedPathsCannotBeMounts(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	for _, mount := range []string{"/", "/api/x", "/admin/metadata"} {
		c := dial(t, srv)
		io.WriteString(c, sourceRequest(mount, basic(user, password)))
		resp := readResponse(t, c)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("mount %q was accepted", mount)
		}
	}
}

func TestListenerOnMissingMountIs404(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	resp, err := http.Get(srv.URL + "/nothing.mp3")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestHeadListenerGetsHeadersOnly(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	resp, err := http.Head(srv.URL + "/essl.mp3")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("got %d %v", resp.StatusCode, resp.Header)
	}
	if n := reg.Get("/essl.mp3").Status().Listeners; n != 0 {
		t.Fatalf("HEAD left %d listeners", n)
	}
}

func TestListenerDisconnectUnsubscribes(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	src := connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	resp, err := http.Get(srv.URL + "/essl.mp3")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "listener", func() bool { return reg.Get("/essl.mp3").Status().Listeners == 1 })
	resp.Body.Close()
	// the handler notices the closed connection on its next write
	for i := 0; i < 50 && reg.Get("/essl.mp3").Status().Listeners != 0; i++ {
		src.Write([]byte{0xFF, 0xE3, 0, 0})
		time.Sleep(10 * time.Millisecond)
	}
	if n := reg.Get("/essl.mp3").Status().Listeners; n != 0 {
		t.Fatalf("%d listeners after disconnect", n)
	}
}

func metadataURL(srv *httptest.Server, mount, song string) string {
	// exactly the escaping libshout uses
	return srv.URL + "/admin/metadata?mode=updinfo&charset=UTF-8&mount=" + strings.ReplaceAll(strings.ReplaceAll(mount, "/", "%2f"), ".", "%2e") + "&song=" + strings.ReplaceAll(strings.ReplaceAll(song, " ", "%20"), ".", "%2e")
}

func TestMetadataUpdateSetsTitle(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })

	// libshout first tries without credentials
	resp, err := http.Get(metadataURL(srv, "/essl.mp3", "SAAB TOWER 118.805"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("unauthenticated metadata: got %d %v", resp.StatusCode, resp.Header)
	}

	req, _ := http.NewRequest("GET", metadataURL(srv, "/essl.mp3", "SAAB TOWER 118.805"), nil)
	req.SetBasicAuth(user, password)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<return>1</return>") {
		t.Fatalf("metadata update: got %d %s", resp.StatusCode, body)
	}
	if got := reg.Get("/essl.mp3").Status().Title; got != "SAAB TOWER 118.805" {
		t.Fatalf("title = %q", got)
	}
}

func TestMetadataForUnknownMountFails(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	req, _ := http.NewRequest("GET", metadataURL(srv, "/none.mp3", "X"), nil)
	req.SetBasicAuth(user, password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 || !strings.Contains(string(body), "<return>0</return>") {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
}
