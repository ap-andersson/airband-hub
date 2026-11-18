// Package airband reads rtl_airband's Prometheus /metrics endpoint and turns the
// counters into live state for the dashboard: levels, squelch, recent transmissions.
package airband

import (
	"fmt"
	"io"
	"sort"
	"strconv"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Bucket is one cumulative histogram bucket.
type Bucket struct {
	UpperBound float64
	Count      uint64
}

// FreqSample is one scanned or monitored frequency, as rtl_airband reports it.
type FreqSample struct {
	Freq        string // MHz as rtl_airband formats it, e.g. "129.800"; the identity
	Label       string
	SignalDBFS  float64
	NoiseDBFS   float64
	SquelchDBFS float64
	Open        bool
	TxCount     uint64  // completed transmissions
	TxSeconds   float64 // their total duration
	SNRBuckets  []Bucket
}

// Sample is one scrape.
type Sample struct {
	Version   string
	Freqs     []FreqSample // sorted by frequency
	Samples   uint64       // I/Q samples read, summed over 8-bit devices
	Clipped   uint64
	PowerSum  float64
	Overflows uint64
}

// Parse reads the Prometheus text format rtl_airband writes.
func Parse(r io.Reader) (Sample, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return Sample{}, fmt.Errorf("parse metrics: %w", err)
	}

	var s Sample
	freqs := map[string]*FreqSample{}
	freq := func(m *dto.Metric) *FreqSample {
		f := labelValue(m, "freq")
		if f == "" {
			return nil
		}
		fs := freqs[f]
		if fs == nil {
			fs = &FreqSample{Freq: f, Label: labelValue(m, "label")}
			freqs[f] = fs
		}
		return fs
	}
	gauge := func(name string, set func(*FreqSample, float64)) {
		for _, m := range metrics(families, name) {
			if fs := freq(m); fs != nil {
				set(fs, value(m))
			}
		}
	}

	gauge("channel_dbfs_signal_level", func(f *FreqSample, v float64) { f.SignalDBFS = v })
	gauge("channel_dbfs_noise_level", func(f *FreqSample, v float64) { f.NoiseDBFS = v })
	gauge("channel_dbfs_squelch_level", func(f *FreqSample, v float64) { f.SquelchDBFS = v })
	gauge("channel_squelch_open", func(f *FreqSample, v float64) { f.Open = v != 0 })
	for _, m := range metrics(families, "channel_transmission_duration_seconds") {
		if fs, h := freq(m), m.GetHistogram(); fs != nil && h != nil {
			fs.TxCount = h.GetSampleCount()
			fs.TxSeconds = h.GetSampleSum()
		}
	}
	for _, m := range metrics(families, "channel_transmission_snr_db") {
		if fs, h := freq(m), m.GetHistogram(); fs != nil && h != nil {
			for _, b := range h.GetBucket() {
				fs.SNRBuckets = append(fs.SNRBuckets, Bucket{UpperBound: b.GetUpperBound(), Count: b.GetCumulativeCount()})
			}
		}
	}

	for _, m := range metrics(families, "device_samples_total") {
		s.Samples += uint64(value(m))
	}
	for _, m := range metrics(families, "device_clipped_samples_total") {
		s.Clipped += uint64(value(m))
	}
	for _, m := range metrics(families, "device_input_power_sum_total") {
		s.PowerSum += value(m)
	}
	for _, m := range metrics(families, "buffer_overflow_count") {
		s.Overflows += uint64(value(m))
	}
	for _, m := range metrics(families, "rtl_airband_build_info") {
		s.Version = labelValue(m, "version")
	}

	for _, fs := range freqs {
		s.Freqs = append(s.Freqs, *fs)
	}
	sort.Slice(s.Freqs, func(i, j int) bool { return mhz(s.Freqs[i].Freq) < mhz(s.Freqs[j].Freq) })
	return s, nil
}

func metrics(families map[string]*dto.MetricFamily, name string) []*dto.Metric {
	if f := families[name]; f != nil {
		return f.GetMetric()
	}
	return nil
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func value(m *dto.Metric) float64 {
	switch {
	case m.Gauge != nil:
		return m.GetGauge().GetValue()
	case m.Counter != nil:
		return m.GetCounter().GetValue()
	case m.Untyped != nil:
		return m.GetUntyped().GetValue()
	}
	return 0
}

func mhz(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
