package relay

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// frame returns a fake MPEG-2 Layer III frame: a valid sync word followed by filler.
func frame(fill byte, size int) []byte {
	f := bytes.Repeat([]byte{fill}, size)
	f[0], f[1] = 0xFF, 0xE3
	return f
}

func recv(t *testing.T, l *Listener) []byte {
	t.Helper()
	select {
	case b, ok := <-l.C:
		if !ok {
			t.Fatal("listener channel closed")
		}
		return b
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data")
	}
	return nil
}

func expectClosed(t *testing.T, l *Listener) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-l.C:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("listener channel not closed")
		}
	}
}

func TestAttachSourceTwiceIsRejected(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 1024, Grace: time.Minute})
	if _, err := r.AttachSource("/a.mp3", StreamInfo{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AttachSource("/a.mp3", StreamInfo{}, nil); !errors.Is(err, ErrMountInUse) {
		t.Fatalf("got %v, want ErrMountInUse", err)
	}
}

func TestGetUnknownMount(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 1024, Grace: time.Minute})
	if r.Get("/nope.mp3") != nil {
		t.Fatal("unknown mount should be nil")
	}
}

func TestWriteFansOutToAllListeners(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 1024, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	l1, l2 := m.Subscribe(), m.Subscribe()
	data := frame(1, 100)
	m.Write(data)
	for _, l := range []*Listener{l1, l2} {
		if got := recv(t, l); !bytes.Equal(got, data) {
			t.Fatalf("listener got %x", got)
		}
	}
}

func TestWriteDoesNotAliasCallerBuffer(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 1024, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	l := m.Subscribe()
	buf := frame(1, 10)
	m.Write(buf)
	buf[5] = 99
	if got := recv(t, l); got[5] != 1 {
		t.Fatal("listener sees later changes to the source's read buffer")
	}
}

func TestNewListenerGetsBurstStartingAtFrameSync(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 250, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	for i := 0; i < 5; i++ {
		m.Write(frame(byte(i+1), 100))
	}
	burst := recv(t, m.Subscribe())
	// the last 250 bytes start mid-frame; the burst is cut to the next frame start
	if len(burst) != 200 || burst[0] != 0xFF || burst[2] != 4 {
		t.Fatalf("burst len %d starts %x", len(burst), burst[:3])
	}
}

func TestNoBurstBeforeAnyData(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 250, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	l := m.Subscribe()
	select {
	case b := <-l.C:
		t.Fatalf("unexpected burst %x", b)
	default:
	}
}

func TestSlowListenerIsDroppedWithoutBlockingWriter(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 16, Grace: time.Minute, ListenerQueue: 4})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	slow, fast := m.Subscribe(), m.Subscribe()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			m.Write(frame(1, 8))
			<-fast.C
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer blocked on a slow listener")
	}
	expectClosed(t, slow)
	if got := m.Status().Listeners; got != 1 {
		t.Fatalf("listeners = %d, want 1", got)
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 16, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	l := m.Subscribe()
	m.Unsubscribe(l)
	m.Unsubscribe(l)
	expectClosed(t, l)
	m.Write(frame(1, 8)) // must not panic on a closed channel
}

func TestSourceCanReconnectWithinGraceKeepingListeners(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 16, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{Name: "old"}, nil)
	l := m.Subscribe()
	m.DetachSource()
	if m.Status().SourceConnected {
		t.Fatal("source still marked connected")
	}
	m2, err := r.AttachSource("/a.mp3", StreamInfo{Name: "new"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m2 != m {
		t.Fatal("reconnecting source got a new mount; listeners would be lost")
	}
	m2.Write(frame(1, 8))
	recv(t, l)
	if got := m2.Status().Info.Name; got != "new" {
		t.Fatalf("stream info not updated: %q", got)
	}
}

func TestMountEndsAfterGraceWithoutSource(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 16, Grace: 20 * time.Millisecond})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, nil)
	l := m.Subscribe()
	m.DetachSource()
	expectClosed(t, l)
	deadline := time.Now().Add(time.Second)
	for r.Get("/a.mp3") != nil {
		if time.Now().After(deadline) {
			t.Fatal("mount not removed after grace")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTitleAndStatus(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 16, Grace: time.Minute})
	m, _ := r.AttachSource("/a.mp3", StreamInfo{Name: "ATC"}, nil)
	m.SetTitle("SAAB TOWER 118.805")
	m.Write(frame(1, 8))
	m.Subscribe()
	s := m.Status()
	if s.Mount != "/a.mp3" || s.Title != "SAAB TOWER 118.805" || s.Listeners != 1 || !s.SourceConnected || s.BytesIn != 8 || s.Info.Name != "ATC" {
		t.Fatalf("unexpected status %+v", s)
	}
	if list := r.List(); len(list) != 1 || list[0].Mount != "/a.mp3" {
		t.Fatalf("unexpected list %+v", list)
	}
}

type closeRecorder struct{ closed bool }

func (c *closeRecorder) Close() error { c.closed = true; return nil }

func TestShutdownClosesSourcesAndListeners(t *testing.T) {
	r := NewRegistry(Options{BurstBytes: 16, Grace: time.Minute})
	src := &closeRecorder{}
	m, _ := r.AttachSource("/a.mp3", StreamInfo{}, src)
	l := m.Subscribe()
	r.Shutdown()
	if !src.closed {
		t.Fatal("source connection not closed")
	}
	expectClosed(t, l)
}
