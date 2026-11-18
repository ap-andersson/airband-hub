package airband

import (
	"math"
	"sync"
	"time"
)

const (
	keepTransmissions = 24 * time.Hour
	maxTransmissions  = 5000
	recentLimit       = 50
	rateWindow        = 10 * time.Second
)

// Transmission is one completed transmission, derived from the duration and SNR
// histograms growing between two scrapes.
type Transmission struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"` // when the hub noticed it, within one scrape interval
	Freq            string    `json:"freq"`
	Label           string    `json:"label"`
	DurationSeconds float64   `json:"duration_seconds"`
	// SNR is known to a histogram bucket; both 0 when unknown
	SNRMinDB float64 `json:"snr_min_db"`
	SNRMaxDB float64 `json:"snr_max_db"`
	// Approximate is set when several transmissions ended within one scrape, so each
	// gets the average duration and no SNR
	Approximate bool `json:"approximate"`
}

type FreqState struct {
	Freq                   string  `json:"freq"`
	Label                  string  `json:"label"`
	SignalDBFS             float64 `json:"signal_dbfs"`
	NoiseDBFS              float64 `json:"noise_dbfs"`
	SquelchDBFS            float64 `json:"squelch_dbfs"`
	Open                   bool    `json:"open"`
	TxLastHour             int     `json:"tx_last_hour"`
	AirtimeLastHourSeconds float64 `json:"airtime_last_hour_seconds"`
}

type Receiver struct {
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Version   string    `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	// over the last ~10 s; nil until two scrapes exist
	ClippingRatio *float64 `json:"clipping_ratio"`
	InputDBFS     *float64 `json:"input_dbfs"`
	Overflows     uint64   `json:"overflows"`
}

type State struct {
	Receiver    Receiver       `json:"receiver"`
	Frequencies []FreqState    `json:"frequencies"`
	Recent      []Transmission `json:"recent"` // newest first
}

type deviceCounters struct {
	at       time.Time
	samples  uint64
	clipped  uint64
	powerSum float64
}

// Tracker keeps the latest scrape and the transmissions seen since the hub started.
type Tracker struct {
	mu       sync.Mutex
	last     *Sample
	receiver Receiver
	txs      []Transmission // oldest first
	devices  []deviceCounters
	now      time.Time
}

func NewTracker() *Tracker { return &Tracker{} }

func (t *Tracker) Update(s Sample, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now

	if t.last != nil {
		prev := map[string]FreqSample{}
		for _, f := range t.last.Freqs {
			prev[f.Freq] = f
		}
		for _, f := range s.Freqs {
			if p, ok := prev[f.Freq]; ok && f.TxCount > p.TxCount {
				t.recordLocked(p, f, now)
			}
		}
	}
	t.pruneLocked(now)

	if t.last != nil && s.Samples < t.last.Samples {
		t.devices = nil // rtl_airband restarted
	}
	t.devices = append(t.devices, deviceCounters{now, s.Samples, s.Clipped, s.PowerSum})
	for len(t.devices) > 2 && now.Sub(t.devices[1].at) >= rateWindow {
		t.devices = t.devices[1:]
	}

	t.last = &s
	t.receiver = Receiver{OK: true, Version: s.Version, UpdatedAt: now, Overflows: s.Overflows}
	if len(t.devices) >= 2 {
		first, last := t.devices[0], t.devices[len(t.devices)-1]
		if n := last.samples - first.samples; n > 0 {
			clip := float64(last.clipped-first.clipped) / float64(n)
			power := 10 * math.Log10((last.powerSum-first.powerSum)/float64(n))
			t.receiver.ClippingRatio = &clip
			if !math.IsInf(power, 0) && !math.IsNaN(power) {
				t.receiver.InputDBFS = &power
			}
		}
	}
}

// Fail records a failed scrape. Squelch states are cleared, since they can no longer be trusted.
func (t *Tracker) Fail(err string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
	t.receiver.OK = false
	t.receiver.Error = err
	t.receiver.UpdatedAt = now
	t.receiver.ClippingRatio, t.receiver.InputDBFS = nil, nil
	t.devices = nil
	if t.last != nil {
		for i := range t.last.Freqs {
			t.last.Freqs[i].Open = false
		}
	}
	t.pruneLocked(now)
}

func (t *Tracker) recordLocked(prev, cur FreqSample, now time.Time) {
	n := cur.TxCount - prev.TxCount
	total := cur.TxSeconds - prev.TxSeconds
	tx := Transmission{End: now, Freq: cur.Freq, Label: cur.Label, DurationSeconds: total / float64(n), Approximate: n > 1}
	if n == 1 {
		tx.SNRMinDB, tx.SNRMaxDB = snrRange(prev.SNRBuckets, cur.SNRBuckets)
	}
	tx.Start = now.Add(-time.Duration(tx.DurationSeconds * float64(time.Second)))
	for i := uint64(0); i < n; i++ {
		t.txs = append(t.txs, tx)
	}
}

// snrRange finds the bucket that gained the one new observation.
func snrRange(prev, cur []Bucket) (float64, float64) {
	if len(prev) != len(cur) {
		return 0, 0
	}
	lower := 0.0
	for i := range cur {
		if cur[i].Count > prev[i].Count {
			if math.IsInf(cur[i].UpperBound, 1) {
				return lower, lower
			}
			return lower, cur[i].UpperBound
		}
		lower = cur[i].UpperBound
	}
	return 0, 0
}

func (t *Tracker) pruneLocked(now time.Time) {
	drop := 0
	for drop < len(t.txs) && (now.Sub(t.txs[drop].End) > keepTransmissions || len(t.txs)-drop > maxTransmissions) {
		drop++
	}
	t.txs = t.txs[drop:]
}

func (t *Tracker) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := State{Receiver: t.receiver, Frequencies: []FreqState{}, Recent: []Transmission{}}
	hourAgo := t.now.Add(-time.Hour)
	if t.last != nil {
		for _, f := range t.last.Freqs {
			fs := FreqState{Freq: f.Freq, Label: f.Label, SignalDBFS: f.SignalDBFS, NoiseDBFS: f.NoiseDBFS, SquelchDBFS: f.SquelchDBFS, Open: f.Open}
			for _, tx := range t.txs {
				if tx.Freq == f.Freq && tx.End.After(hourAgo) {
					fs.TxLastHour++
					fs.AirtimeLastHourSeconds += tx.DurationSeconds
				}
			}
			st.Frequencies = append(st.Frequencies, fs)
		}
	}
	for i := len(t.txs) - 1; i >= 0 && len(st.Recent) < recentLimit; i-- {
		st.Recent = append(st.Recent, t.txs[i])
	}
	return st
}
