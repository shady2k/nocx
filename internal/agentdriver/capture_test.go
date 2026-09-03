package agentdriver_test

// Replay of the captured corpus in testdata/captures.
//
// A driver is built against MOMENTS — the input box before a turn, the
// spinner during one, the dialog that interrupts it — and every one of them
// is gone by the end of the capture. So a test names a capture and a
// millisecond mark, and gets the screen as it stood there.
//
// It replays through internal/panegrid rather than through a bare emulator on
// purpose: the frame a driver classifies in production comes out of a Store
// that was fed from byte zero, and a test that built its frame some other way
// would be asserting about a screen the product never produces.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// captureNames is the corpus, named once. Two tests walk all of it — the
// closed-set sweep in claude_test.go and the projection sweep in
// observation_test.go — and a corpus named twice is a corpus that grows in one
// place only.
var captureNames = []string{
	"claude-error",
	"claude-idle", "claude-idle-60", "claude-idle-80", "claude-working",
	"claude-permission", "claude-permission-60", "claude-modal", "claude-subagent",
}

type captureHeader struct {
	Agent string `json:"agent"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
}

type captureChunk struct {
	AtMs int64  `json:"atMs"`
	Data string `json:"data"`
}

// replayStore feeds a capture up to atMs into a real Store and hands back both,
// so a test that wants to print something MORE onto that screen can.
func replayStore(t *testing.T, name string, atMs int64) (*panegrid.Store, string) {
	t.Helper()
	path := filepath.Join("testdata", "captures", name+".jsonl")
	f, err := os.Open(path) //nolint:gosec // a fixture path this test builds
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	if !sc.Scan() {
		t.Fatalf("%s: empty capture", path)
	}
	var h captureHeader
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		t.Fatalf("%s: header: %v", path, err)
	}
	if h.Cols <= 0 || h.Rows <= 0 {
		t.Fatalf("%s: header has no geometry (%dx%d)", path, h.Cols, h.Rows)
	}

	store := panegrid.New(log.NewSlogAdapter(nil))
	const pane = "capture"
	if err := store.Enrol(pane, h.Cols, h.Rows); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { store.Withdraw(pane) })

	for sc.Scan() {
		var c captureChunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("%s: chunk: %v", path, err)
		}
		if c.AtMs > atMs {
			break
		}
		store.Feed(pane, []byte(c.Data))
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: read: %v", path, err)
	}
	return store, pane
}

// replay is the common case: the screen at a moment, and nothing else.
func replay(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	store, pane := replayStore(t, name, atMs)
	fr, err := store.Frame(pane)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return fr
}

// screen paints rows onto a real emulator and parks the cursor, for the shapes
// the corpus does not contain. It goes through panegrid for the same reason
// replay does: a frame assembled by hand is a frame the product never makes.
func screen(t *testing.T, cols, rows int, lines []string, cursorX, cursorY int) panegrid.Frame {
	t.Helper()
	store := panegrid.New(log.NewSlogAdapter(nil))
	const pane = "synthetic"
	if err := store.Enrol(pane, cols, rows); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { store.Withdraw(pane) })
	var b strings.Builder
	b.WriteString("\x1b[2J")
	for y, text := range lines {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", y+1, text)
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH", cursorY+1, cursorX+1)
	store.Feed(pane, []byte(b.String()))
	fr, err := store.Frame(pane)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return fr
}
