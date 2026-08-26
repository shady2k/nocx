package transport

// session.ended — the feed's first honest source (nocx-p0xhg.4).
//
// A session ending is a REGISTRY fact, not a parsed byte: AD-6 is untouched
// because nothing here reads the stream, and the cause comes from the
// session layer, which is its single owner (nocx-ictcq). That is why the
// event is attested where notify.raise's is a program request.
//
// The pipeline under test is the real one — handleOpen → monitorExit → the
// session's classification of a captured wait outcome → the raise — driven
// through the same fake pty the exit-notification suite uses, because these
// two events come off the same seam and must be asserted against the same
// classification.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/testwait"
	gossh "golang.org/x/crypto/ssh"
)

// newSessionEndedServer is newExitServer with a raiser wired, and it hands
// back the registry so a test can assert attribution against the entry the
// backend stamped from rather than against a literal. Same fake pty and same
// openers as the exit suite: one seam, one vocabulary.
func newSessionEndedServer(t *testing.T, fake *exitFakePTY, raiser NotifyRaiser) (*WSServer, *session.Reg) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, &exitFakePTYFactory{fake: fake})
	opts := []WSServerOption{}
	if raiser != nil {
		opts = append(opts, WithNotifyRaiser(raiser))
	}
	ws := NewWSServer(logger, reg, opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, reg
}

// sessionHost reads the host the registry holds for a live session — the
// value the raise must be attributed with. Read BEFORE the exit, because
// monitorExit closes the registry entry on its way past.
func sessionHost(t *testing.T, reg *session.Reg, sid string) string {
	t.Helper()
	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry Get(%q): %v", sid, err)
	}
	return sess.Host()
}

func TestSessionEndRaisesAnAttestedEvent(t *testing.T) {
	fake := newExitFakePTY()
	raiser := &fakeNotifyRaiser{}
	ws, reg := newSessionEndedServer(t, fake, raiser)
	sid, conn := openExitSession(t, ws)
	host := sessionHost(t, reg, sid)

	fake.recordWait(realExitStatus(1))
	awaitExit(t, conn) // the raise precedes the notification, so this orders the read

	evs := raiser.captured()
	if len(evs) != 1 {
		t.Fatalf("raised %d events, want exactly 1", len(evs))
	}
	got := evs[0]
	if got.Kind != notify.KindSessionEnded {
		t.Errorf("Kind = %q, want %q", got.Kind, notify.KindSessionEnded)
	}
	if got.Trust != notify.TrustAttested {
		t.Errorf("Trust = %q — a registry fact must be attested, not a program request", got.Trust)
	}
	if got.Level != notify.LevelWarning {
		t.Errorf("Level = %q, want warning for a non-zero exit status", got.Level)
	}
	if got.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", got.SessionID, sid)
	}
	if got.Attribution.Backend != commandnames.LocalRoute {
		t.Errorf("Attribution.Backend = %q, want %q", got.Attribution.Backend, commandnames.LocalRoute)
	}
	if got.Attribution.Session != sid {
		t.Errorf("Attribution.Session = %q, want %q", got.Attribution.Session, sid)
	}
	if got.Attribution.Host != host {
		t.Errorf("Attribution.Host = %q, want the registry's %q", got.Attribution.Host, host)
	}
	if got.Body != "exit status 1" {
		t.Errorf("Body = %q, want the exit status", got.Body)
	}
	// At is NOT stamped here: ingress owns that stamp (notify/ingress.go), so
	// a relay replaying a buffered batch keeps its own instants.
	if !got.At.IsZero() {
		t.Errorf("At = %v, want the zero time — ingress stamps it, not the source", got.At)
	}
}

// The level and the words come from the CAUSE, and the cause comes from the
// session layer. Three rows, because a mapping with one case tested is a
// mapping nobody checked.
func TestSessionEndLevelAndTextFollowTheCause(t *testing.T) {
	cases := map[string]struct {
		wait      error
		wantLevel notify.Level
		wantBody  string
		wantWord  string
	}{
		"a clean exit is a success": {
			wait: nil, wantLevel: notify.LevelSuccess, wantBody: "", wantWord: "ended",
		},
		"a non-zero exit is a warning and carries its status": {
			wait: realExitStatus(42), wantLevel: notify.LevelWarning, wantBody: "exit status 42", wantWord: "ended",
		},
		"a loss is a warning and carries no status": {
			wait: &gossh.ExitMissingError{}, wantLevel: notify.LevelWarning, wantBody: "", wantWord: "was interrupted",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newExitFakePTY()
			raiser := &fakeNotifyRaiser{}
			ws, reg := newSessionEndedServer(t, fake, raiser)
			sid, conn := openExitSession(t, ws)
			host := sessionHost(t, reg, sid)

			fake.recordWait(tc.wait)
			awaitExit(t, conn)

			evs := raiser.captured()
			if len(evs) != 1 {
				t.Fatalf("raised %d events, want exactly 1", len(evs))
			}
			got := evs[0]
			if got.Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tc.wantLevel)
			}
			if got.Body != tc.wantBody {
				t.Errorf("Body = %q, want %q", got.Body, tc.wantBody)
			}
			// A local session has no host, and the title says so in words
			// rather than in a gap (nocx-lmmi5). The registry's host is read
			// anyway, so this asserts the local case is genuinely hostless
			// rather than assuming it.
			if host != "" {
				t.Fatalf("this suite opens LOCAL sessions; the registry reports host %q", host)
			}
			wantTitle := "Local session " + tc.wantWord
			if got.Title != wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, wantTitle)
			}
		})
	}
}

