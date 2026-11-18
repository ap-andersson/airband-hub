package mp3

import (
	"math"
	"os"
	"testing"
	"time"
)

// Set AIRBAND_REAL_MP3 to one of rtl_airband's hourly recordings to check the index and the
// decoder agree on it. Real recordings are not committed.
func TestRealRecording(t *testing.T) {
	path := os.Getenv("AIRBAND_REAL_MP3")
	if path == "" {
		t.Skip("AIRBAND_REAL_MP3 not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ix := ScanFrames(data)
	scanned := time.Since(start)
	start = time.Now()
	a, err := Analyze(data, 10)
	if err != nil {
		t.Fatal(err)
	}
	decoded := time.Since(start)
	tx := Transmissions(a.Peaks, a.PeaksPerSecond)
	var airtime float64
	for _, s := range tx {
		airtime += s.End - s.Start
	}
	t.Logf("%d frames, index %.3f s vs decoded %.3f s; index %v, decode %v; %d transmissions, %.0f s airtime",
		len(ix.Frames), ix.Duration(), a.Duration, scanned, decoded, len(tx), airtime)
	if math.Abs(ix.Duration()-a.Duration) > 0.1 {
		t.Fatalf("index and decoder disagree: %.3f vs %.3f s", ix.Duration(), a.Duration)
	}
}
