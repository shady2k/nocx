package proto

// The measurement the brief owes the close, taken through the REAL path: a
// real ghostty emulator fed real output, the frame the runtime ACTUALLY
// publishes for it, measured as the carrier would carry it — not EncodeFrame
// over a fixture, and not a number cited from the schema's record. This test
// lives beside the codec rather than beside the runtime because the numbers
// it reports are the carrier's: what it proves is that every frame the
// runtime can publish at shipped geometries fits the parts the carrier
// assembles, and that the pathological screen — every cell its own
// truecolour style, the case runs cannot compress — takes the named answer
// (parts, within the assembly bound) rather than a silent oversize.

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// measureTerminal is the terminal port over nothing: the measurement writes
// no bytes to a program and resizes nothing, because it is the SCREEN side
// the frame comes from.
type measureTerminal struct{}

func (measureTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (measureTerminal) Resize(sessionruntime.Geometry) error {
	return nil
}

func newMeasuredRuntime(t *testing.T, cols, rows int) *sessionruntime.Session {
	t.Helper()
	g := emulator.Geometry{Cols: cols, Rows: rows, CellWidthPx: 10, CellHeightPx: 20}
	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	rt, err := sessionruntime.New(sessionruntime.Config{
		Incarnation:  sessionruntime.Incarnation{Session: "measure", Generation: 1},
		Geometry:     sessionruntime.Geometry{Cols: cols, Rows: rows},
		Terminal:     measureTerminal{},
		Emulator:     screen,
		Completeness: sessionruntime.CompletenessComplete,
	})
	if err != nil {
		t.Fatalf("build the runtime: %v", err)
	}
	return rt
}

func ingestAll(t *testing.T, rt *sessionruntime.Session, stream []byte) {
	t.Helper()
	for len(stream) > 0 {
		n := len(stream)
		if n > sessionruntime.MaxIngestBytes {
			n = sessionruntime.MaxIngestBytes
		}
		if err := rt.Ingest(stream[:n]); err != nil {
			t.Fatalf("ingest: %v", err)
		}
		stream = stream[n:]
	}
}

// realisticScreen builds the escape-byte stream of one screen: a styled
// prompt row, numbered body rows with truecolour spans, and an inverse
// status bar. It is output a program could have written, not a fixture the
// encoder was handed.
func realisticScreen(cols, rows int) []byte {
	var b bytes.Buffer
	b.WriteString("\x1b[1;32m~\x1b[0m \x1b[1;34m$\x1b[0m make test\r\n")
	for y := 1; y < rows-1; y++ {
		b.WriteString(fmt.Sprintf("\x1b[33m[%02d]\x1b[0m ", y%100))
		for x := 10; x < cols; x += 10 {
			b.WriteString(fmt.Sprintf("\x1b[38;2;%d;128;255m%-10s\x1b[0m", (x*7)%256, "output"))
		}
		b.WriteString("\r\n")
	}
	b.WriteString("\x1b[7m STATUS \x1b[0m")
	return b.Bytes()
}

func TestPublishedFrameSizeAtRealGeometry(t *testing.T) {
	for _, g := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		t.Run(fmt.Sprintf("%dx%d", g[0], g[1]), func(t *testing.T) {
			rt := newMeasuredRuntime(t, g[0], g[1])
			if _, err := rt.CommitGeometry(sessionruntime.Geometry{Cols: g[0], Rows: g[1]}); err != nil {
				t.Fatalf("commit %dx%d: %v", g[0], g[1], err)
			}
			ingestAll(t, rt, realisticScreen(g[0], g[1]))

			c := rt.Consumers().Attach()
			frames := c.Take()
			if len(frames) == 0 {
				t.Fatal("a screen of published output delivered no frame")
			}
			got := frames[len(frames)-1]
			t.Logf("%dx%d published frame: %d bytes", g[0], g[1], len(got.Bytes))
			parts, err := SplitScreenDataFrame([16]byte{}, [16]byte{}, uint64(got.Revision), got.Bytes)
			if err != nil {
				t.Fatalf("the realistic screen does not fit the carrier: %v", err)
			}
			if len(parts) > 1 {
				t.Logf("%dx%d carried as %d parts", g[0], g[1], len(parts))
			}
		})
	}
}

func TestPublishedPathologicalScreenTakesTheNamedAnswer(t *testing.T) {
	rt := newMeasuredRuntime(t, 120, 40)
	ingestAll(t, rt, pathologicalScreen(120, 40))

	c := rt.Consumers().Attach()
	frames := c.Take()
	if len(frames) == 0 {
		t.Fatal("the pathological screen published no frame")
	}
	got := frames[len(frames)-1]
	t.Logf("120x40 pathological published frame: %d bytes", len(got.Bytes))
	parts, err := SplitScreenDataFrame([16]byte{}, [16]byte{}, uint64(got.Revision), got.Bytes)
	if err != nil {
		t.Fatalf("the pathological screen was refused whole and must be delivered as parts: %v", err)
	}
	t.Logf("120x40 pathological carried as %d parts", len(parts))
	if len(parts) < 2 {
		t.Fatalf("the pathological screen fit one part (%d); the fixture no longer exercises the continuation", len(parts))
	}
}

// pathologicalScreen gives every cell its own truecolour foreground: no two
// adjacent cells share a style, so runs compress nothing and the frame is as
// large as shipped geometry can make it.
func pathologicalScreen(cols, rows int) []byte {
	var b bytes.Buffer
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;255m#", x%256, y%256)
		}
		b.WriteString("\r\n")
	}
	return b.Bytes()
}
