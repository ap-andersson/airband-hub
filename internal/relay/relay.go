// Package relay fans a live MP3 stream from one source out to many listeners.
//
// A Mount outlives its source for a grace period, so a source that reconnects
// (rtl_airband restarting after a config change) picks up the listeners that are
// still attached instead of dropping them.
package relay

import (
	"errors"
	"io"
	"sort"
	"sync"
	"time"
)

var ErrMountInUse = errors.New("mount already has a source")

type Options struct {
	// BurstBytes of recent stream are sent to a new listener at once, so playback starts
	// without waiting for the player's buffer to fill at stream rate.
	BurstBytes int
	// Grace is how long a mount without a source keeps its listeners.
	Grace time.Duration
	// ListenerQueue is how many chunks a listener may fall behind before it is dropped.
	ListenerQueue int
}

// StreamInfo is what the source announced in its ice-* headers.
type StreamInfo struct {
	Name        string `json:"name"`
	Genre       string `json:"genre"`
	Description string `json:"description"`
	ContentType string `json:"content_type"`
	AudioInfo   string `json:"audio_info"`
}

type Status struct {
	Mount           string     `json:"mount"`
	Info            StreamInfo `json:"info"`
	Title           string     `json:"title"`
	TitleUpdated    time.Time  `json:"title_updated"`
	SourceConnected bool       `json:"source_connected"`
	SourceSince     time.Time  `json:"source_since"`
	Listeners       int        `json:"listeners"`
	BytesIn         uint64     `json:"bytes_in"`
}

type Registry struct {
	opts   Options
	mu     sync.Mutex
	mounts map[string]*Mount
}

func NewRegistry(opts Options) *Registry {
	if opts.ListenerQueue <= 0 {
		opts.ListenerQueue = 256
	}
	return &Registry{opts: opts, mounts: map[string]*Mount{}}
}

// AttachSource claims the mount for a new source. source is closed by Shutdown and may be nil.
func (r *Registry) AttachSource(name string, info StreamInfo, source io.Closer) (*Mount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.mounts[name]
	if m == nil {
		m = &Mount{name: name, reg: r, listeners: map[*Listener]struct{}{}}
		r.mounts[name] = m
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sourceConnected {
		return nil, ErrMountInUse
	}
	if m.graceTimer != nil {
		m.graceTimer.Stop()
		m.graceTimer = nil
	}
	m.info = info
	m.source = source
	m.sourceConnected = true
	m.sourceSince = time.Now()
	return m, nil
}

// Get returns the mount, or nil if there is none.
func (r *Registry) Get(name string) *Mount {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mounts[name]
}

func (r *Registry) List() []Status {
	r.mu.Lock()
	mounts := make([]*Mount, 0, len(r.mounts))
	for _, m := range r.mounts {
		mounts = append(mounts, m)
	}
	r.mu.Unlock()
	out := make([]Status, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, m.Status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

// Shutdown disconnects every source and listener.
func (r *Registry) Shutdown() {
	r.mu.Lock()
	mounts := r.mounts
	r.mounts = map[string]*Mount{}
	r.mu.Unlock()
	for _, m := range mounts {
		m.end()
	}
}

// Listener receives the stream on C, which is closed when the listener is dropped or the mount ends.
type Listener struct {
	C      chan []byte
	closed bool // guarded by the mount's mutex
}

type Mount struct {
	name string
	reg  *Registry

	mu              sync.Mutex
	info            StreamInfo
	title           string
	titleUpdated    time.Time
	source          io.Closer
	sourceConnected bool
	sourceSince     time.Time
	burst           []byte
	bytesIn         uint64
	listeners       map[*Listener]struct{}
	graceTimer      *time.Timer
}

func (m *Mount) Name() string { return m.name }

func (m *Mount) Info() StreamInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

// Write sends p to every listener. It never blocks: a listener whose queue is full is dropped.
func (m *Mount) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	chunk := append([]byte(nil), p...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytesIn += uint64(len(chunk))
	m.burst = append(m.burst, chunk...)
	if over := len(m.burst) - m.reg.opts.BurstBytes; over > 0 {
		m.burst = append(m.burst[:0:0], m.burst[over:]...)
	}
	for l := range m.listeners {
		select {
		case l.C <- chunk:
		default:
			m.dropLocked(l)
		}
	}
}

func (m *Mount) Subscribe() *Listener {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := &Listener{C: make(chan []byte, m.reg.opts.ListenerQueue)}
	if start := frameStart(m.burst); start >= 0 {
		l.C <- append([]byte(nil), m.burst[start:]...)
	}
	m.listeners[l] = struct{}{}
	return l
}

func (m *Mount) Unsubscribe(l *Listener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropLocked(l)
}

func (m *Mount) SetTitle(title string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.title = title
	m.titleUpdated = time.Now()
}

// DetachSource marks the source gone. Listeners stay for the grace period in case it reconnects.
func (m *Mount) DetachSource() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sourceConnected = false
	m.source = nil
	if m.graceTimer != nil {
		m.graceTimer.Stop()
	}
	m.graceTimer = time.AfterFunc(m.reg.opts.Grace, m.expire)
}

func (m *Mount) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{
		Mount:           m.name,
		Info:            m.info,
		Title:           m.title,
		TitleUpdated:    m.titleUpdated,
		SourceConnected: m.sourceConnected,
		SourceSince:     m.sourceSince,
		Listeners:       len(m.listeners),
		BytesIn:         m.bytesIn,
	}
}

func (m *Mount) expire() {
	m.reg.mu.Lock()
	m.mu.Lock()
	expired := !m.sourceConnected && m.reg.mounts[m.name] == m
	if expired {
		delete(m.reg.mounts, m.name)
	}
	m.mu.Unlock()
	m.reg.mu.Unlock()
	if expired {
		m.end()
	}
}

func (m *Mount) end() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source != nil {
		m.source.Close()
		m.source = nil
	}
	m.sourceConnected = false
	if m.graceTimer != nil {
		m.graceTimer.Stop()
		m.graceTimer = nil
	}
	for l := range m.listeners {
		m.dropLocked(l)
	}
}

func (m *Mount) dropLocked(l *Listener) {
	delete(m.listeners, l)
	if !l.closed {
		l.closed = true
		close(l.C)
	}
}

// frameStart returns the index of the first MPEG audio frame sync in b, or -1.
// Starting a new listener on a frame boundary lets players lock on immediately.
func frameStart(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xFF && b[i+1]&0xE0 == 0xE0 {
			return i
		}
	}
	return -1
}
