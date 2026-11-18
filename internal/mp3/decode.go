package mp3

/*
#cgo CFLAGS: -O2
#cgo LDFLAGS: -lm
#define MINIMP3_IMPLEMENTATION
#define MINIMP3_ONLY_MP3
#include "minimp3.h"
*/
import "C"

import (
	"math"
	"unsafe"
)

// Analysis is the waveform of one recording.
type Analysis struct {
	SampleRate     int
	Duration       float64
	PeaksPerSecond int
	// Peaks holds the loudest sample in each 1/PeaksPerSecond s, scaled from -60..0 dBFS to
	// 1..255; 0 is quieter than -60 dBFS. A log scale keeps distant transmissions visible.
	Peaks []uint8
}

const floorDBFS = -60.0

func scale(peak int) uint8 {
	if peak <= 0 {
		return 0
	}
	db := 20 * math.Log10(float64(peak)/32768)
	if db < floorDBFS {
		return 0
	}
	return uint8(1 + math.Round((db-floorDBFS)/-floorDBFS*254))
}

// Analyze decodes data (minimp3, which unlike pure-Go decoders handles MPEG-2.5, i.e. 8 kHz)
// and returns its peak envelope. Mono is assumed; for stereo the louder channel counts.
func Analyze(data []byte, peaksPerSecond int) (Analysis, error) {
	a := Analysis{PeaksPerSecond: peaksPerSecond}
	if len(data) == 0 {
		return a, nil
	}
	var dec C.mp3dec_t
	C.mp3dec_init(&dec)
	pcm := make([]C.mp3d_sample_t, C.MINIMP3_MAX_SAMPLES_PER_FRAME)
	var info C.mp3dec_frame_info_t

	var bucketLen, inBucket, peak int
	var total int64
	for off := 0; off < len(data); {
		n := int(C.mp3dec_decode_frame(&dec, (*C.uint8_t)(unsafe.Pointer(&data[off])), C.int(len(data)-off), &pcm[0], &info))
		if info.frame_bytes == 0 {
			break
		}
		frame := data[off : off+int(info.frame_bytes)]
		off += int(info.frame_bytes)
		// minimp3 plays a Xing/Info header frame as silence, but players and ScanFrames skip
		// it; skipping it here too keeps the waveform on the same timeline as playback
		if n == 0 || (len(frame) >= 4 && isTagFrame(frame)) {
			continue
		}
		if a.SampleRate == 0 {
			a.SampleRate = int(info.hz)
			bucketLen = max(1, a.SampleRate/peaksPerSecond)
		}
		ch := max(1, int(info.channels))
		for i := 0; i < n*ch; i += ch {
			for c := 0; c < ch; c++ {
				v := int(pcm[i+c])
				if v < 0 {
					v = -v
				}
				peak = max(peak, v)
			}
			if inBucket++; inBucket == bucketLen {
				a.Peaks = append(a.Peaks, scale(peak))
				inBucket, peak = 0, 0
			}
		}
		total += int64(n)
	}
	if inBucket > 0 {
		a.Peaks = append(a.Peaks, scale(peak))
	}
	if a.SampleRate > 0 {
		a.Duration = float64(total) / float64(a.SampleRate)
	}
	return a, nil
}

// Segment is a stretch of a recording, in seconds from its start.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

const (
	activeLevel = 1   // anything above -60 dBFS; rtl_airband writes digital silence while squelch is closed
	mergeGap    = 1.5 // pauses shorter than this stay within one transmission
	minLength   = 0.3 // shorter blips are not transmissions
)

// Transmissions finds where the recording has sound.
func Transmissions(peaks []uint8, peaksPerSecond int) []Segment {
	out := []Segment{}
	step := 1 / float64(peaksPerSecond)
	var cur *Segment
	for i, p := range peaks {
		if p < activeLevel {
			continue
		}
		t := float64(i) * step
		if cur != nil && t-cur.End <= mergeGap {
			cur.End = t + step
			continue
		}
		if cur != nil && cur.End-cur.Start >= minLength {
			out = append(out, *cur)
		}
		cur = &Segment{Start: t, End: t + step}
	}
	if cur != nil && cur.End-cur.Start >= minLength {
		out = append(out, *cur)
	}
	for i := range out { // tidy float noise from the step arithmetic
		out[i].Start = math.Round(out[i].Start*1000) / 1000
		out[i].End = math.Round(out[i].End*1000) / 1000
	}
	return out
}
