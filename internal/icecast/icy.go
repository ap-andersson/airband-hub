package icecast

import (
	"io"
	"strings"
)

// icyMetaint is how many audio bytes go between metadata blocks. At rtl_airband's 48 kbit/s
// (6000 B/s) a new title reaches the player within about 1.4 s.
const icyMetaint = 8192

// icyWriter interleaves SHOUTcast/Icecast "ICY" metadata into the audio, for players such as
// VLC that ask for it with "Icy-MetaData: 1": after every metaint bytes of audio comes one
// length byte (in 16-byte units) and the padded "StreamTitle='...';" text. An unchanged title
// is sent as length 0.
type icyWriter struct {
	w        io.Writer
	metaint  int
	left     int // audio bytes until the next metadata block
	title    func() string
	lastSent string
	sentOnce bool
}

func newICYWriter(w io.Writer, metaint int, title func() string) *icyWriter {
	return &icyWriter{w: w, metaint: metaint, left: metaint, title: title}
}

func (iw *icyWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), iw.left)
		if _, err := iw.w.Write(p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
		iw.left -= n
		if iw.left == 0 {
			block := []byte{0}
			if t := iw.title(); t != iw.lastSent || (!iw.sentOnce && t != "") {
				block = icyBlock(t)
				iw.lastSent, iw.sentOnce = t, true
			}
			if _, err := iw.w.Write(block); err != nil {
				return written, err
			}
			iw.left = iw.metaint
		}
	}
	return written, nil
}

// icyBlock encodes a title as an ICY metadata block. Quotes would end the value early and NULs
// would end it in some players, so both are dropped; the text is capped at what fits.
func icyBlock(title string) []byte {
	title = strings.Map(func(r rune) rune {
		if r == '\'' || r == 0 {
			return -1
		}
		return r
	}, title)
	const maxText = 255*16 - len("StreamTitle='';")
	if len(title) > maxText {
		title = title[:maxText]
	}
	text := "StreamTitle='" + title + "';"
	blocks := (len(text) + 15) / 16
	out := make([]byte, 1+blocks*16)
	out[0] = byte(blocks)
	copy(out[1:], text)
	return out
}
