package session

// The end-to-end shape of the commit-point hook (nocx-6q1uh.4, spec §6.2,
// owner.go's tokenGate/recordTokenOutcome and tokens.go's checkToken): a
// token minted from a real snapshot is spent exactly once through the real
// owner and the real runtime — Admit, Commit, the write, and the record a
// replay answers from afterward — with nothing here standing in for any of
// sessionruntime's own machinery.

import (
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// submitTokenIntent is this test's own shape of a wire-driven session.intent
// (nocx-6q1uh.5 wires the real op): a fresh pendingIntent carrying tok and
// canon, with checkToken(tok) as its Check — exactly what that future caller
// is expected to build.
func submitTokenIntent(t *testing.T, hs *hostSession, control sessionruntime.Control, tok Token, canon canonicalIntent) ownerResult {
	t.Helper()
	done, err := hs.owner.submit(ownerItem{
		kind: itemIntent,
		intent: &pendingIntent{
			Intent: sessionruntime.Intent{
				At:      tok.At,
				Under:   control.Epoch,
				By:      control.Holder,
				Kind:    canon.Kind,
				Payload: canon.Payload,
			},
			Check:     checkToken(tok),
			Token:     tok,
			Canonical: canon,
			CommitBy:  math.MaxInt64,
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case res := <-done:
		return res
	case <-time.After(hangLimit):
		t.Fatal("the owner never resolved the submitted intent")
		return ownerResult{}
	}
}

func TestACommitPointSpendsATokenExactlyOnce(t *testing.T) {
	clock := newFakeClock()
	proc := newScriptedProcess("")
	rt := pumpRuntime(t, proc)
	hs := &hostSession{
		proc:    proc,
		win:     newWindow(2 * creditLimit),
		runtime: rt,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:     clock.Now,
	}
	owner := newSessionOwner(proc, rt, hs.win, hs.log)
	rt.SetReplies(owner)
	hs.owner = owner
	hs.tokens = newTokenBook(rt.Incarnation(), clock.Now)
	owner.SetTokens(hs.tokens)
	go owner.run()
	t.Cleanup(func() { owner.stop(true, time.Time{}) })

	control, err := rt.GrantControl(sessionruntime.Principal{Kind: sessionruntime.PrincipalAgent, ID: "coordinator"})
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}

	snap, err := hs.takeSnapshot()
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	tok, err := hs.mintTarget(snap.ID, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 0})
	if err != nil {
		t.Fatalf("mintTarget: %v", err)
	}

	canon := canonicalIntent{Kind: sessionruntime.IntentKindText, Payload: []byte("hi"), AccessEpoch: tok.AccessEpoch}

	first := submitTokenIntent(t, hs, control, tok, canon)
	if first.State != sessionruntime.IntentStateExecuted {
		t.Fatalf("first submission: state=%v err=%v, want Executed", first.State, first.Err)
	}
	written := proc.awaitWrite(t)
	if string(written) != "hi" {
		t.Fatalf("the program received %q, want %q", written, "hi")
	}

	// A second submission of the SAME token and the SAME canonical intent
	// replays the recorded result rather than writing a second time (spec
	// §6.2).
	replay := submitTokenIntent(t, hs, control, tok, canon)
	if replay.State != sessionruntime.IntentStateExecuted || replay.BytesWritten != first.BytesWritten {
		t.Fatalf("replay: got state=%v bytesWritten=%d, want state=%v bytesWritten=%d",
			replay.State, replay.BytesWritten, first.State, first.BytesWritten)
	}
	select {
	case again := <-proc.written:
		t.Fatalf("the replay wrote to the program a second time: %q", again)
	default:
	}

	// A DIFFERENT canonical intent under the same token is refused
	// token_spent, and writes nothing.
	other := canonicalIntent{Kind: sessionruntime.IntentKindText, Payload: []byte("bye"), AccessEpoch: tok.AccessEpoch}
	refused := submitTokenIntent(t, hs, control, tok, other)
	if refused.State != sessionruntime.IntentStateRefused || !errors.Is(refused.Err, ErrTokenSpent) {
		t.Fatalf("a different intent under a spent token: got state=%v err=%v, want Refused/ErrTokenSpent",
			refused.State, refused.Err)
	}
	select {
	case again := <-proc.written:
		t.Fatalf("the refused attempt wrote to the program: %q", again)
	default:
	}

	if state, r := hs.tokens.Status(tok.ID); state != "recorded" || r == nil || r.BytesWritten != first.BytesWritten {
		t.Fatalf("status after the exchange: got (%q, %v), want the recorded result", state, r)
	}
}
