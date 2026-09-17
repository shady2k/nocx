package client_test

// The screen reads off the real socket (nocx-ygxjv.3): the third of
// contracts/README.md's three checks, and the only one that can catch a field
// the helper never sends.
//
// The shapes are validated on the RAW result rather than on what the client
// decodes, because the decoding is what would hide a missing field: a struct
// with an absent member decodes to its zero value and reads exactly like a
// member the helper sent as zero. Both are done below — the raw payload against
// the schema, and the client's own answer against the same session — so a
// disagreement between them fails here rather than in a pane.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TestTheScreenResultConformsToItsContractOverTheWire reads a real session's
// screen through the real service and validates what crossed.
func TestTheScreenResultConformsToItsContractOverTheWire(t *testing.T) {
	c := hostedSessions(t)
	entry := spawnOverTheWire(t, c)

	params := proto.ScreenParams{Session: proto.HostSessionID{
		Generation: proto.GenerationID(entry.HostSessionID.Generation),
		Session:    entry.HostSessionID.Session,
	}}
	if err := validateHelperJSON(loadHelperSchema(t, "session.screen.params.schema.json"),
		mustMarshal(t, params)); err != nil {
		t.Fatalf("the screen params the test sends do not satisfy the contract: %v", err)
	}

	var raw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpScreen, params, &raw); err != nil {
		t.Fatalf("screen: %v", err)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "session.screen.schema.json"), raw); err != nil {
		t.Fatalf("the screen result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, raw)
	}

	// And the client's own answer is the same screen: the shape above is the
	// wire's and this is what a caller acts on.
	frame, err := c.Screen(context.Background(), entry.HostSessionID)
	if err != nil {
		t.Fatalf("client.Screen: %v", err)
	}
	var wire proto.ScreenResult
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode the raw result: %v", err)
	}
	// GEOMETRY is compared and the CURSOR is not, deliberately: these are two
	// independent reads of a live shell, and the prompt it printed between them
	// moves the caret. Asserting on it would be asserting that nothing happened
	// in between, which is a claim about timing rather than about the decode.
	if frame.Cols != wire.Frame.Cols || frame.Rows != wire.Frame.Rows {
		t.Errorf("client frame is %dx%d, wire frame is %dx%d",
			frame.Cols, frame.Rows, wire.Frame.Cols, wire.Frame.Rows)
	}
	if len(frame.Lines) != len(wire.Frame.Lines) {
		t.Errorf("client frame has %d rows, the wire carried %d", len(frame.Lines), len(wire.Frame.Lines))
	}
	for y, row := range frame.Lines {
		if len(row) != frame.Cols {
			t.Fatalf("client row %d is %d cells wide, want %d: a frame is a rectangle", y, len(row), frame.Cols)
		}
	}
	// The client's cursor is inside the frame it decoded, which is the property
	// a renderer needs and the one a mis-decoded row would break.
	if frame.CursorX < 0 || frame.CursorX >= frame.Cols || frame.CursorY < 0 || frame.CursorY >= frame.Rows {
		t.Errorf("client cursor (%d,%d) is outside a %dx%d frame", frame.CursorX, frame.CursorY, frame.Cols, frame.Rows)
	}
	// A runtime the helper created before the first byte of output is COMPLETE,
	// and the client must decode that spelling rather than fall back to a zero
	// value: unknown is the state a write gate refuses in, so the two must not
	// be confusable.
	if frame.Completeness != sessionruntime.CompletenessComplete {
		t.Errorf("completeness decoded as %v, want Complete", frame.Completeness)
	}
	if wire.Completeness != proto.CompletenessComplete {
		t.Errorf("the wire carried %q, want %q", wire.Completeness, proto.CompletenessComplete)
	}
}

// TestTheReplayResultConformsToItsContractOverTheWire feeds a capture's bytes to
// the helper's PTY-less emulator over the real socket.
func TestTheReplayResultConformsToItsContractOverTheWire(t *testing.T) {
	c := hostedSessions(t)

	params := proto.ReplayParams{
		Cols:    20,
		Rows:    4,
		Through: []int{1, 2},
		Chunks: [][]byte{
			[]byte("\x1b[2J\x1b[1;1Hfirst"),
			[]byte("\x1b[3;1Hsecond"),
		},
	}
	if err := validateHelperJSON(loadHelperSchema(t, "session.replay.params.schema.json"),
		mustMarshal(t, params)); err != nil {
		t.Fatalf("the replay params the test sends do not satisfy the contract: %v", err)
	}

	var raw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpReplay, params, &raw); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "session.replay.schema.json"), raw); err != nil {
		t.Fatalf("the replay result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, raw)
	}

	var wire proto.ReplayResult
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode the raw result: %v", err)
	}
	if len(wire.Frames) != 2 {
		t.Fatalf("the wire carried %d frames, want one per mark", len(wire.Frames))
	}
	frames, err := c.Replay(context.Background(), params.Cols, params.Rows, params.Chunks, params.Through)
	if err != nil {
		t.Fatalf("client.Replay: %v", err)
	}
	if len(frames) != len(wire.Frames) {
		t.Fatalf("client replay answered %d frames, the wire carried %d", len(frames), len(wire.Frames))
	}
	for i := range frames {
		if frames[i].Cols != wire.Frames[i].Cols || len(frames[i].Lines) != len(wire.Frames[i].Lines) {
			t.Errorf("frame %d: client %dx%d, wire %dx%d",
				i, frames[i].Cols, len(frames[i].Lines), wire.Frames[i].Cols, len(wire.Frames[i].Lines))
		}
	}
	// The screen the SECOND mark names, read through the client: the emulator
	// is fed forward, so the first chunk's row is still there.
	if got := strings.TrimRight(rowTextOf(frames[1].Lines[0]), " "); got != "first" {
		t.Errorf("at the second mark row 1 reads %q, want the first chunk's text", got)
	}
	if got := strings.TrimRight(rowTextOf(frames[1].Lines[2]), " "); got != "second" {
		t.Errorf("at the second mark row 3 reads %q, want the second chunk's text", got)
	}
}

// spawnOverTheWire starts one shell through the real service, the way the
// sibling contract test does: the params satisfy their own schema, so what the
// result proves is about an answer somebody would really send for.
func spawnOverTheWire(t *testing.T, c *client.Client) client.SessionEntry {
	t.Helper()
	in := proto.SpawnParams{Cwd: "/", Cols: 100, Rows: 30, WindowBytes: 1 << 20, IdempotencyKey: "pane-screen"}
	if err := validateHelperJSON(loadHelperSchema(t, "session.spawn.params.schema.json"), mustMarshal(t, in)); err != nil {
		t.Fatalf("the spawn params do not satisfy the contract: %v", err)
	}
	entry, err := c.Spawn(context.Background(), in)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	return entry
}

// rowTextOf renders one frame row as a reader does: continuation cells skipped,
// a blank cell one space.
func rowTextOf(row []paneview.Cell) string {
	var b strings.Builder
	for _, c := range row {
		if c.Width == 0 {
			continue
		}
		if c.Text == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Text)
	}
	return b.String()
}
