package transport

// A session nocx has no shell integration for (nocx-22k1c.3).
//
// The pane's sentence rests on two backend facts, and this is where they are
// pinned. The first is that recording does not ask whether a session is
// integrated: the recorder is the replay ring's consumer (nocx-22k1c.1) and
// the ring is fed by the output pump, which knows nothing about the
// lifecycle channel. The second is that such a session says WHY on the wire,
// as `unsupported-shell`, so the pane can name it rather than shrug.
//
// If either stops being true the product starts lying in a way nothing else
// would catch — a card claiming a recording that is not happening, or a card
// that cannot say which of thirty things went wrong. The frontend tests
// assert the sentence; these assert the two facts underneath it.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// unsupportedShellPath is a login shell with no local tier —
// shellintegration.LocalShellKind answers ShellUnknown for it, and
// internal/app's local factory then starts it conventionally and reports
// ReasonUnsupportedShell. The path is only a label here; nothing execs it.
const unsupportedShellPath = "/usr/local/bin/fish"

// unsupportedShellFactory is internal/app's local factory for a login shell
// with no tier, reduced to the two things this package can observe: it hands
// back a terminal that can be fed, and it enters the session into the
// integration axis as conventional/unsupported-shell FROM INSIDE the open.
//
// Inside the open for the reason integrationPTYFactory states: the open
// handler emits the session's status once after its ack, so an axis
// registered after `open` has returned races that emit and the frame count
// stops being one. It is also where the production factory does it, which is
// the point of using a factory at all rather than calling RegisterIntegration
// from the test body.
type unsupportedShellFactory struct {
	term *feedablePTY
	ws   atomic.Pointer[WSServer]
}

func (f *unsupportedShellFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	if ws := f.ws.Load(); ws != nil && cfg.SessionID != "" {
		ws.RegisterIntegration(session.ID(cfg.SessionID), unsupportedShellPath,
			IntegrationConventional, ssh.ReasonUnsupportedShell)
	}
	return f.term, nil
}

// The whole of the backend half, in one session: a shell with no integration
// records what it prints, has no channel any block could come from, and says
// which of the reasons this is — the last two off the real socket, validated
// against the contract.
func TestIntegration_UnsupportedShellRecordsAndSaysWhy(t *testing.T) {
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	rec := newFakeRecorder()
	f := &unsupportedShellFactory{term: term}
	e := newLifecycleTestEnvWithReg(t, session.New(logger, f), WithSessionOutputRecorder(rec))
	f.ws.Store(e.ws)
	sid := e.openSession(t, 1)

	// 1. It says WHY, in the closed vocabulary the pane's table is keyed on.
	//
	// Asked for by the property that makes it the answer rather than by
	// position: this axis re-sends deliberately (the open handler after its
	// ack, a reattach's replay), so "the next frame" and "the frame that
	// says how it came out" are different questions.
	raw, got := readIntegrationWhere(t, e.conn, sid,
		"the fact that concludes the axis", integrationConcluded)
	validateJSON(t, schema, raw,
		"session.integrationChanged params (real socket, unsupported shell)")
	if got.Status != IntegrationConventional {
		t.Errorf("status = %q, want %q", got.Status, IntegrationConventional)
	}
	if got.Reason != string(ssh.ReasonUnsupportedShell) {
		t.Errorf("reason = %q, want %q — a pane that cannot name the reason can only shrug",
			got.Reason, ssh.ReasonUnsupportedShell)
	}
	if got.Shell != unsupportedShellPath {
		t.Errorf("shell = %q, want %q", got.Shell, unsupportedShellPath)
	}

	// 2. It produces no blocks, and the reason is structural rather than
	// timed: blocks come from the authenticated lifecycle channel, and this
	// session has no lane on it — so there is nothing that COULD publish an
	// attempt, whatever anybody waits for.
	e.ws.lifecycleMu.Lock()
	lanes := len(e.ws.lifecycleLanes)
	e.ws.lifecycleMu.Unlock()
	if lanes != 0 {
		t.Errorf("lifecycle lanes = %d, want 0: a shell with no tier is given no channel", lanes)
	}

	// 3. And it is recorded all the same. The recorder never asks whether
	// the session integrated — this is the assertion that keeps the pane's
	// sentence true, and the one a future gate on integration would break.
	stream := recordingStream(8 << 10)
	awaitPush(t, "the session's output", pushOutput(term, stream))
	awaitRecorded(t, rec, len(stream))
	if n := len(rec.recorded()); n < len(stream) {
		t.Errorf("recorded %d of %d bytes produced by a session with no integration", n, len(stream))
	}
}

// The paired negative, and the one that stops the sentence appearing
// everywhere: a session that INTEGRATED carries no reason at all, so the pane
// has nothing to key a message on. Without it, "names the reason" would be
// satisfied by a backend that named one for every session alive.
//
// Off the real socket and on the RAW frame, because the claim is about a
// field being ABSENT: a Go struct decoded from the wire has a zero value for
// a field the server never sent and one it sent as "", and only the bytes
// tell those apart. TestIntegration_LiveDomainReportsIntegrated already
// asserts the decoded half; this is the half it cannot see.
func TestIntegration_AnIntegratedSessionCarriesNoReasonOverTheWire(t *testing.T) {
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	e := newIntegrationEnv(t)

	// The shell answers, exactly as establish() drives it. The status frame
	// is read BEFORE the establishment acknowledgement because that is the
	// order the server writes them.
	mustLifecycleIngest(t, e.pub, "T", lifecycleEnv(e.lane, e.h, 1, lifecycleHelloEvt()))
	raw, got := readIntegrationWhere(t, e.conn, e.sid, "the session to reach integrated",
		func(p integrationChangedParams) bool { return p.Status == IntegrationIntegrated })
	ackEstablishmentFrom(t, e.pub, e.lane, e.h, e.conn)

	validateJSON(t, schema, raw,
		"session.integrationChanged params (real socket, integrated)")
	if got.Reason != "" {
		t.Errorf("reason = %q, want none: an integrated session has nothing to explain", got.Reason)
	}
	var onTheWire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onTheWire); err != nil {
		t.Fatalf("decode the raw fact: %v", err)
	}
	if _, present := onTheWire["reason"]; present {
		t.Errorf("an integrated session carries a reason field on the wire: %s", raw)
	}
}
