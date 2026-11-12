package content_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func splitBlockRowsChunks(data []byte, size int) [][]byte {
	var chunks [][]byte
	for len(data) > 0 {
		n := min(size, len(data))
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return chunks
}

// BenchmarkBlockRowsText compares identical physical text and encoded byte counts
// with hard line breaks and with one wrapped logical line. Run with -benchtime=1x
// when comparing implementations that copy the growing logical line per row.
func BenchmarkBlockRowsText(b *testing.B) {
	for _, size := range []int{256 << 10, 4 << 20} {
		for _, wrapped := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/wrapped=%t", size, wrapped), func(b *testing.B) {
				var body bytes.Buffer
				for offset := 0; offset < size; offset += 80 {
					text := strings.Repeat("x", min(80, size-offset))
					fmt.Fprintf(&body, "{\"from\":%d,\"row\":{\"text\":%q,\"continuation\":%5t}}\n", offset/80, text, wrapped && offset > 0)
				}
				chunks := splitBlockRowsChunks(body.Bytes(), 64<<10)
				wantLen := size
				if !wrapped {
					wantLen += (size - 1) / 80
				}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for range b.N {
					got, err := content.BlockRowsTextAfter(chunks, 0)
					if err != nil || len(got) != wantLen {
						b.Fatalf("BlockRowsTextAfter length = %d, err = %v; want %d", len(got), err, wantLen)
					}
				}
			})
		}
	}
}

func TestBlockRowsTextAfter_ContinuationAcrossChunks(t *testing.T) {
	// A retained continuation, empty logical lines, and byte-sized chunks
	// exercise separators independently of text length and JSON boundaries.
	body := "{\"row\":{\"text\":\"\",\"continuation\":true}}\n" +
		"{\"row\":{\"text\":\"\"}}\n" +
		"{\"row\":{\"text\":\"A中é  \",\"marks\":[[1,1,2],[2,2,1]],\"continuation\":true}}\n" +
		"{\"row\":{\"text\":\"B\",\"marks\":[[0,0,0]],\"continuation\":true}}\n" +
		"{\"row\":{\"text\":\"\"}}\n" +
		"{\"row\":{\"text\":\"tail\"}}\n" +
		"{\"row\":{\"text\":\"\"}}\n"
	chunks := [][]byte{nil}
	for _, value := range []byte(body) {
		chunks = append(chunks, []byte{value}, nil)
	}
	got, err := content.BlockRowsTextAfter(chunks, 0)
	if err != nil || got != "\nA中é B\n\ntail\n" {
		t.Fatalf("BlockRowsTextAfter = %q, %v; want %q", got, err, "\nA中é B\n\ntail\n")
	}
}

func TestBlockRowsTextAfter_SkipsRowsBelowOutputStart(t *testing.T) {
	// The rows below outputStartRow are the command's own echo and redraw
	// (nocx-4y82c.1.11). The reader decodes through them without emitting
	// them, and a continuation either side of the skip must not gain or lose
	// a separator (nocx-4y82c.1.23).
	const echo = "{\"from\":0,\"row\":{\"text\":\"$ make\"}}\n" +
		"{\"from\":1,\"row\":{\"text\":\"$ make all\"}}\n"
	for _, tc := range []struct {
		name     string
		body     string
		startRow uint64
		want     string
	}{
		{
			"keeps every line at zero", echo +
				"{\"from\":2,\"row\":{\"text\":\"one\"}}\n" +
				"{\"from\":3,\"row\":{\"text\":\"two\",\"continuation\":true}}\n" +
				"{\"from\":4,\"row\":{\"text\":\"three\"}}\n", 0,
			"$ make\n$ make all\nonetwo\nthree",
		},
		{
			"drops the echo and joins the wrapped output line", echo +
				"{\"from\":2,\"row\":{\"text\":\"one\"}}\n" +
				"{\"from\":3,\"row\":{\"text\":\"two\",\"continuation\":true}}\n" +
				"{\"from\":4,\"row\":{\"text\":\"three\"}}\n", 2,
			"onetwo\nthree",
		},
		{
			"a continuation at the output start keeps its own line",
			"{\"from\":0,\"row\":{\"text\":\"echo\"}}\n" +
				"{\"from\":1,\"row\":{\"text\":\"cont\",\"continuation\":true}}\n" +
				"{\"from\":2,\"row\":{\"text\":\"body\"}}\n", 1,
			"cont\nbody",
		},
		{"an output start past the last row reads nothing", echo, 9, ""},
	} {
		for _, chunkSize := range []int{1, 7, 1 << 20} {
			t.Run(fmt.Sprintf("%s/chunk=%d", tc.name, chunkSize), func(t *testing.T) {
				chunks := splitBlockRowsChunks([]byte(tc.body), chunkSize)
				got, err := content.BlockRowsTextAfter(chunks, tc.startRow)
				if err != nil || got != tc.want {
					t.Fatalf("BlockRowsTextAfter(startRow=%d) = %q, %v; want %q", tc.startRow, got, err, tc.want)
				}
			})
		}
	}
}

func TestBlockRowsTextAfter_EmptyAndInvalidChunks(t *testing.T) {
	for _, chunks := range [][][]byte{nil, {nil, {}, []byte(" \n")}} {
		got, err := content.BlockRowsTextAfter(chunks, 0)
		if err != nil || got != "" {
			t.Fatalf("empty BlockRowsTextAfter = %q, %v", got, err)
		}
	}
	for _, invalid := range []string{
		`{"row":`,
		`{"row":{"text":"x","marks":[[0,1,1],[0,1,1]]}}`,
		`{"row":{"text":"","marks":[[0,2,1]]}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			chunks := [][]byte{[]byte("{\"row\":{\"text\":\"valid\"}}\n"), []byte(invalid)}
			got, err := content.BlockRowsTextAfter(chunks, 0)
			if err == nil || got != "" {
				t.Fatalf("invalid BlockRowsTextAfter = %q, %v; want empty text and error", got, err)
			}
		})
	}
}