// The point of raising here rather than beside the exit notification: the
// notification needs an attached renderer and the record does not. A session
// that ended while nobody was looking is exactly the one the bell exists for.
//
// The wait is on an observable state — the subscriber slot the disconnect
// clears — never on a duration.
func TestSessionEndIsRaisedWithNoRendererAttached(t *testing.T) {
	fake := newExitFakePTY()
	raiser := &fakeNotifyRaiser{}
	ws, _ := newSessionEndedServer(t, fake, raiser)
	sid, conn := openExitSession(t, ws)
	awaitSubscriber(t, ws, session.ID(sid))

	_ = conn.Close()
	testwait.WaitForTimeout(t, "the disconnect to clear the subscriber slot", wantWithin, func() bool {
		rx := ws.getRx(session.ID(sid))
		if rx == nil {
			return true
		}
		wconn, _ := rx.getSubscriber()
		return wconn == nil
	})

	fake.recordWait(realExitStatus(3))
	testwait.WaitForTimeout(t, "the session.ended raise", wantWithin, func() bool {
		return len(raiser.captured()) == 1
	})
	if got := raiser.captured()[0]; got.Kind != notify.KindSessionEnded {
		t.Errorf("Kind = %q, want %q", got.Kind, notify.KindSessionEnded)
	}
}

// And on an ordinary machine with no notify pipeline wired at all — the
// dev-web harness, the e2e stand, every other test in this package — the
// exit notification still arrives and nothing panics. This is the paired
// "and it succeeds" for the nil guard: a guard tested only by the path that
// skips it is a guard nobody proved harmless.
func TestSessionEndWithNoRaiserStillNotifiesExit(t *testing.T) {
	fake := newExitFakePTY()
	ws, _ := newSessionEndedServer(t, fake, nil)
	sid, conn := openExitSession(t, ws)

	fake.recordWait(realExitStatus(7))
	got := awaitExit(t, conn)

	if got.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, sid)
	}
	if got.Cause != string(session.ExitExited) {
		t.Errorf("cause = %q, want %q", got.Cause, session.ExitExited)
	}
	if got.Status == nil || *got.Status != 7 {
		t.Errorf("status = %v, want 7", got.Status)
	}
}

// --- the close the user asked for ---------------------------------------
//
// The other half of the interval. Every event that IS raised goes into the
// feed and policy decides only its channels, never its membership — so the
// correctness has to live in the SOURCE: a session end the user just
// requested is not news, and monitorExit must not raise for it. Without
// this, closing a tab filed a warning reading "Session on <host> was
// interrupted", because a forced teardown records no shell report and
// ExitOutcome correctly refuses to invent one.

