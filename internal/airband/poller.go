package airband

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Poller scrapes rtl_airband's /metrics on an interval and feeds the Tracker.
type Poller struct {
	URL      string
	Interval time.Duration
	Client   *http.Client
	Tracker  *Tracker
	Log      *slog.Logger
}

func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	failing := false
	for {
		err := p.scrape(ctx)
		now := time.Now()
		if err != nil {
			p.Tracker.Fail(err.Error(), now)
			if !failing {
				p.Log.Warn("cannot read rtl_airband metrics", "url", p.URL, "err", err)
			}
			failing = true
		} else if failing {
			p.Log.Info("rtl_airband metrics are back", "url", p.URL)
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Poller) scrape(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.Interval+2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", p.URL, resp.Status)
	}
	s, err := Parse(resp.Body)
	if err != nil {
		return err
	}
	p.Tracker.Update(s, time.Now())
	return nil
}
