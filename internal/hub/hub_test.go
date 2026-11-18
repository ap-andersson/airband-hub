package hub

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ap-andersson/airband-hub/internal/relay"
)

func TestHealthzAndMounts(t *testing.T) {
	reg := relay.NewRegistry(relay.Options{BurstBytes: 64, Grace: time.Minute})
	m, _ := reg.AttachSource("/essl.mp3", relay.StreamInfo{Name: "OSTGOTA ATC"}, nil)
	m.SetTitle("MALMEN TOWER 129.800")
	srv := httptest.NewServer(NewHandler(Config{SourceUser: "source", SourcePassword: "x", SourceTimeout: time.Minute}, reg, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/api/mounts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var mounts []relay.Status
	if err := json.NewDecoder(resp.Body).Decode(&mounts); err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Mount != "/essl.mp3" || mounts[0].Title != "MALMEN TOWER 129.800" || mounts[0].Info.Name != "OSTGOTA ATC" {
		t.Fatalf("unexpected mounts %+v", mounts)
	}
}
