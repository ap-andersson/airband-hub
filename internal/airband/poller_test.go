package airband

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollerFeedsTrackerAndReportsFailures(t *testing.T) {
	body, err := os.ReadFile("testdata/metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "nope", http.StatusServiceUnavailable)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	tr := NewTracker()
	p := &Poller{URL: srv.URL, Interval: 10 * time.Millisecond, Client: srv.Client(), Tracker: tr, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitUntil(t, func() bool { return tr.State().Receiver.OK && len(tr.State().Frequencies) == 4 })
	down.Store(true)
	waitUntil(t, func() bool { r := tr.State().Receiver; return !r.OK && r.Error != "" })
	down.Store(false)
	waitUntil(t, func() bool { return tr.State().Receiver.OK })
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
