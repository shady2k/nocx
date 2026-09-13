package agentdriver_test

// Replay of the captured corpus in testdata/captures.
//
// A driver is built against MOMENTS — the input box before a turn, the
// spinner during one, the dialog that interrupts it — and every one of them
// is gone by the end of the capture. So a test names a capture and a
// millisecond mark, and gets the screen as it stood there.
//
// It replays through the SAME emulator the product reads a pane with — the
// helper's, which in a test is this process's copy of the same adapter, over
// the same bytes from byte zero. A frame assembled any other way would be a
// frame the product never makes.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentcapture/replaylocal"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
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

// replayer is a capture plus whatever a test paints onto it, read through the
// emulator that owns the terminal.
//
// It replaces a stateful Replayer that held an emulator of its own: the
// emulator is the helper's now (ADR-0066), and a replay is a QUESTION — feed
// these bytes and answer the screen at this mark. "Paint something more" is one
// more chunk and one more mark, which is exactly what the old Feed was.
type replayer struct {
	header agentcapture.Header
	chunks []agentcapture.Chunk
	mark   int64
}

func newReplayer(t *testing.T, name string, atMs int64) *replayer {
	t.Helper()
	header, chunks, err := agentcapture.Read(capturePath(name))
	if err != nil {
		t.Fatalf("read capture %s: %v", name, err)
	}
	if header.Cols <= 0 || header.Rows <= 0 {
		t.Fatalf("capture %s has no geometry", name)
	}
	through := agentcapture.ChunksThrough(chunks, atMs, 0)
	return &replayer{header: header, chunks: chunks[:through], mark: atMs}
}

// Feed appends chunks to the stream, as painting onto the replayed screen.
func (r *replayer) Feed(chunks []agentcapture.Chunk) error {
	r.chunks = append(r.chunks, chunks...)
	// Past every mark already asked for, so a later read still replays from
	// byte zero through everything fed so far.
	r.mark++
	return nil
}

// Frame answers the screen as it stands.
func (r *replayer) Frame() (paneview.Frame, error) {
	moments, err := agentcapture.Frames(context.Background(), replaylocal.Replayer{}, r.header, r.chunks, []int64{r.mark})
	if err != nil {
		return paneview.Frame{}, err
	}
	if len(moments) != 1 {
		return paneview.Frame{}, fmt.Errorf("replay answered %d moments, want 1", len(moments))
	}
	return moments[0].Frame, nil
}

// replay is the common case: the screen at a moment, and nothing else.
func replay(t *testing.T, name string, atMs int64) paneview.Frame {
	t.Helper()
	f, err := newReplayer(t, name, atMs).Frame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return f
}

// screen paints rows onto a real emulator and parks the cursor, for the shapes
// the corpus does not contain. It goes through panegrid for the same reason
// replay does: a frame assembled by hand is a frame the product never makes.
func screen(t *testing.T, cols, rows int, lines []string, cursorX, cursorY int) paneview.Frame {
	t.Helper()
	store := paneviewtest.NewViews(log.NewSlogAdapter(nil))
	const pane = "synthetic"
	if err := store.Watch(pane, cols, rows); err != nil {
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
