// Package recordings lists rtl_airband's recorded files, analyses them into waveforms and
// transmission markers, and serves their audio from an exact time.
package recordings

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Recording is one file written by an rtl_airband "file" output.
type Recording struct {
	Path     string    `json:"path"` // relative to the recordings directory, with forward slashes
	Name     string    `json:"name"` // the output's filename_template
	Start    time.Time `json:"start"`
	Freq     string    `json:"freq,omitempty"` // MHz, when the output has include_freq
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Growing is set for the .tmp file rtl_airband is still writing
	Growing bool `json:"growing"`
}

// rtl_airband names files <template>_<YYYYMMDD>_<HH> (continuous, hourly) or
// <template>_<YYYYMMDD>_<HHMMSS> (split on transmission), optionally followed by a second
// timestamp (split_include_transmission_start) and _<frequency in Hz> (include_freq).
var nameRE = regexp.MustCompile(`^(.+?)_(\d{8})_(\d{2}|\d{6})(?:_\d{8}_\d{6})?(?:_(\d{6,10}))?\.mp3(\.tmp)?$`)

func parseName(base string, loc *time.Location) (name string, start time.Time, freq string, growing bool, ok bool) {
	m := nameRE.FindStringSubmatch(base)
	if m == nil {
		return "", time.Time{}, "", false, false
	}
	clock := m[3]
	if len(clock) == 2 {
		clock += "0000"
	}
	start, err := time.ParseInLocation("20060102150405", m[2]+clock, loc)
	if err != nil {
		return "", time.Time{}, "", false, false
	}
	if m[4] != "" {
		hz := strings.TrimLeft(m[4], "0")
		for len(hz) < 7 {
			hz = "0" + hz
		}
		freq = hz[:len(hz)-6] + "." + hz[len(hz)-6:len(hz)-3]
	}
	return m[1], start.UTC(), freq, m[5] != "", true
}

// Library lists the recordings under a directory, rescanning at most every TTL.
type Library struct {
	root *os.Root
	loc  *time.Location
	ttl  time.Duration

	mu      sync.Mutex
	list    []Recording
	byPath  map[string]Recording
	scanned time.Time
}

// NewLibrary opens dir. loc is the time zone the file names are written in: UTC unless
// rtl_airband runs with localtime = true.
func NewLibrary(dir string, loc *time.Location) (*Library, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Library{root: root, loc: loc, ttl: 30 * time.Second}, nil
}

// List returns every recording, oldest first.
func (l *Library) List() ([]Recording, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.list != nil && time.Since(l.scanned) < l.ttl {
		return l.list, nil
	}
	var list []Recording
	err := fs.WalkDir(l.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." {
				return err
			}
			return nil // an unreadable subfolder should not hide the rest
		}
		if d.IsDir() {
			return nil
		}
		name, start, freq, growing, ok := parseName(path.Base(p), l.loc)
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		list = append(list, Recording{Path: p, Name: name, Start: start, Freq: freq, Size: info.Size(), Modified: info.ModTime(), Growing: growing})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Start.Equal(list[j].Start) {
			return list[i].Start.Before(list[j].Start)
		}
		return list[i].Path < list[j].Path
	})
	l.list = list
	l.byPath = make(map[string]Recording, len(list))
	for _, r := range list {
		l.byPath[r.Path] = r
	}
	l.scanned = time.Now()
	return list, nil
}

var ErrNotFound = errors.New("no such recording")

// Get looks a recording up by path, rescanning once if it is unknown (a new hour may have started).
func (l *Library) Get(p string) (Recording, error) {
	if _, err := l.List(); err != nil {
		return Recording{}, err
	}
	l.mu.Lock()
	r, ok := l.byPath[p]
	if !ok && time.Since(l.scanned) > time.Second {
		l.scanned = time.Time{}
	}
	l.mu.Unlock()
	if ok {
		return l.refresh(r)
	}
	if _, err := l.List(); err != nil {
		return Recording{}, err
	}
	l.mu.Lock()
	r, ok = l.byPath[p]
	l.mu.Unlock()
	if !ok {
		return Recording{}, ErrNotFound
	}
	return l.refresh(r)
}

// refresh updates size and time of a file that may still be growing.
func (l *Library) refresh(r Recording) (Recording, error) {
	info, err := l.root.Stat(r.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// e.g. the .tmp was just renamed: the cached list is out of date
			l.mu.Lock()
			l.scanned = time.Time{}
			l.mu.Unlock()
			return Recording{}, ErrNotFound
		}
		return Recording{}, err
	}
	r.Size, r.Modified = info.Size(), info.ModTime()
	return r, nil
}

// ReadFile reads a recording; the path cannot leave the recordings directory.
func (l *Library) ReadFile(r Recording) ([]byte, error) {
	return l.root.ReadFile(r.Path)
}

func (l *Library) Open(r Recording) (*os.File, error) {
	return l.root.Open(r.Path)
}
