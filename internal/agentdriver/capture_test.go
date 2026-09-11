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
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
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
	"claude-trust", "claude-lmstudio-turn",
	"claude-lmstudio-idle-80", "claude-lmstudio-idle-60", "claude-lmstudio-model",
	"claude-lmstudio-permission", "claude-lmstudio-subagent", "claude-lmstudio-subagent-finished",
	"claude-lmstudio-api-refused",
	"claude-2.1.266-turn", "claude-2.1.266-idle-80", "claude-2.1.266-idle-60", "claude-2.1.266-model",
	"claude-2.1.266-permission", "claude-2.1.266-subagent-finished", "claude-2.1.266-api-refused",
}

// capturePath names a committed capture.
func capturePath(name string) string {
	return filepath.Join("testdata", "captures", name+".jsonl")
}

// replayer feeds a capture up to atMs through internal/agentcapture — the one
// reader of the format, the same one calibration and cmd/agent-capture use —
// and hands the replayer back, so a test that wants to paint something MORE
// onto that screen can Feed it.
func replayer(t *testing.T, name string, atMs int64) *agentcapture.Replayer {
	t.Helper()
	header, chunks, err := agentcapture.Read(capturePath(name))
	if err != nil {
		t.Fatalf("read capture %s: %v", name, err)
	}
	r, err := agentcapture.NewReplayer(log.NewSlogAdapter(nil), header)
	if err != nil {
		t.Fatalf("replayer for %s: %v", name, err)
	}
	t.Cleanup(r.Close)
	if err := r.Feed(chunks[:agentcapture.ChunksThrough(chunks, atMs, 0)]); err != nil {
		t.Fatalf("feed %s to %dms: %v", name, atMs, err)
	}
	return r
}

// replay is the common case: the screen at a moment, and nothing else.
func replay(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	f, err := replayer(t, name, atMs).Frame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return f
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
