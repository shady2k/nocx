package session_test

// The screen reads (nocx-ygxjv.3): the frame a session's runtime holds, and the
// PTY-less replay a capture is read through.
//
// These drive the SERVICE, over the same seams every other test in this package
// uses, because what is being asserted is what a coordinator gets when it asks
// — not what paneview does with an emulator it was handed (that is
// internal/paneview's own suite).

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// TestTheScreenOpAnswersTheFrameTheRuntimeHolds is the ordinary case: bytes the
// program printed are on the screen the helper answers with, and the two facts
// only a runtime has travel beside it.
func TestTheScreenOpAnswersTheFrameTheRuntimeHolds(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	entry := spawnOne(t, svc)
	proc := procOf(t, spawner, 0)

	// The program draws: a marker in its own row, and the cursor left after it.
	proc.say(t, "\x1b[2J\x1b[3;5HTHE-SCREEN")

	res := waitForScreen(t, svc, entry, "THE-SCREEN")

	if res.Frame.Cols != 80 || res.Frame.Rows != 24 {
		t.Errorf("frame is %dx%d, want the size the session was spawned at", res.Frame.Cols, res.Frame.Rows)
	}
	if len(res.Frame.Lines) != res.Frame.Rows {
		t.Fatalf("frame carries %d rows, want %d: a frame is a rectangle", len(res.Frame.Lines), res.Frame.Rows)
	}
	if got := rowText(res.Frame.Lines[2]); !strings.HasPrefix(got, "    THE-SCREEN") {
		t.Errorf("row 3 reads %q, want the marker at the fifth column", got)
	}
	// The caret is where the program left it: five columns of CUP plus nine
	// cells of text, zero-indexed on this side.
	if res.Frame.CursorX != 14 || res.Frame.CursorY != 2 {
		t.Errorf("cursor is (%d,%d), want (14,2)", res.Frame.CursorX, res.Frame.CursorY)
	}
	if !res.Frame.CursorVisible {
		t.Error("the caret is reported hidden; a program that said nothing about DECTCEM shows one")
	}
	// A runtime that saw byte zero says so, and a write gate reads this.
	if res.Completeness != proto.CompletenessComplete {
		t.Errorf("completeness = %q, want %q: this runtime was created before the first byte was read",
			res.Completeness, proto.CompletenessComplete)
	}
}

// TestTheScreenOpRefusesASessionItDoesNotHold is the paired refusal: the same
// call, naming a session this generation has no runtime for.
func TestTheScreenOpRefusesASessionItDoesNotHold(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	_, err := svc.Call(callCtx(), proto.OpScreen, mustJSON(t, proto.ScreenParams{
		Session: proto.HostSessionID{Generation: "gen-under-test", Session: "ffffffffffffffffffffffffffffffff"},
	}))
	if !errors.Is(err, session.ErrNoSuchSession) {
		t.Fatalf("screen for an unknown session = %v, want session.ErrNoSuchSession", err)
	}
}

// TestTheReplayOpAnswersTheScreenAtEachMark is the calibration path's whole
// reason: a capture's bytes become the frames the rules are verified against,
// and they are produced by the same emulator the sessions are directed with.
func TestTheReplayOpAnswersTheScreenAtEachMark(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	res, err := svc.Call(callCtx(), proto.OpReplay, mustJSON(t, proto.ReplayParams{
		Cols:    20,
		Rows:    4,
		Through: []int{1, 2},
		Chunks: [][]byte{
			[]byte("\x1b[2J\x1b[1;1Hfirst"),
			[]byte("\x1b[3;1Hsecond"),
		},
	}))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	out, err := decodeReplay(res)
	if err != nil {
		t.Fatalf("decode replay result: %v", err)
	}
	if len(out.Frames) != 2 {
		t.Fatalf("replay answered %d frames, want one per mark", len(out.Frames))
	}
	if got := rowText(out.Frames[0].Lines[0]); !strings.HasPrefix(got, "first") {
		t.Errorf("the first mark reads %q, want the first chunk's screen", got)
	}
	if got := rowText(out.Frames[1].Lines[2]); !strings.HasPrefix(got, "second") {
		t.Errorf("the second mark reads %q, want the second chunk's screen", got)
	}
	if got := rowText(out.Frames[1].Lines[0]); !strings.HasPrefix(got, "first") {
		t.Errorf("at the second mark row 1 reads %q: the emulator is fed forward, never restarted", got)
	}
}

