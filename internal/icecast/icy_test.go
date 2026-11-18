package icecast

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// readICY splits a stream with ICY metadata into its audio and its metadata blocks.
func readICY(t *testing.T, r io.Reader, metaint, audioBytes int) ([]byte, []string) {
	t.Helper()
	var audio []byte
	var metas []string
	for len(audio) < audioBytes {
		chunk := make([]byte, metaint)
		if _, err := io.ReadFull(r, chunk); err != nil {
			t.Fatal(err)
		}
		audio = append(audio, chunk...)
		var n [1]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			t.Fatal(err)
		}
		meta := make([]byte, int(n[0])*16)
		if _, err := io.ReadFull(r, meta); err != nil {
			t.Fatal(err)
		}
		metas = append(metas, string(bytes.TrimRight(meta, "\x00")))
	}
	return audio, metas
}

func TestICYWriterInterleavesTitleOnlyWhenItChanges(t *testing.T) {
	var out bytes.Buffer
	title := "MALMEN TOWER 129.800"
	w := newICYWriter(&out, 10, func() string { return title })
	w.Write([]byte("0123456789abcdefghij")) // two blocks of audio
	title = "SAAB TOWER 118.805"
	w.Write([]byte("klmnopqrst"))

	audio, metas := readICY(t, &out, 10, 30)
	if string(audio) != "0123456789abcdefghijklmnopqrst" {
		t.Fatalf("audio %q", audio)
	}
	want := []string{"StreamTitle='MALMEN TOWER 129.800';", "", "StreamTitle='SAAB TOWER 118.805';"}
	if len(metas) != 3 || metas[0] != want[0] || metas[1] != want[1] || metas[2] != want[2] {
		t.Fatalf("metadata %q", metas)
	}
}

func TestICYWriterSplitsWritesAcrossBlocks(t *testing.T) {
	var out bytes.Buffer
	w := newICYWriter(&out, 4, func() string { return "" })
	for _, s := range []string{"ab", "cdef", "g", "hijkl"} {
		if n, err := w.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	// no title yet: every block is followed by an empty metadata byte
	if got := out.String(); got != "abcd\x00efgh\x00ijkl\x00" {
		t.Fatalf("got %q", got)
	}
}

func TestICYTitleIsSanitised(t *testing.T) {
	block := icyBlock("O'Hare\x00 tower " + strings.Repeat("x", 5000))
	if len(block)%16 != 1 || int(block[0])*16 != len(block)-1 || len(block) > 1+255*16 {
		t.Fatalf("bad block length %d", len(block))
	}
	text := bytes.TrimRight(block[1:], "\x00")
	if !bytes.HasPrefix(text, []byte("StreamTitle='OHare tower x")) || bytes.IndexByte(text, 0) >= 0 || !bytes.HasSuffix(text, []byte("';")) {
		t.Fatalf("quote or NUL not removed, or not terminated: %q ... %q", text[:30], text[len(text)-5:])
	}
}

func TestListenerGetsICYMetadataWhenAskedFor(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	src := connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	reg.Get("/essl.mp3").SetTitle("OSTGOTA APPROACH 135.855")

	req, _ := http.NewRequest("GET", srv.URL+"/essl.mp3", nil)
	req.Header.Set("Icy-MetaData", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	metaint, err := strconv.Atoi(resp.Header.Get("icy-metaint"))
	if err != nil || metaint <= 0 {
		t.Fatalf("icy-metaint %q", resp.Header.Get("icy-metaint"))
	}
	go func() {
		chunk := append([]byte{0xFF, 0xE3}, bytes.Repeat([]byte{1}, 998)...)
		for i := 0; i < 3*metaint/1000+2; i++ {
			src.Write(chunk)
		}
	}()
	_, metas := readICY(t, resp.Body, metaint, metaint)
	if metas[0] != "StreamTitle='OSTGOTA APPROACH 135.855';" {
		t.Fatalf("metadata %q", metas)
	}
}

func TestListenerWithoutICYGetsPlainAudio(t *testing.T) {
	srv, reg := newServer(t, time.Minute)
	src := connectSource(t, srv, "/essl.mp3")
	waitFor(t, "mount", func() bool { return reg.Get("/essl.mp3") != nil })
	reg.Get("/essl.mp3").SetTitle("X")
	resp, err := http.Get(srv.URL + "/essl.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("icy-metaint") != "" {
		t.Fatal("icy-metaint sent to a client that did not ask")
	}
	payload := bytes.Repeat([]byte{7}, 20000)
	src.Write(payload)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(resp.Body, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("plain stream altered")
	}
}
