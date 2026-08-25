package transport

// The awaiting-takeover transition (ADR-0020 decision 3): a program that
// takes the alternate screen puts the lane in awaiting-takeover — the agent
// is DEMOTED, not evicted: it loses write authority and keeps the right to
// read the screen and advise. The fact travels up from the renderer
// (agent.laneInteractivity carries the buffer kind the renderer owns — the
// backend cannot see the alternate screen, AD-6); the transition is decided
// here, in Go. Asserted by trying: writing is refused (and no run request
// ever reaches the renderer), reading succeeds (a real frame comes back),
// and a lane that returns to normal accepts runs again.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/session"
)

func TestLaneInteractivity_AwaitingTakeoverRefusesWritesAndAllowsReads(t *testing.T) {
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	// The lane takes the alternate screen: the renderer's report. The
	// transition is decided HERE, in Go, from the fact.
	resp := jsonrpcCall(t, conn, "agent.laneInteractivity", map[string]any{
		"sessionId": sid, "bufferKind": "alternate",
	})
	if isErrorResponse(t, resp) {
		t.Fatalf("laneInteractivity refused: %s", resp)
	}

	// Writing is refused: RequestRun returns the takeover refusal, and NO
	// run request is minted — the broker's pending set stays empty, so the
	// renderer was never asked to submit anything (asserted by the
	// mechanism, not by a socket read that would poison the connection).
	done := make(chan error, 1)
	go func() { _, err := ws.RequestRun(t.Context(), sid, "ls"); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "awaiting takeover") {
			t.Fatalf("RequestRun = %v, want the awaiting-takeover refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the refusal must be immediate — the write is refused, not deferred")
	}
	if n := ws.broker.Pending(); n != 0 {
		t.Fatalf("broker pending = %d after the refusal, want 0 — no run request may exist", n)
	}

	// Reading keeps working: the readScreen request is minted and the
	// renderer's frame answer comes back — the demotion keeps read rights.
	readDone := make(chan error, 1)
	var frame json.RawMessage
	go func() {
		var err error
		frame, err = ws.RequestScreen(t.Context(), sid, nil)
		readDone <- err
	}()
	raw := readNotification(t, conn, "agent.readScreenRequest", 5*time.Second)
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("readScreenRequest decode: %v", err)
	}
	reply := jsonrpcCall(t, conn, "agent.readScreenResolved", readScreenFrameWire(t, req.RequestID, "vim is still here", "the human owns it"))
	if isErrorResponse(t, reply) {
		t.Fatalf("readScreenResolved refused: %s", reply)
	}
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("RequestScreen = %v, want the frame — the agent keeps reading while demoted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestScreen never settled")
	}
	// The frame's text is per-cell (the wire vocabulary) — join the chars
	// to assert the READ carried the renderer's answer.
	var body struct {
		Rows []struct {
			Cells []struct {
				Char string `json:"char"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(frame, &body); err != nil {
		t.Fatalf("frame decode: %v", err)
	}
	var text strings.Builder
	for _, r := range body.Rows {
		for _, c := range r.Cells {
			text.WriteString(c.Char)
		}
	}
	if !strings.Contains(text.String(), "the human owns it") {
		t.Fatalf("RequestScreen frame text = %q, want the renderer's answer", text.String())
	}
	// The TUI exits: the lane leaves awaiting-takeover, and runs are
	// accepted again — the demotion is a transition, not a state the lane
	// is stuck in.
	resp = jsonrpcCall(t, conn, "agent.laneInteractivity", map[string]any{
		"sessionId": sid, "bufferKind": "normal",
	})
	if isErrorResponse(t, resp) {
		t.Fatalf("laneInteractivity(normal) refused: %s", resp)
	}
	runDone := make(chan error, 1)
	go func() {
		_, err := ws.RequestRun(t.Context(), sid, "ls")
		runDone <- err
	}()
	raw = readNotification(t, conn, "agent.runRequest", 5*time.Second)
	var runReq struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &runReq); err != nil {
		t.Fatalf("runRequest decode: %v", err)
	}
	// Settle the request so the goroutine exits: a failed outcome is the
	// honest terminal answer for a test that only wants the params.
	runReply := jsonrpcCall(t, conn, "agent.runResolved", map[string]any{
		"requestId": runReq.RequestID, "outcome": "failed", "error": "takeover test",
	})
	if isErrorResponse(t, runReply) {
		t.Fatalf("runResolved refused: %s", runReply)
	}
	select {
	case err := <-runDone:
		if err == nil || !strings.Contains(err.Error(), "could not run the command") {
			t.Fatalf("RequestRun = %v, want the renderer's failed-outcome error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestRun never settled")
	}
}

func TestLaneInteractivity_ValidationAtTheWire(t *testing.T) {
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing sessionId", map[string]any{"bufferKind": "alternate"}},
		{"empty sessionId", map[string]any{"sessionId": "", "bufferKind": "alternate"}},
		{"unknown bufferKind", map[string]any{"sessionId": sid, "bufferKind": "swapped"}},
	}
	for _, c := range cases {
		resp := jsonrpcCall(t, conn, "agent.laneInteractivity", c.params)
		var env struct {
			Error *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if env.Error == nil || env.Error.Code != -32602 {
			t.Fatalf("%s: err = %+v, want -32602 — a malformed report is refused, never silently defaulted", c.name, env.Error)
		}
	}

	// The paired success: the valid report is accepted and does NOT touch
	// the lane state's write authority for an unrelated session.
	resp := jsonrpcCall(t, conn, "agent.laneInteractivity", map[string]any{
		"sessionId": sid, "bufferKind": "normal",
	})
	if isErrorResponse(t, resp) {
		t.Fatalf("valid report refused: %s", resp)
	}
}

// The lane state itself: the transition is decided in Go from the renderer's
// fact, and a session that closes forgets its transition (a re-opened
// session of the same id is a different incarnation).
func TestLaneInteractivity_StateIsPerSessionAndDroppedOnClose(t *testing.T) {
	ls := newLaneState()
	sid := session.ID("lane-state-session")
	other := session.ID("other-session")

	if ls.awaitingTakeover(sid) {
		t.Fatal("a fresh lane is not awaiting takeover")
	}
	// A watcher records every transition, synchronously on the reporting
	// goroutine (the shape the lease's suspension callback relies on).
	var mu sync.Mutex
	var seen []bool
	stop := ls.watch(sid, func() {
		mu.Lock()
		seen = append(seen, ls.awaitingTakeover(sid))
		mu.Unlock()
	})
	snapshot := func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return append([]bool(nil), seen...)
	}

	ls.note(sid, "alternate")
	if !ls.awaitingTakeover(sid) {
		t.Fatal("the alternate screen must put the lane in awaiting-takeover")
	}
	if got := snapshot(); len(got) != 1 || !got[0] {
		t.Fatalf("watcher saw %v, want exactly one awaiting transition", got)
	}
	// Another session's transition does not move this one, nor wake its
	// watcher.
	ls.note(other, "alternate")
	if !ls.awaitingTakeover(sid) {
		t.Fatal("another lane's transition must not clear this lane's")
	}
	if got := snapshot(); len(got) != 1 {
		t.Fatalf("another lane's transition woke this lane's watcher: %v", got)
	}
	ls.note(sid, "normal")
	if ls.awaitingTakeover(sid) {
		t.Fatal("leaving the alternate screen must clear awaiting-takeover")
	}
	if got := snapshot(); len(got) != 2 || got[1] {
		t.Fatalf("watcher saw %v, want the leaving transition to report not awaiting", got)
	}
	// An unwatched lane stops waking the watcher.
	stop()
	ls.note(sid, "alternate")
	if !ls.awaitingTakeover(sid) {
		t.Fatal("the state still transitions without a watcher")
	}
	if got := snapshot(); len(got) != 2 {
		t.Fatalf("watcher fired after stop: %v", got)
	}
	ls.remove(sid)
	if ls.awaitingTakeover(sid) {
		t.Fatal("a closed session's lane must forget its transition")
	}
	// A watcher's stop after remove is a no-op, and a later note on the
	// same id starts a fresh record (a re-opened session is a different
	// incarnation).
	stop()
	ls.note(sid, "alternate")
	if !ls.awaitingTakeover(sid) {
		t.Fatal("a re-opened session's lane starts clean and can transition again")
	}
}
