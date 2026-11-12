package content_test

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// TestBlockRowsTextAfter_WrappedLineAllocationIsLinear guards the cost of a
// single logical line thousands of rows long: joining continuations onto a
// growing string copies the prefix once per row, which is quadratic. Every row
// here is joined (the output start is zero), and the bound is on bytes
// allocated, never on time.
func TestBlockRowsTextAfter_WrappedLineAllocationIsLinear(t *testing.T) {
	const (
		textBytes = 1 << 20
		width     = 80
	)
	rows := textBytes / width
	var body bytes.Buffer
	for row := range rows {
		fmt.Fprintf(&body, "{\"from\":%d,\"row\":{\"text\":%q,\"continuation\":%t}}\n",
			row, strings.Repeat("x", width), row > 0)
	}
	chunks := splitBlockRowsChunks(body.Bytes(), 64<<10)

	// Warm the decoder outside the measurement; then one call's total
	// allocation is the cost being bounded.
	if _, err := content.BlockRowsTextAfter(chunks, 0); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := content.BlockRowsTextAfter(chunks, 0)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if want := rows * width; len(got) != want {
		t.Fatalf("BlockRowsTextAfter length = %d, want %d", len(got), want)
	}
	// The growing-prefix implementation allocated 6.98 GB per call for this
	// input; decoding and writing once allocates 54 MB, under 32 times the
	// encoded bytes. The bound leaves room to double before it trips.
	const limitPerInputByte = 64
	allocated := after.TotalAlloc - before.TotalAlloc
	if limit := uint64(limitPerInputByte) * uint64(body.Len()); allocated > limit { //nolint:gosec // a test's own byte count, never negative
		t.Fatalf("BlockRowsTextAfter allocated %d bytes for %d bytes of rows, want at most %d: a wrapped line is being copied per row", allocated, body.Len(), limit)
	}
}
