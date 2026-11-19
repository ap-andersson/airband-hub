package airband

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func snrBuckets(counts ...uint64) []Bucket {
	// bounds 3, 6, 9, +Inf; counts are cumulative
	bounds := []float64{3, 6, 9, math.Inf(1)}
	b := make([]Bucket, len(counts))
	for i, c := range counts {
		b[i] = Bucket{UpperBound: bounds[i], Count: c}
	}
	return b
}

func sample(txCount uint64, txSeconds float64, snr []Bucket, open bool) Sample {
	return Sample{
		Version: "v7.0.1",
		Freqs: []FreqSample{{
			Freq: "129.800", Label: "MALMEN TOWER 129.800",
			SignalDBFS: -40, NoiseDBFS: -47, SquelchDBFS: -29,
			Open: open, TxCount: txCount, TxSeconds: txSeconds, SNRBuckets: snr,
		}},
	}
}

func TestFirstSampleCreatesNoTransmissions(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(5, 20, snrBuckets(0, 1, 5, 5), false), t0)
	st := tr.State()
	if len(st.Recent) != 0 || st.Frequencies[0].TxLastHour != 0 {
		t.Fatalf("history before the hub started was counted: %+v", st)
	}
	if !st.Receiver.OK || st.Receiver.Version != "v7.0.1" {
		t.Fatalf("receiver %+v", st.Receiver)
	}
}

func TestOneNewTransmissionHasExactDurationAndSNRRange(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(5, 20, snrBuckets(0, 1, 5, 5), false), t0)
	tr.Update(sample(6, 23.5, snrBuckets(0, 1, 6, 6), false), t0.Add(time.Second))
	st := tr.State()
	if len(st.Recent) != 1 {
		t.Fatalf("got %d transmissions", len(st.Recent))
	}
	tx := st.Recent[0]
	if tx.Freq != "129.800" || tx.Label != "MALMEN TOWER 129.800" || tx.DurationSeconds != 3.5 || tx.Approximate {
		t.Fatalf("unexpected %+v", tx)
	}
	if tx.SNRMinDB != 6 || tx.SNRMaxDB != 9 {
		t.Fatalf("SNR range %v..%v, want 6..9", tx.SNRMinDB, tx.SNRMaxDB)
	}
	if !tx.Start.Equal(t0.Add(time.Second).Add(-3500 * time.Millisecond)) {
		t.Fatalf("start %v", tx.Start)
	}
	if f := st.Frequencies[0]; f.TxLastHour != 1 || f.AirtimeLastHourSeconds != 3.5 {
		t.Fatalf("hour stats %+v", f)
	}
}

func TestSeveralTransmissionsInOneScrapeAreApproximate(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(5, 20, snrBuckets(0, 1, 5, 5), false), t0)
	tr.Update(sample(7, 24, snrBuckets(0, 1, 7, 7), false), t0.Add(time.Second))
	st := tr.State()
	if len(st.Recent) != 2 || !st.Recent[0].Approximate || st.Recent[0].DurationSeconds != 2 {
		t.Fatalf("unexpected %+v", st.Recent)
	}
}

func TestCounterResetIsNotATransmission(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(50, 200, snrBuckets(0, 10, 50, 50), false), t0)
	tr.Update(sample(0, 0, snrBuckets(0, 0, 0, 0), false), t0.Add(time.Second)) // rtl_airband restarted
	tr.Update(sample(1, 2, snrBuckets(0, 0, 1, 1), false), t0.Add(2*time.Second))
	if n := len(tr.State().Recent); n != 1 {
		t.Fatalf("got %d transmissions, want 1 (only the one after the restart)", n)
	}
}

func TestRecentIsNewestFirstAndHourWindowSlides(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(0, 0, snrBuckets(0, 0, 0, 0), false), t0)
	tr.Update(sample(1, 1, snrBuckets(0, 0, 1, 1), false), t0.Add(time.Minute))
	tr.Update(sample(2, 3, snrBuckets(0, 0, 2, 2), false), t0.Add(30*time.Minute))
	st := tr.State()
	if st.Recent[0].DurationSeconds != 2 || st.Recent[1].DurationSeconds != 1 {
		t.Fatalf("not newest first: %+v", st.Recent)
	}
	tr.Update(sample(2, 3, snrBuckets(0, 0, 2, 2), false), t0.Add(70*time.Minute))
	if f := tr.State().Frequencies[0]; f.TxLastHour != 1 || f.AirtimeLastHourSeconds != 2 {
		t.Fatalf("hour window not sliding: %+v", f)
	}
}

func TestOpenSquelchAndLevelsPassThrough(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(0, 0, nil, true), t0)
	f := tr.State().Frequencies[0]
	if !f.Open || f.SignalDBFS != -40 || f.NoiseDBFS != -47 || f.SquelchDBFS != -29 {
		t.Fatalf("unexpected %+v", f)
	}
}

func TestDeviceRates(t *testing.T) {
	tr := NewTracker()
	s := sample(0, 0, nil, false)
	s.Samples, s.Clipped, s.PowerSum = 1_000_000, 0, 0
	tr.Update(s, t0)
	if r := tr.State().Receiver; r.ClippingRatio != nil || r.InputDBFS != nil {
		t.Fatal("rates need two samples")
	}
	s.Samples, s.Clipped, s.PowerSum = 3_000_000, 20, 2_000_000*0.001 // mean power 0.001 = -30 dBFS
	tr.Update(s, t0.Add(time.Second))
	r := tr.State().Receiver
	if r.ClippingRatio == nil || *r.ClippingRatio != 20.0/2_000_000 {
		t.Fatalf("clipping %v", r.ClippingRatio)
	}
	if r.InputDBFS == nil || math.Abs(*r.InputDBFS-(-30)) > 1e-9 {
		t.Fatalf("input power %v", r.InputDBFS)
	}
}

func TestFailureKeepsLastLevelsButMarksReceiverDown(t *testing.T) {
	tr := NewTracker()
	tr.Update(sample(0, 0, nil, true), t0)
	tr.Fail("connection refused", t0.Add(time.Second))
	st := tr.State()
	if st.Receiver.OK || st.Receiver.Error != "connection refused" {
		t.Fatalf("receiver %+v", st.Receiver)
	}
	if len(st.Frequencies) != 1 || st.Frequencies[0].Open {
		t.Fatalf("stale squelch state shown as open: %+v", st.Frequencies)
	}
}

func TestRecordedTransmissionsAreHandedOnAndRestored(t *testing.T) {
	tr := NewTracker()
	var got []Transmission
	tr.OnRecord = func(added []Transmission) { got = append(got, added...) }
	tr.Update(sample(5, 20, snrBuckets(0, 1, 5, 5), false), t0)
	tr.Update(sample(6, 23.5, snrBuckets(0, 1, 6, 6), false), t0.Add(time.Second))
	if len(got) != 1 || got[0].DurationSeconds != 3.5 {
		t.Fatalf("handed on %+v", got)
	}

	// a restarted hub gets them back, older than what it records itself
	tr2 := NewTracker()
	tr2.Update(sample(6, 23.5, snrBuckets(0, 1, 6, 6), false), time.Now())
	tr2.Update(sample(7, 25, snrBuckets(0, 1, 7, 7), false), time.Now())
	old := got[0]
	old.Start, old.End = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour+3*time.Second)
	tr2.Restore([]Transmission{old})
	recent := tr2.State().Recent
	if len(recent) != 2 || !recent[1].End.Equal(old.End) {
		t.Fatalf("recent after restore %+v", recent)
	}
}
