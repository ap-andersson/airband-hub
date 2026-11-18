package mp3

import (
	"math"
	"os"
	"testing"
)

// testdata/tones.mp3 is synthetic, made with lame -V 7 from 60 s of 8 kHz mono: silence with a
// loud burst at 10.0-13.0 s and a quiet one (-26 dBFS) at 30.0-31.5 s. 8 kHz makes it MPEG-2.5,
// like rtl_airband's recordings, and lame wrote a Xing header frame first.
func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/tones.mp3")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScanFramesSkipsXingAndCountsAudio(t *testing.T) {
	ix := ScanFrames(fixture(t))
	if ix.SampleRate != 8000 {
		t.Fatalf("sample rate %d", ix.SampleRate)
	}
	if len(ix.Frames) != 837 {
		t.Fatalf("got %d frames", len(ix.Frames))
	}
	if ix.Frames[0].Samples != 0 {
		t.Fatal("the Xing header frame must not count as audio")
	}
	if d := ix.Duration(); math.Abs(d-60.2) > 0.2 {
		t.Fatalf("duration %.2f", d)
	}
}

func TestFrameAtIsExact(t *testing.T) {
	ix := ScanFrames(fixture(t))
	for _, want := range []float64{0, 10, 30.05, 59.9} {
		i, start := ix.FrameAt(want)
		if start > want || want-start >= 576.0/8000 {
			t.Errorf("FrameAt(%v) starts at %v", want, start)
		}
		if ix.Frames[i].Samples == 0 {
			t.Errorf("FrameAt(%v) returned a non-audio frame", want)
		}
	}
	if i, _ := ix.FrameAt(1e6); i != len(ix.Frames)-1 {
		t.Errorf("past the end should clamp to the last frame, got %d", i)
	}
}

func TestScanFramesSurvivesAppendedSessionsAndTruncation(t *testing.T) {
	one := fixture(t)
	// rtl_airband appends after a restart: a second encoder session, with its own Xing frame
	two := append(append([]byte{}, one...), one...)
	two = two[:len(two)-100] // the current hour's file can end mid-frame
	ix := ScanFrames(two)
	if d := ix.Duration(); math.Abs(d-120.4) > 0.4 {
		t.Fatalf("duration %.2f", d)
	}
	junk := append([]byte("ID3 not really a tag, then garbage"), one...)
	if ix := ScanFrames(junk); len(ix.Frames) != 837 {
		t.Fatalf("resync after garbage: %d frames", len(ix.Frames))
	}
}

func TestAnalyzePeaksAndTransmissions(t *testing.T) {
	a, err := Analyze(fixture(t), 10)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Duration-60.2) > 0.2 || a.SampleRate != 8000 || a.PeaksPerSecond != 10 {
		t.Fatalf("unexpected %+v", a.Duration)
	}
	if n := len(a.Peaks); n < 600 || n > 605 {
		t.Fatalf("got %d peaks for 60 s at 10/s", n)
	}
	level := func(sec float64) uint8 { return a.Peaks[int(sec*10)] }
	if level(5) != 0 || level(20) != 0 {
		t.Errorf("silence should be 0, got %d and %d", level(5), level(20))
	}
	loud, quiet := level(11.5), level(30.7)
	if loud < 200 || quiet < 60 || quiet >= loud {
		t.Errorf("loud %d, quiet %d", loud, quiet)
	}

	tx := Transmissions(a.Peaks, a.PeaksPerSecond)
	if len(tx) != 2 {
		t.Fatalf("got %d transmissions: %+v", len(tx), tx)
	}
	// lame delays the audio by ~1100 samples (0.14 s) and the end gains up to one bucket;
	// playback uses the same decoded timeline, so markers match what is heard
	near := func(got, want float64) bool { return got-want >= -0.05 && got-want <= 0.35 }
	if !near(tx[0].Start, 10) || !near(tx[0].End, 13) || !near(tx[1].Start, 30) || !near(tx[1].End, 31.5) {
		t.Fatalf("transmissions %+v", tx)
	}
}

func TestTransmissionsMergesShortGapsAndDropsBlips(t *testing.T) {
	p := make([]uint8, 200) // 20 s at 10/s
	for i := 20; i < 40; i++ {
		p[i] = 150 // 2.0-4.0 s
	}
	for i := 45; i < 60; i++ {
		p[i] = 150 // 4.5-6.0 s: a 0.5 s pause in the same transmission
	}
	p[100] = 150 // a single 0.1 s blip
	tx := Transmissions(p, 10)
	if len(tx) != 1 || tx[0].Start != 2 || tx[0].End != 6 {
		t.Fatalf("got %+v", tx)
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	a, err := Analyze(nil, 10)
	if err != nil || a.Duration != 0 || len(a.Peaks) != 0 {
		t.Fatalf("got %+v, %v", a, err)
	}
}
