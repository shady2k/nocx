package apisend

// The bound is the whole point of this file: a response is captured
// STREAMED, stopping at the ceiling plus one byte, never buffered whole.
// That is `files.read`'s property (contracts/files.read.schema.json:
// "reads at most the effective limit plus one byte and never the whole
// file, so the memory guard holds for a 40 GB file"), copied here rather
// than re-derived — design §12.3. A cap applied AFTER reading is not a cap,
// so the assertion is on allocation, not on the returned length.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/apicoll"
)

// TestCapture_StopsAtTheCeilingWithoutBufferingTheBody streams 64 MiB at a
// sender whose ceiling is 2 MiB and asserts that the process never allocated
// enough to have held the body.
func TestCapture_StopsAtTheCeilingWithoutBufferingTheBody(t *testing.T) {
	var written atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 64; i++ {
			if _, err := w.Write(chunk); err != nil {
				return // the client stopped reading: that is the pass condition
			}
			written.Add(1)
		}
	}))
	defer srv.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ex, err := New().Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
	runtime.ReadMemStats(&after)

	got := answered(t, ex, err)
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	if int64(len(got.Text)) > ceiling {
		t.Errorf("len = %d, want <= %d", len(got.Text), ceiling)
	}
	if got.Size != ceiling {
		t.Errorf("Size = %d, want the ceiling %d", got.Size, ceiling)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 8<<20 {
		t.Errorf("allocated %d bytes — the body was buffered; a cap applied AFTER "+
			"reading is not a cap (design §12.3)", d)
	}
	// The second proof, independent of the allocator: the server never got
	// to write its 64 MiB, because the reader stopped. A cap applied after
	// reading would have let every chunk through.
	if got := written.Load(); got >= 64 {
		t.Errorf("the server wrote all %d MiB — the whole body was read", got)
	}
}

// TestCapture_TheCeilingCanOnlyBeLowered: 2 MiB is inherited from
// files.read, not chosen here, and the parameter is a floor-ward knob only.
func TestCapture_TheCeilingCanOnlyBeLowered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 4; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	cases := []struct {
		name    string
		maxByte int64
		want    int64
	}{
		{"lowered", 1024, 1024},
		{"zero means the ceiling", 0, ceiling},
		{"negative means the ceiling", -1, ceiling},
		{"above the ceiling is still the ceiling", 1 << 30, ceiling},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ex, err := newBounded(c.maxByte).Send(context.Background(),
				apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
			got := answered(t, ex, err)
			if !got.Truncated {
				t.Error("Truncated = false, want true")
			}
			if got.Size != c.want || int64(len(got.Text)) != c.want {
				t.Errorf("Size = %d, len(Text) = %d, want %d", got.Size, len(got.Text), c.want)
			}
		})
	}
}

// TestCapture_ThreeDistinctStates: truncated, binary and lossy are three
// facts with three meanings. A binary body carries EMPTY text — never
// base64, which would be the bulk payload in JSON that AD-1 prohibits.
func TestCapture_ThreeDistinctStates(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want capture
	}{
		{
			"plain text is none of the three",
			[]byte("hello, world"),
			capture{Text: "hello, world", Size: 12},
		},
		{
			"a NUL makes it binary, and binary carries no text",
			[]byte{'P', 'N', 'G', 0x00, 0x01, 0x02},
			capture{Binary: true, Text: "", Size: 6},
		},
		{
			"invalid UTF-8 without a NUL is lossy text, not binary",
			[]byte{'c', 'a', 'f', 0xE9}, // latin-1 "café"
			capture{Lossy: true, Text: "caf�", Size: 4},
		},
		{
			"an empty body is none of the three",
			[]byte{},
			capture{Text: "", Size: 0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := c.body
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			ex, err := New().Send(context.Background(),
				apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
			got := answered(t, ex, err)
			if got.Binary != c.want.Binary || got.Lossy != c.want.Lossy || got.Truncated != c.want.Truncated {
				t.Errorf("binary/lossy/truncated = %v/%v/%v, want %v/%v/%v",
					got.Binary, got.Lossy, got.Truncated, c.want.Binary, c.want.Lossy, c.want.Truncated)
			}
			if got.Text != c.want.Text {
				t.Errorf("Text = %q, want %q", got.Text, c.want.Text)
			}
			if got.Size != c.want.Size {
				t.Errorf("Size = %d, want %d", got.Size, c.want.Size)
			}
			if got.Binary && got.Text != "" {
				t.Errorf("a binary body carried text %q — the run says "+
					"\"binary body, N bytes\", never base64", got.Text)
			}
			if !utf8.ValidString(got.Text) {
				t.Errorf("Text is not valid UTF-8: %q", got.Text)
			}
		})
	}
}

// TestCapture_TruncatedTextRemainsValidUTF8: the ceiling can fall in the
// middle of a multi-byte rune, and the decoded text is still valid UTF-8 —
// the contract says "always valid UTF-8" with no exception for the cut.
func TestCapture_TruncatedTextRemainsValidUTF8(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("é", 4096))
	}))
	defer srv.Close()

	// 9 bytes cuts inside the fifth "é" (2 bytes each).
	ex, err := newBounded(9).Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{})
	got := answered(t, ex, err)
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if !utf8.ValidString(got.Text) {
		t.Fatalf("Text = %q is not valid UTF-8 after the cut", got.Text)
	}
	if !got.Lossy {
		t.Error("Lossy = false — the bytes read ended mid-rune, and the text says so")
	}
}
