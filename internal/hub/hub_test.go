package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ap-andersson/airband-hub/internal/airband"
	"github.com/ap-andersson/airband-hub/internal/relay"
)

func newTestServer(t *testing.T, tracker *airband.Tracker) (*httptest.Server, *relay.Registry) {
	t.Helper()
	reg := relay.NewRegistry(relay.Options{BurstBytes: 64, Grace: time.Minute})
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui:"+r.URL.Path) })
	srv := httptest.NewServer(NewHandler(Config{
		SourceUser: "source", SourcePassword: "x", SourceTimeout: time.Minute,
		Tracker: tracker, LiveInterval: 20 * time.Millisecond, UI: ui,
	}, reg, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(func() { reg.Shutdown(); srv.Close() })
	return srv, reg
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestHealthzAndMounts(t *testing.T) {
	srv, reg := newTestServer(t, nil)
	m, _ := reg.AttachSource("/essl.mp3", relay.StreamInfo{Name: "OSTGOTA ATC"}, nil)
	m.SetTitle("MALMEN TOWER 129.800")

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	resp.Body.Close()

	var mounts []relay.Status
	getJSON(t, srv.URL+"/api/mounts", &mounts)
	if len(mounts) != 1 || mounts[0].Mount != "/essl.mp3" || mounts[0].Title != "MALMEN TOWER 129.800" || mounts[0].Info.Name != "OSTGOTA ATC" {
		t.Fatalf("unexpected mounts %+v", mounts)
	}
}

func TestLiveWithoutMetrics(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	var live Live
	getJSON(t, srv.URL+"/api/live", &live)
	if live.MetricsEnabled || live.Frequencies == nil || live.Recent == nil {
		t.Fatalf("unexpected %+v", live)
	}
}

func TestLiveWithMetrics(t *testing.T) {
	tr := airband.NewTracker()
	tr.Update(airband.Sample{Version: "v7", Freqs: []airband.FreqSample{{Freq: "129.800", Label: "MALMEN TOWER", Open: true}}}, time.Now())
	srv, _ := newTestServer(t, tr)
	var live Live
	getJSON(t, srv.URL+"/api/live", &live)
	if !live.MetricsEnabled || !live.Receiver.OK || len(live.Frequencies) != 1 || !live.Frequencies[0].Open {
		t.Fatalf("unexpected %+v", live)
	}
}

func TestLiveStreamPushesEvents(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	resp, err := http.Get(srv.URL + "/api/live/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	events := 0
	for events < 2 && sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var live Live
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &live); err != nil {
			t.Fatal(err)
		}
		events++
	}
	if events < 2 {
		t.Fatalf("got %d events", events)
	}
}

func TestNonMountPathsGoToUI(t *testing.T) {
	srv, reg := newTestServer(t, nil)
	reg.AttachSource("/essl.mp3", relay.StreamInfo{ContentType: "audio/mpeg"}, nil)
	for path, want := range map[string]string{"/": "ui:/", "/recordings": "ui:/recordings"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Errorf("%s served %q", path, body)
		}
	}
	resp, err := http.Head(srv.URL + "/essl.mp3")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatal("a live mount must win over the UI")
	}
}
