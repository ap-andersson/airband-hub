// Command airband-hub receives rtl_airband's live stream and serves it to players.
//
// Configuration is through environment variables:
//
//	HUB_LISTEN           address to listen on (default ":8000")
//	HUB_SOURCE_PASSWORD  password rtl_airband's icecast output uses (required)
//	HUB_SOURCE_USER      username for it (default "source")
//	HUB_BURST_BYTES      recent stream sent to a new listener at once (default 24576, ~4 s at 48 kbps)
//	HUB_SOURCE_TIMEOUT   drop a source that sends nothing this long (default 30s; rtl_airband
//	                     sends no audio for ~10 s after connecting while the dongle starts)
//	HUB_MOUNT_GRACE      keep listeners this long after the source drops (default 30s)
//	HUB_METRICS_URL      rtl_airband's metrics endpoint, e.g. http://rtlsdrairband:9101/metrics.
//	                     Without it the dashboard shows only the stream.
//	HUB_METRICS_INTERVAL how often to read it (default 1s)
//	HUB_LOG_LEVEL        debug, info, warn or error (default info)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ap-andersson/airband-hub/internal/airband"
	"github.com/ap-andersson/airband-hub/internal/hub"
	"github.com/ap-andersson/airband-hub/internal/relay"
	"github.com/ap-andersson/airband-hub/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "airband-hub:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(env(key, def.String()))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration like 10s", key)
	}
	return d, nil
}

func run() error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("HUB_LOG_LEVEL", "info"))); err != nil {
		return fmt.Errorf("HUB_LOG_LEVEL: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	password := os.Getenv("HUB_SOURCE_PASSWORD")
	if password == "" {
		return errors.New("HUB_SOURCE_PASSWORD must be set")
	}
	burst, err := strconv.Atoi(env("HUB_BURST_BYTES", "24576"))
	if err != nil || burst < 0 {
		return errors.New("HUB_BURST_BYTES must be a non-negative number")
	}
	sourceTimeout, err := envDuration("HUB_SOURCE_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}
	grace, err := envDuration("HUB_MOUNT_GRACE", 30*time.Second)
	if err != nil {
		return err
	}

	metricsInterval, err := envDuration("HUB_METRICS_INTERVAL", time.Second)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var tracker *airband.Tracker
	if url := os.Getenv("HUB_METRICS_URL"); url != "" {
		tracker = airband.NewTracker()
		poller := &airband.Poller{URL: url, Interval: metricsInterval, Client: &http.Client{}, Tracker: tracker, Log: log}
		go poller.Run(ctx)
		log.Info("reading rtl_airband metrics", "url", url, "every", metricsInterval)
	} else {
		log.Info("HUB_METRICS_URL not set; the dashboard shows only the stream")
	}

	reg := relay.NewRegistry(relay.Options{BurstBytes: burst, Grace: grace})
	srv := &http.Server{
		Addr: env("HUB_LISTEN", ":8000"),
		Handler: hub.NewHandler(hub.Config{
			SourceUser:     env("HUB_SOURCE_USER", "source"),
			SourcePassword: password,
			SourceTimeout:  sourceTimeout,
			Tracker:        tracker,
			LiveInterval:   time.Second,
			UI:             web.Handler(),
		}, reg, log),
		// no write timeout: listener streams never end
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	// sources and listeners are long-lived or hijacked, so Shutdown would wait on them
	reg.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
