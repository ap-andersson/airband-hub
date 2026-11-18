// Package mp3 indexes and decodes the MP3 files rtl_airband records: an exact frame index
// for seeking, and a decoded peak envelope for the waveform view.
package mp3

import "bytes"

// Frame is one MPEG audio frame in a file.
type Frame struct {
	Offset  int64
	Size    int
	Samples int // 0 for a Xing/Info header frame, which holds no audio
}

// Index lists every frame, so a time maps to an exact byte offset. Seeking through the Xing
// table instead is up to 15 s off on rtl_airband's mostly-silent VBR files.
type Index struct {
	SampleRate int
	Frames     []Frame
	// FirstSample[i] is the number of audio samples before frame i
	FirstSample []int64
}

func (ix Index) Duration() float64 {
	if ix.SampleRate == 0 || len(ix.Frames) == 0 {
		return 0
	}
	last := len(ix.Frames) - 1
	return float64(ix.FirstSample[last]+int64(ix.Frames[last].Samples)) / float64(ix.SampleRate)
}

// FrameAt returns the audio frame playing at seconds, and the time that frame starts.
func (ix Index) FrameAt(seconds float64) (int, float64) {
	if len(ix.Frames) == 0 {
		return 0, 0
	}
	target := int64(seconds * float64(ix.SampleRate))
	lo, hi := 0, len(ix.Frames)-1
	for lo < hi { // last frame whose first sample is <= target
		mid := (lo + hi + 1) / 2
		if ix.FirstSample[mid] <= target {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo < len(ix.Frames)-1 && ix.Frames[lo].Samples == 0 {
		lo++
	}
	return lo, float64(ix.FirstSample[lo]) / float64(ix.SampleRate)
}

// Layer III bitrates in kbit/s by bitrate index.
var (
	bitratesV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	bitratesV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	rates      = map[int][3]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
)

type header struct {
	size, samples, rate int
}

// parseHeader reads a Layer III frame header (MPEG-1, 2 or 2.5).
func parseHeader(b []byte) (header, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return header{}, false
	}
	version := int(b[1]>>3) & 3 // 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5
	layer := int(b[1]>>1) & 3   // 1 = Layer III
	bri := int(b[2] >> 4)
	sri := int(b[2]>>2) & 3
	pad := int(b[2]>>1) & 1
	if version == 1 || layer != 1 || bri == 0 || bri == 15 || sri == 3 {
		return header{}, false
	}
	rate := rates[version][sri]
	if version == 3 {
		return header{size: 144*bitratesV1[bri]*1000/rate + pad, samples: 1152, rate: rate}, true
	}
	return header{size: 72*bitratesV2[bri]*1000/rate + pad, samples: 576, rate: rate}, true
}

// isTagFrame reports a Xing/Info (LAME) or VBRI header frame; they sit where audio would.
func isTagFrame(frame []byte) bool {
	n := min(len(frame), 48)
	return bytes.Contains(frame[:n], []byte("Xing")) || bytes.Contains(frame[:n], []byte("Info")) || bytes.Contains(frame[:n], []byte("VBRI"))
}

// ScanFrames walks the frame headers. It resynchronises after garbage and stops at a
// truncated last frame, so it works on files that are still being written and on files
// with several appended encoder sessions.
func ScanFrames(data []byte) Index {
	ix := Index{}
	var samples int64
	pos := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" && data[3] >= 2 && data[3] <= 4 && (data[6]|data[7]|data[8]|data[9])&0x80 == 0 {
		// ID3v2 tag: skip it; its size is four 7-bit bytes
		size := int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9])
		if 10+size < len(data) {
			pos = 10 + size
		}
	}
	for pos+4 <= len(data) {
		h, ok := parseHeader(data[pos:])
		if !ok || pos+h.size > len(data) {
			if ok { // truncated last frame
				break
			}
			pos++
			continue
		}
		// a sync word inside audio data can look like a header; require the next frame to line up too
		if next := pos + h.size; next+4 <= len(data) {
			if _, ok := parseHeader(data[next:]); !ok {
				pos++
				continue
			}
		}
		f := Frame{Offset: int64(pos), Size: h.size, Samples: h.samples}
		if isTagFrame(data[pos : pos+h.size]) {
			f.Samples = 0
		}
		if ix.SampleRate == 0 {
			ix.SampleRate = h.rate
		}
		ix.Frames = append(ix.Frames, f)
		ix.FirstSample = append(ix.FirstSample, samples)
		samples += int64(f.Samples)
		pos += h.size
	}
	return ix
}