// TestAPTYStreamCrossesAsBytesAndNotAsText: a capture is not UTF-8, and the
// wire must not be the place that loses that.
//
// The assertion is on the FRAMING rather than on a screen, because a screen is
// what the emulator makes of the bytes and this is about what arrives: a JSON
// string would replace every byte that is not a valid sequence with U+FFFD
// before the emulator ever saw them, and the replay would then answer about a
// stream nobody recorded. 0x00, a C1 control, a lone continuation byte and a
// truncated sequence are the four shapes a PTY really produces — and the
// decoding below is what proves none of them was rewritten on the way.
func TestAPTYStreamCrossesAsBytesAndNotAsText(t *testing.T) {
	raw := []byte{0x00, 0x9b, 0x80, 0xe2, 0x82, 0xff, 'o', 'k'}
	encoded, err := json.Marshal(proto.ReplayParams{
		Cols: 12, Rows: 2, Through: []int{1}, Chunks: [][]byte{raw},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back proto.ReplayParams
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Chunks) != 1 {
		t.Fatalf("decoded %d chunks, want 1", len(back.Chunks))
	}
	if !bytes.Equal(back.Chunks[0], raw) {
		t.Fatalf("chunk crossed as %q, want %q: the wire must not rewrite a PTY stream", back.Chunks[0], raw)
	}

	// And the op accepts them: the same bytes reach the emulator through the
	// service, which is the path the coordinator takes.
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	var out proto.ReplayResult
	if err := callInto(t, svc, proto.OpReplay, proto.ReplayParams{
		Cols: 12, Rows: 2, Through: []int{1}, Chunks: [][]byte{raw},
	}, &out); err != nil {
		t.Fatalf("replay of a non-UTF-8 stream: %v", err)
	}
	if len(out.Frames) != 1 {
		t.Fatalf("replay answered %d frames, want 1", len(out.Frames))
	}
}

// TestTheReplayOpRefusesWhatItCannotHonour: the geometry comes from a FILE a
// person may have written, so it is not a size nocx spawned. A refusal here is
// what keeps a header claiming a hundred million cells from being allocated.
func TestTheReplayOpRefusesWhatItCannotHonour(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	chunks := [][]byte{[]byte("x")}

	for _, tc := range []struct {
		name   string
		params proto.ReplayParams
	}{
		{"a geometry no terminal has", proto.ReplayParams{Cols: 0, Rows: 10, Through: []int{1}, Chunks: chunks}},
		{"a geometry past the bound", proto.ReplayParams{
			Cols: proto.MaxReplayCells, Rows: 2, Through: []int{1}, Chunks: chunks,
		}},
		{"a mark past the stream", proto.ReplayParams{Cols: 10, Rows: 2, Through: []int{5}, Chunks: chunks}},
		{"marks that go backwards", proto.ReplayParams{Cols: 10, Rows: 2, Through: []int{1, 1, 0}, Chunks: chunks}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Call(callCtx(), proto.OpReplay, mustJSON(t, tc.params))
			if !errors.Is(err, session.ErrReplay) {
				t.Fatalf("replay = %v, want session.ErrReplay", err)
			}
			// And the code the caller switches on is BadParams: the request was
			// composed by the caller and is the caller's to fix.
			if code, _ := svc.Refusal(err); code != proto.ErrCodeBadParams {
				t.Errorf("refusal code = %q, want %q", code, proto.ErrCodeBadParams)
			}
		})
	}
}

// waitForScreen asks until the marker is on the screen, so no test depends on
// how the pump's reads were chunked.
func waitForScreen(t *testing.T, svc *session.Service, entry proto.SessionEntry, marker string) proto.ScreenResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := call[proto.ScreenResult](t, svc, proto.OpScreen, proto.ScreenParams{Session: entry.Session})
		for _, row := range res.Frame.Lines {
			if strings.Contains(rowText(row), marker) {
				return res
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the marker %q never reached the screen", marker)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// rowText renders one wire row, skipping the continuation cells of a wide
// cluster exactly as a reader does.
func rowText(row []proto.ScreenCell) string {
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

func decodeReplay(res any) (proto.ReplayResult, error) {
	raw, err := json.Marshal(res)
	if err != nil {
		return proto.ReplayResult{}, err
	}
	var out proto.ReplayResult
	return out, json.Unmarshal(raw, &out)
}

func callInto(t *testing.T, svc *session.Service, op string, params, out any) error {
	t.Helper()
	res, err := svc.Call(callCtx(), op, mustJSON(t, params))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// procOf is the process the spawner produced for one spawn.
func procOf(t *testing.T, spawner *fakeSpawner, i int) *fakeProcess {
	t.Helper()
	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	if i >= len(spawner.procs) {
		t.Fatalf("the spawner produced %d processes, want at least %d", len(spawner.procs), i+1)
	}
	return spawner.procs[i]
}
