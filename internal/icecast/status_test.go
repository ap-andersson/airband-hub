package icecast

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func status(t *testing.T, url string) (map[string]any, http.Header) {
	t.Helper()
	resp, err := http.Get(url + "/status-json.xsl")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v["icestats"].(map[string]any), resp.Header
}

func TestStatusJSONWithoutSources(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	stats, hdr := status(t, srv.URL)
	if _, ok := stats["source"]; ok {
		t.Fatal("Icecast leaves source out when nothing is streaming")
	}
	if hdr.Get("Access-Control-Allow-Origin") != "*" || hdr.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("headers %v", hdr)
	}
}

func TestStatusJSONOneSourceIsAnObject(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	reg.Get("/essl.mp3").SetTitle("MALMEN TOWER 129.800")

	stats, _ := status(t, srv.URL)
	src, ok := stats["source"].(map[string]any)
	if !ok {
		t.Fatalf("one source must be an object, got %T", stats["source"])
	}
	// the fields tar1090's atc.js and other Icecast clients read
	if src["title"] != "MALMEN TOWER 129.800" || src["server_name"] != "OSTGOTA ATC" ||
		src["listenurl"] != srv.URL+"/essl.mp3" || src["server_type"] != "audio/mpeg" ||
		src["audio_info"] != "samplerate=8000;channels=1" || src["listeners"] != float64(0) {
		t.Fatalf("source %v", src)
	}
}

func TestStatusJSONSeveralSourcesAreAnArray(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	connectSource(t, srv, "/a.mp3")
	connectSource(t, srv, "/b.mp3")
	waitFor(t, "mounts", func() bool { return reg.Get("/a.mp3") != nil && reg.Get("/b.mp3") != nil })
	stats, _ := status(t, srv.URL)
	if list, ok := stats["source"].([]any); !ok || len(list) != 2 {
		t.Fatalf("got %T %v", stats["source"], stats["source"])
	}
}

func TestStatusJSONIsNotAMount(t *testing.T) {
	srv, _ := newServer(t, time.Minute)
	c := dial(t, srv)
	c.Write([]byte(sourceRequest("/status-json.xsl", basic(user, password))))
	if resp := readResponse(t, c); resp.StatusCode == http.StatusOK {
		t.Fatal("a source could take over /status-json.xsl")
	}
}
