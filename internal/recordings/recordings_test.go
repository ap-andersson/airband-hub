package recordings

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParseName(t *testing.T) {
	cases := []struct {
		base, name, start, freq string
		growing                 bool
	}{
		{"essl_20261118_16.mp3", "essl", "2026-11-18T16:00:00Z", "", false},
		{"essl_20261118_17.mp3.tmp", "essl", "2026-11-18T17:00:00Z", "", true},
		{"TWR+GND_20261118_163012.mp3", "TWR+GND", "2026-11-18T16:30:12Z", "", false},
		{"scan_out_20261118_163012_129800000.mp3", "scan_out", "2026-11-18T16:30:12Z", "129.800", false},
		{"tx_20261118_163012_20261118_163110_118805000.mp3", "tx", "2026-11-18T16:30:12Z", "118.805", false},
	}
	for _, c := range cases {
		name, start, freq, growing, ok := parseName(c.base, time.UTC)
		if !ok || name != c.name || start.Format(time.RFC3339) != c.start || freq != c.freq || growing != c.growing {
			t.Errorf("%s: got %q %v %q %v %v", c.base, name, start, freq, growing, ok)
		}
	}
	for _, bad := range []string{"notes.txt", "essl_2026_16.mp3", "essl_20261318_16.mp3", "essl_20261118_16.wav"} {
		if _, _, _, _, ok := parseName(bad, time.UTC); ok {
			t.Errorf("%s accepted", bad)
		}
	}
	stockholm, _ := time.LoadLocation("Europe/Stockholm")
	if _, start, _, _, _ := parseName("essl_20261118_16.mp3", stockholm); start.Format(time.RFC3339) != "2026-11-18T15:00:00Z" {
		t.Errorf("localtime names: got %v", start)
	}
}

func setup(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	dir, data := t.TempDir(), t.TempDir()
	fixture, err := os.ReadFile("../mp3/testdata/tones.mp3")
	if err != nil {
		t.Fatal(err)
	}
	put := func(rel string, b []byte) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("2026/11/17/essl_20261117_23.mp3", fixture) // 00:00 on the 18th in Stockholm
	put("2026/11/18/essl_20261118_16.mp3", fixture)
	put("2026/11/18/essl_20261118_17.mp3.tmp", fixture[:len(fixture)/2])
	put("2026/11/18/notes.txt", []byte("not a recording"))
	os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.mp3"), fixture, 0o644)

	lib, err := NewLibrary(dir, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handlers{Library: lib, Analyzer: &Analyzer{Library: lib, DataDir: data, Log: log}, Log: log}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dir, data
}

func get(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp
}

func TestDaysAndListFollowTheViewersTimeZone(t *testing.T) {
	srv, _, _ := setup(t)
	var utc []day
	get(t, srv.URL+"/api/recordings/days", &utc)
	if len(utc) != 2 || utc[0] != (day{"2026-11-18", 2}) || utc[1] != (day{"2026-11-17", 1}) {
		t.Fatalf("UTC days %+v", utc)
	}
	var local []day
	get(t, srv.URL+"/api/recordings/days?tz=Europe/Stockholm", &local)
	if len(local) != 1 || local[0] != (day{"2026-11-18", 3}) {
		t.Fatalf("Stockholm days %+v", local)
	}
	var recs []Recording
	get(t, srv.URL+"/api/recordings?date=2026-11-18&tz=Europe/Stockholm", &recs)
	if len(recs) != 3 || recs[0].Path != "2026/11/17/essl_20261117_23.mp3" || !recs[2].Growing {
		t.Fatalf("recordings %+v", recs)
	}
	if resp := get(t, srv.URL+"/api/recordings/days?tz=Mars/Olympus", nil); resp.StatusCode != 400 {
		t.Fatalf("bad zone: %d", resp.StatusCode)
	}
}

func TestWaveformIsComputedAndCachedOnDisk(t *testing.T) {
	srv, _, data := setup(t)
	var an Analysis
	get(t, srv.URL+"/api/recordings/waveform?path=2026/11/18/essl_20261118_16.mp3", &an)
	if an.PeaksPerSecond != 10 || len(an.Peaks) < 600 || len(an.Transmissions) != 2 {
		t.Fatalf("analysis: %v s, %d peaks, %+v", an.Duration, len(an.Peaks), an.Transmissions)
	}
	cache := filepath.Join(data, "waveforms", "2026", "11", "18", "essl_20261118_16.mp3.json.gz")
	f, err := os.Open(cache)
	if err != nil {
		t.Fatalf("not cached on disk: %v", err)
	}
	defer f.Close()
	if _, err := gzip.NewReader(f); err != nil {
		t.Fatal(err)
	}
	// the growing file is analysed but not cached on disk
	get(t, srv.URL+"/api/recordings/waveform?path=2026/11/18/essl_20261118_17.mp3.tmp", &an)
	if _, err := os.Stat(filepath.Join(data, "waveforms", "2026", "11", "18", "essl_20261118_17.mp3.tmp.json.gz")); err == nil {
		t.Fatal("a growing file was cached on disk")
	}
}

func TestAudioStartsAtExactFrame(t *testing.T) {
	srv, _, _ := setup(t)
	resp, err := http.Get(srv.URL + "/api/recordings/audio?path=2026/11/18/essl_20261118_16.mp3&from=30")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("got %d %v", resp.StatusCode, resp.Header)
	}
	start, err := strconv.ParseFloat(resp.Header.Get("X-Audio-Start"), 64)
	if err != nil || start > 30 || 30-start >= 0.072 {
		t.Fatalf("X-Audio-Start %q", resp.Header.Get("X-Audio-Start"))
	}
	if len(body) < 100 || body[0] != 0xFF || body[1]&0xE0 != 0xE0 {
		t.Fatalf("body does not start on a frame: % x", body[:4])
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatal("players need range support")
	}
}

func TestPathsCannotEscape(t *testing.T) {
	srv, _, _ := setup(t)
	for _, p := range []string{"../secret.mp3", "/etc/passwd", "2026/../../secret.mp3", "2026/11/18/notes.txt", "2026/11/18/missing_20261118_01.mp3", ""} {
		resp := get(t, srv.URL+"/api/recordings/audio?path="+p, nil)
		if resp.StatusCode == 200 {
			t.Errorf("%q was served", p)
		}
	}
}

func TestDownload(t *testing.T) {
	srv, _, _ := setup(t)
	resp := get(t, srv.URL+"/api/recordings/download?path=2026/11/18/essl_20261118_17.mp3.tmp", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Disposition") != `attachment; filename="essl_20261118_17.mp3"` {
		t.Fatalf("got %d %v", resp.StatusCode, resp.Header)
	}
}

func TestBackfillCachesFinishedFiles(t *testing.T) {
	srv, dir, data := setup(t)
	_ = srv
	lib, _ := NewLibrary(dir, time.UTC)
	a := &Analyzer{Library: lib, DataDir: data, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	go a.Backfill(ctx, time.Millisecond)
	list, _ := lib.List()
	deadline := time.Now().Add(5 * time.Second)
	for {
		all := true
		for _, r := range list {
			if !r.Growing && !(&Analyzer{Library: lib, DataDir: data}).Cached(r) {
				all = false
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("backfill did not cache every finished recording")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
