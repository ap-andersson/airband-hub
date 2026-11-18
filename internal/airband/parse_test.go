package airband

import (
	"os"
	"strings"
	"testing"
)

func TestParseRealMetrics(t *testing.T) {
	f, err := os.Open("testdata/metrics.txt") // scraped from the Pi, 4 scanned frequencies
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.Version, "v7.") {
		t.Errorf("version %q", s.Version)
	}
	want := []string{"118.805", "129.800", "133.105", "135.855"}
	if len(s.Freqs) != len(want) {
		t.Fatalf("got %d frequencies", len(s.Freqs))
	}
	for i, fs := range s.Freqs {
		if fs.Freq != want[i] {
			t.Errorf("freq %d = %s, want %s", i, fs.Freq, want[i])
		}
		if fs.Label == "" || fs.NoiseDBFS > -20 || fs.NoiseDBFS < -80 || fs.SquelchDBFS <= fs.NoiseDBFS {
			t.Errorf("implausible %+v", fs)
		}
		if len(fs.SNRBuckets) != 21 { // 20 bounds + +Inf
			t.Errorf("%s: %d SNR buckets", fs.Freq, len(fs.SNRBuckets))
		}
	}
	if s.Freqs[1].Label != "MALMEN TOWER 129.800" {
		t.Errorf("label %q", s.Freqs[1].Label)
	}
	if s.Samples == 0 {
		t.Error("no device samples")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse(strings.NewReader("this is { not metrics\n")); err == nil {
		t.Fatal("expected an error")
	}
}
