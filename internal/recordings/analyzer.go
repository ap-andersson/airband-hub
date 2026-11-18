package recordings

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ap-andersson/airband-hub/internal/mp3"
)

const peaksPerSecond = 10

// Analysis is what the waveform view needs for one recording.
type Analysis struct {
	Duration       float64       `json:"duration"`
	SampleRate     int           `json:"sample_rate"`
	PeaksPerSecond int           `json:"peaks_per_second"`
	Peaks          []byte        `json:"peaks"` // JSON: base64
	Transmissions  []mp3.Segment `json:"transmissions"`
}

type cached struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Analysis Analysis  `json:"analysis"`
}

// Analyzer decodes recordings into waveforms, caching results in memory and, when DataDir
// is set, on disk. Finished files never change, so their disk cache is kept for good.
type Analyzer struct {
	Library *Library
	DataDir string // "" keeps results in memory only
	Log     *slog.Logger

	once    sync.Once
	decodes chan struct{} // limits concurrent decodes
	mu      sync.Mutex
	mem     map[string]cached
	order   []string // for evicting the oldest memory entry
}

const memEntries = 96

func (a *Analyzer) init() {
	a.once.Do(func() {
		a.decodes = make(chan struct{}, 2)
		a.mem = map[string]cached{}
	})
}

func (a *Analyzer) diskPath(r Recording) string {
	return filepath.Join(a.DataDir, "waveforms", filepath.FromSlash(r.Path)+".json.gz")
}

func (a *Analyzer) fromMemory(r Recording) (Analysis, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.mem[r.Path]
	if ok && c.Size == r.Size && c.Modified.Equal(r.Modified) {
		return c.Analysis, true
	}
	return Analysis{}, false
}

func (a *Analyzer) remember(r Recording, an Analysis) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.mem[r.Path]; !ok {
		a.order = append(a.order, r.Path)
		if len(a.order) > memEntries {
			delete(a.mem, a.order[0])
			a.order = a.order[1:]
		}
	}
	a.mem[r.Path] = cached{Size: r.Size, Modified: r.Modified, Analysis: an}
}

func (a *Analyzer) fromDisk(r Recording) (Analysis, bool) {
	if a.DataDir == "" || r.Growing {
		return Analysis{}, false
	}
	f, err := os.Open(a.diskPath(r))
	if err != nil {
		return Analysis{}, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return Analysis{}, false
	}
	var c cached
	if json.NewDecoder(zr).Decode(&c) != nil || c.Size != r.Size || !c.Modified.Equal(r.Modified) {
		return Analysis{}, false
	}
	return c.Analysis, true
}

func (a *Analyzer) toDisk(r Recording, an Analysis) {
	if a.DataDir == "" || r.Growing {
		return
	}
	p := a.diskPath(r)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		a.Log.Warn("cannot cache waveform", "err", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		a.Log.Warn("cannot cache waveform", "err", err)
		return
	}
	zw := gzip.NewWriter(tmp)
	err = json.NewEncoder(zw).Encode(cached{Size: r.Size, Modified: r.Modified, Analysis: an})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p)
	}
	if err != nil {
		os.Remove(tmp.Name())
		a.Log.Warn("cannot cache waveform", "path", r.Path, "err", err)
	}
}

// Cached reports whether r's analysis is already available without decoding.
func (a *Analyzer) Cached(r Recording) bool {
	a.init()
	if _, ok := a.fromMemory(r); ok {
		return true
	}
	_, ok := a.fromDisk(r)
	return ok
}

// Get returns r's analysis, decoding it if needed.
func (a *Analyzer) Get(ctx context.Context, r Recording) (Analysis, error) {
	a.init()
	if an, ok := a.fromMemory(r); ok {
		return an, nil
	}
	if an, ok := a.fromDisk(r); ok {
		a.remember(r, an)
		return an, nil
	}
	select {
	case a.decodes <- struct{}{}:
	case <-ctx.Done():
		return Analysis{}, ctx.Err()
	}
	defer func() { <-a.decodes }()
	if an, ok := a.fromMemory(r); ok { // another request may have finished it meanwhile
		return an, nil
	}

	data, err := a.Library.ReadFile(r)
	if err != nil {
		return Analysis{}, err
	}
	res, err := mp3.Analyze(data, peaksPerSecond)
	if err != nil {
		return Analysis{}, err
	}
	an := Analysis{
		Duration:       res.Duration,
		SampleRate:     res.SampleRate,
		PeaksPerSecond: res.PeaksPerSecond,
		Peaks:          res.Peaks,
		Transmissions:  mp3.Transmissions(res.Peaks, res.PeaksPerSecond),
	}
	if an.Peaks == nil {
		an.Peaks = []byte{}
	}
	a.remember(r, an)
	a.toDisk(r, an)
	return an, nil
}

// Backfill analyses every finished recording that has no cached waveform yet, newest first,
// so browsing older days is instant. It pauses between files to stay out of the receiver's way.
func (a *Analyzer) Backfill(ctx context.Context, pause time.Duration) {
	a.init()
	if a.DataDir == "" {
		return
	}
	for {
		list, err := a.Library.List()
		if err != nil {
			a.Log.Warn("backfill: cannot list recordings", "err", err)
		}
		done := 0
		for i := len(list) - 1; i >= 0; i-- {
			r := list[i]
			if r.Growing {
				continue
			}
			if _, ok := a.fromDisk(r); ok {
				continue
			}
			if _, err := a.Get(ctx, r); err != nil && ctx.Err() == nil {
				a.Log.Warn("backfill: cannot analyse", "path", r.Path, "err", err)
			}
			done++
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
		if done > 0 {
			a.Log.Info("backfill: analysed recordings", "count", done)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}