// awaitCloseAndExit sends the close RPC and consumes BOTH of its
// consequences off the one socket: the RPC's result and the exit
// notification. They race, and the ordinary call helper drops
// notifications it passes on its way to a response — so both are read in
// one loop instead. Returns the exit notification's payload.
func awaitCloseAndExit(t *testing.T, conn *websocket.Conn, sid string, id int) exitWire {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "close",
		"params":  map[string]string{"sessionId": sid},
	})
	if err != nil {
		t.Fatalf("marshal close: %v", err)
	}
	if werr := conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write close: %v", werr)
	}

	deadline := time.Now().Add(wantWithin)
	var got exitWire
	seenExit, seenResult := false, false
	for !seenExit || !seenResult {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out: exit notification seen=%v, close result seen=%v", seenExit, seenResult)
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		_, msg, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read after close: %v", rerr)
		}
		var env struct {
			ID     *int             `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
			Error  *jsonrpcErrorObj `json:"error"`
		}
		if uerr := json.Unmarshal(msg, &env); uerr != nil {
			continue
		}
		switch {
		case env.ID != nil && *env.ID == id:
			if env.Error != nil {
				t.Fatalf("close: %+v", env.Error)
			}
			seenResult = true
		case env.Method == "exit":
			if uerr := json.Unmarshal(env.Params, &got); uerr != nil {
				t.Fatalf("exit params: unmarshal: %v", uerr)
			}
			seenExit = true
		}
	}
	return got
}

// A tab the user closed files nothing. The exit notification is unaffected:
// the renderer still learns the session is gone, and still learns it ended
// without a shell report — only the centre stays quiet, because the person
// reading it is the person who did it.
func TestSessionClosedByTheUserRaisesNoEvent(t *testing.T) {
	fake := newExitFakePTY()
	raiser := &fakeNotifyRaiser{}
	ws, reg := newSessionEndedServer(t, fake, raiser)
	sid, conn := openExitSession(t, ws)

	got := awaitCloseAndExit(t, conn, sid, 2)

	// The exit notification is unaffected — same payload as before the fix.
	if got.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, sid)
	}
	if got.Cause != string(session.ExitInterrupted) {
		t.Errorf("cause = %q, want %q — a forced teardown has no shell report", got.Cause, session.ExitInterrupted)
	}
	if got.Status != nil {
		t.Errorf("status = %v, want absent for a teardown", *got.Status)
	}
	// The raise precedes the notification in monitorExit, so reading the
	// notification has already ordered this read past the decision.
	if evs := raiser.captured(); len(evs) != 0 {
		t.Fatalf("raised %d events for a close the user asked for, want 0: %+v", len(evs), evs)
	}
	// And the marker did not survive its session: nothing is left keyed by
	// an id that will never be seen again.
	if _, err := reg.Get(session.ID(sid)); err == nil {
		t.Error("registry still holds the session after close")
	}
	ws.ringsMu.Lock()
	left := len(ws.closeRequested)
	ws.ringsMu.Unlock()
	if left != 0 {
		t.Errorf("closeRequested holds %d entries after the exit consumed the marker, want 0", left)
	}
}

// The other end: an end the user did NOT ask for still raises exactly one
// event, both ways a session can end on its own. A suppression keyed by a
// marker is only correct if the unmarked paths are unchanged, and the
// dropped connection is the case the bell exists for.
func TestSessionEndTheUserDidNotAskForStillRaises(t *testing.T) {
	cases := map[string]struct {
		wait      error
		wantLevel notify.Level
	}{
		"the shell exited":         {wait: realExitStatus(1), wantLevel: notify.LevelWarning},
		"the connection dropped":   {wait: &gossh.ExitMissingError{}, wantLevel: notify.LevelWarning},
		"the shell exited cleanly": {wait: nil, wantLevel: notify.LevelSuccess},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newExitFakePTY()
			raiser := &fakeNotifyRaiser{}
			ws, _ := newSessionEndedServer(t, fake, raiser)
			_, conn := openExitSession(t, ws)

			fake.recordWait(tc.wait)
			awaitExit(t, conn)

			evs := raiser.captured()
			if len(evs) != 1 {
				t.Fatalf("raised %d events, want exactly 1", len(evs))
			}
			if evs[0].Level != tc.wantLevel {
				t.Errorf("Level = %q, want %q", evs[0].Level, tc.wantLevel)
			}
		})
	}
}

// The wording has ONE owner, and both kinds of end read as themselves: a
// remote end names its host, a local end says it was local. Asserted at the
// function rather than through the socket because the pipeline above opens
// local sessions only — a remote exit needs an ssh stand this suite does not
// have — and the defect was in the sentence, not in the plumbing.
//
// Before this, a local end rendered "Session on  ended": the hostless format
// left a double space in the centre's first source, and the panel's meta line
// opened with " · ".
func TestSessionEndedTitleNamesLocalAndRemoteEnds(t *testing.T) {
	cases := []struct {
		name  string
		host  string
		cause session.ExitCause
		want  string
	}{
		{name: "a local exit", host: "", cause: session.ExitExited, want: "Local session ended"},
		{name: "a local loss", host: "", cause: session.ExitInterrupted, want: "Local session was interrupted"},
		{name: "a remote exit", host: "prod-1.example.com", cause: session.ExitExited, want: "Session on prod-1.example.com ended"},
		{name: "a remote loss", host: "prod-1.example.com", cause: session.ExitInterrupted, want: "Session on prod-1.example.com was interrupted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionEndedTitle(tc.host, tc.cause); got != tc.want {
				t.Errorf("sessionEndedTitle(%q, %q) = %q, want %q", tc.host, tc.cause, got, tc.want)
			}
			if strings.Contains(sessionEndedTitle(tc.host, tc.cause), "  ") {
				t.Errorf("title %q contains a double space — the hostless gap this bead closed", sessionEndedTitle(tc.host, tc.cause))
			}
		})
	}
}
