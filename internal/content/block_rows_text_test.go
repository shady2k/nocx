package content_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

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
				var chunks [][]byte
				for data := body.Bytes(); len(data) > 0; {
					n := min(64<<10, len(data))
					chunks = append(chunks, data[:n])
					data = data[n:]
				}
				wantLen := size
				if !wrapped {
					wantLen += (size - 1) / 80
				}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for range b.N {
					got, err := content.BlockRowsText(chunks)
					if err != nil || len(got) != wantLen {
						b.Fatalf("BlockRowsText length = %d, err = %v; want %d", len(got), err, wantLen)
					}
				}
			})
		}
	}
}

func TestBlockRowsText_ContinuationAcrossChunks(t *testing.T) {
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
	got, err := content.BlockRowsText(chunks)
	if err != nil || got != "\nA中é B\n\ntail\n" {
		t.Fatalf("BlockRowsText = %q, %v; want %q", got, err, "\nA中é B\n\ntail\n")
	}
}

func TestBlockRowsText_EmptyAndInvalidChunks(t *testing.T) {
	for _, chunks := range [][][]byte{nil, {nil, {}, []byte(" \n")}} {
		got, err := content.BlockRowsText(chunks)
		if err != nil || got != "" {
			t.Fatalf("empty BlockRowsText = %q, %v", got, err)
		}
	}
	for _, invalid := range []string{
		`{"row":`,
		`{"row":{"text":"x","marks":[[0,1,1],[0,1,1]]}}`,
		`{"row":{"text":"","marks":[[0,2,1]]}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			chunks := [][]byte{[]byte("{\"row\":{\"text\":\"valid\"}}\n"), []byte(invalid)}
			got, err := content.BlockRowsText(chunks)
			if err == nil || got != "" {
				t.Fatalf("invalid BlockRowsText = %q, %v; want empty text and error", got, err)
			}
		})
	}
}
