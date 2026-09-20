package sessionruntime

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// The rendezvous join, on a REAL PTY: a program's output carries the render
// fence (OSC 1337, NOCX_FENCE, ADR-0024 §7), the coordinator's authenticated
// completion arrives through [Session.Completed], and the runtime is what
// joins the two halves (design §6.4). Nothing here is mocked below the
// contract: the fence bytes are written by a real shell onto a real pty and
// ingested by the real emulator, exactly as the helper's pump would feed
// them.
//
// Nothing here waits on a duration. Every wait ends on an observable state —
// the screen holding a sentinel, the rendezvous reaching a state, the program's
// output ending — and the hang limit only ends a wait that can no longer be
// satisfied.
//
// A sighted marker authorises nothing (ADR-0024 decision 1): the fence-only
// and forged tests below pin that the rendezvous stays parked or idle and
// completeness never claims a boundary the authenticated half never drew.

const fenceNonceHex = "ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01ab01"

// fenceSeq is the exact byte sequence the shell writes after a command's
// output (internal/shellintegration/scripts/nocx.bash) and the one sequence
// the emulator's adapter matches.
func fenceSeq(nonceHex string) string {
	return "\x1b]1337;NOCX_FENCE;" + nonceHex + "\x07"
}

// rendezvousOf reads one session's meeting by nonce — the keyed read, since
// the rendezvous is a set: two commands can be pending at once, and "the"
// rendezvous is not a thing a caller can name. The white-box shape is the
// same one lifecycle_complete_internal_test.go takes on the helper side: the
// meeting is the observable a joined pair of halves lands in.
func rendezvousOf(s *Session, nonce FenceNonce) Rendezvous {
	return s.RendezvousFor(nonce)
}

// waitForRendezvous waits until the session's meeting for nonce reaches want
// and returns it. Like waitForScreen, the condition is an observable state
// change re-read after each report from the pump; the hang limit only ends a
// wait that can no longer be satisfied, and a pump that has ended says so.
func waitForRendezvous(t *testing.T, s *Session, changed <-chan struct{}, done <-chan error, nonce FenceNonce, want RendezvousState) Rendezvous {
	t.Helper()
	deadline := time.After(hangLimit)
	for {
		rv := rendezvousOf(s, nonce)
		if rv.State == want {
			return rv
		}
		select {
		case err := <-done:
			t.Fatalf("the pump ended while waiting for the rendezvous to reach %s: err=%v, it is %s",
				rendezvousStateName(want), err, rendezvousStateName(rv.State))
		case <-changed:
		case <-deadline:
			t.Fatalf("the rendezvous never reached %s; it is %s", rendezvousStateName(want), rendezvousStateName(rv.State))
		}
	}
}

// waitForScreenGone waits until the screen no longer holds want: the
// overwrite-or-trim half of design §6.4, observable instead of assumed.
func waitForScreenGone(t *testing.T, s *Session, changed <-chan struct{}, done <-chan error, want string) {
	t.Helper()
	deadline := time.After(hangLimit)
	for {
		if !strings.Contains(string(s.Snapshot().Screen), want) {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("the pump ended while waiting for %q to leave the screen: err=%v", want, err)
		case <-changed:
		case <-deadline:
			t.Fatalf("%q never left the screen; the program's overwrite produced nothing", want)
		}
	}
}

// rendezvousStateName names a rendezvous state for a failure message, the way
// the contract's own failure strings do.
func rendezvousStateName(st RendezvousState) string {
	switch st {
	case RendezvousIdle:
		return "idle"
	case RendezvousAwaitingSighting:
		return "awaiting-sighting"
	case RendezvousAwaitingAuthenticated:
		return "awaiting-authenticated"
	case RendezvousComplete:
		return "complete"
	case RendezvousExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Both arrival orders end complete (design §6.4: neither order is the error
// case — SSH orders the two channels independently).
// ---------------------------------------------------------------------------

// completionFirstProgram prints, then waits for one byte BEFORE writing the
// fence: the test delivers the authenticated completion while no fence byte
// exists anywhere in the stream, so the ordering completion-then-fence holds
// by construction and not by timing.
const completionFirstProgram = rawPreamble + `
printf 'WAIT-FOR-COMPLETE'
readhex 1 >/dev/null
printf '\033]1337;NOCX_FENCE;` + fenceNonceHex + `\007'
printf 'FENCE-WRITTEN\n'
`

func TestACompletionBeforeItsFenceStillEndsCompleteOnARealPTY(t *testing.T) {
	p := startProgram(t, completionFirstProgram, harnessGeometry(80, 24))
	p.wait("WAIT-FOR-COMPLETE")

	nonce := fenceNonceFromString(t, fenceNonceHex)
	p.s.Completed(p.s.Incarnation(), nonce, 3)
	if rv := rendezvousOf(p.s, nonce); rv.State != RendezvousAwaitingSighting {
		t.Fatalf("after the authenticated completion the rendezvous is %s, want awaiting-sighting",
			rendezvousStateName(rv.State))
	}

	// The program's next byte is the fence: once the screen says the program
	// wrote past it, the emulator has sighted the fence and the meeting must
	// have closed.
	p.typed("go")
	p.wait("FENCE-WRITTEN")
	rv := waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousComplete)
	if string(rv.PinnedSource) != "WAIT-FOR-COMPLETE" {
		t.Fatalf("the completed rendezvous pinned %q, want the row the fence was drawn over", rv.PinnedSource)
	}
}

// fenceFirstProgram writes the fence and then blocks: the sighting arrives
// first, the authenticated completion second.
const fenceFirstProgram = rawPreamble + `
printf 'fence-source-row'
printf '\033]1337;NOCX_FENCE;` + fenceNonceHex + `\007'
printf '\r\nFENCE-SIGHTED\n'
readhex 1 >/dev/null
printf 'AFTER-FENCE\n'
`

func TestAFenceBeforeItsCompletionStillEndsCompleteOnARealPTY(t *testing.T) {
	p := startProgram(t, fenceFirstProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	rv := waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	if string(rv.PinnedSource) != "fence-source-row" {
		t.Fatalf("the sighting pinned %q, want the content the fence was drawn over", rv.PinnedSource)
	}

	p.s.Completed(p.s.Incarnation(), nonce, 0)
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousComplete)
}

// ---------------------------------------------------------------------------
// The pinned source survives the interval (design §6.4): later output may
// overwrite or scroll the rows the fence was drawn on before the
// authenticated half arrives, and the content must not change when it does.
// ---------------------------------------------------------------------------

const fenceThenOverwriteProgram = rawPreamble + `
printf 'fence-source-row'
printf '\033]1337;NOCX_FENCE;` + fenceNonceHex + `\007'
printf '\r\n'
readhex 1 >/dev/null
i=0
while [ "$i" -lt 40 ]; do
	echo "overwrite line $i"
	i=$((i+1))
done
printf 'OVERWRITE-DONE\n'
`

func TestAnOverwrittenFenceStillYieldsTheContentItWasDrawnOver(t *testing.T) {
	p := startProgram(t, fenceThenOverwriteProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	rv := waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	if string(rv.PinnedSource) != "fence-source-row" {
		t.Fatalf("the sighting pinned %q, want the content at the fence", rv.PinnedSource)
	}

	// Forty lines over a twenty-four-row screen: the fenced row is rewritten
	// AND scrolled away before the completion arrives.
	p.typed("go")
	p.wait("OVERWRITE-DONE")
	waitForScreenGone(t, p.s, p.changed, p.done, "fence-source-row")

	p.s.Completed(p.s.Incarnation(), nonce, 0)
	waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousComplete)
	if got := rendezvousOf(p.s, nonce); string(got.PinnedSource) != "fence-source-row" {
		t.Fatalf("after the rows were overwritten the rendezvous pins %q, want the content at the fence", got.PinnedSource)
	}
}

// ---------------------------------------------------------------------------
// A stream marker authorises nothing (ADR-0024 decision 1). Asserted
// directly: the rendezvous never reaches complete, and completeness never
// claims a boundary the authenticated half did not draw.
// ---------------------------------------------------------------------------

// forgedMarkerProgram prints OSC 133 D — the standard prompt-marker whose C
// and D forms have no meaning to nocx (ADR-0024) — and nothing else. No
// NOCX_FENCE byte is in the stream at all.
const forgedMarkerProgram = rawPreamble + `
printf '\033]133;D;0\007'
printf 'FORGED-DONE\n'
`

func TestAForgedOSC133MarkerCompletesNothingOnARealPTY(t *testing.T) {
	p := startProgram(t, forgedMarkerProgram, harnessGeometry(80, 24))
	p.wait("FORGED-DONE")

	rv := rendezvousOf(p.s, fenceNonceFromString(t, fenceNonceHex))
	if rv.State != RendezvousIdle {
		t.Fatalf("a forged OSC 133 D left the rendezvous %s, want idle: a stream marker may locate nothing and complete nothing",
			rendezvousStateName(rv.State))
	}
	if len(rv.PinnedSource) != 0 {
		t.Fatalf("a forged marker pinned %q, want nothing pinned", rv.PinnedSource)
	}
	if got := p.s.Completeness(); got != CompletenessComplete {
		t.Fatalf("a forged marker left completeness %v, want unchanged complete", got)
	}
}

func TestAFenceWithNoCompletionCompletesNothingOnARealPTY(t *testing.T) {
	p := startProgram(t, fenceFirstProgram, harnessGeometry(80, 24))
	nonce := fenceNonceFromString(t, fenceNonceHex)

	// The sighting happens — that is the fence's whole licence — but the
	// meeting never closes: the state parks, completeness keeps the value
	// the authenticated half alone may change, and nothing is complete.
	rv := waitForRendezvous(t, p.s, p.changed, p.done, nonce, RendezvousAwaitingAuthenticated)
	if len(rv.PinnedSource) == 0 {
		t.Fatalf("the sighted fence pinned nothing, want the content it was drawn over: a sighting locates")
	}
	if got := p.s.Completeness(); got != CompletenessComplete {
		t.Fatalf("a fence alone moved completeness to %v, want unchanged complete: no authenticated boundary was drawn", got)
	}

	// BEFORE the expiry, write authority is intact: an intent commits.
	p.mustSend(IntentKindText, []byte("x"))

	// AT the expiry, the bounded wait elapses with nothing authenticated
	// behind the sighting: the meeting ends, the pin is dropped, and
	// completeness and write authority are UNTOUCHED — the expiry this test
	// once read only as a moment (nocx fix-rendezvous-8W4D: this read, taken
	// in a runtime with no expiry configured, is how blocker 1 got through).
	if err := p.s.ExpireRendezvous(nonce); err != nil {
		t.Fatalf("expire the unbacked sighting: %v", err)
	}
	if got := rendezvousOf(p.s, nonce).State; got != RendezvousExpired {
		t.Fatalf("the unbacked meeting is %s after its wait elapsed, want expired", rendezvousStateName(got))
	}
	if got := p.s.Completeness(); got != CompletenessComplete {
		t.Fatalf("expiring an unbacked sighting moved completeness to %v, want unchanged complete: a fence that authorised nothing may degrade nothing", got)
	}

	// AFTER the expiry, write authority is STILL intact — unauthenticated
	// output must never revoke the person's ability to type, for however
	// long the session lives.
	p.mustSend(IntentKindText, []byte("y"))
}

func fenceNonceFromString(t *testing.T, hexed string) FenceNonce {
	t.Helper()
	var n FenceNonce
	raw, err := hex.DecodeString(hexed)
	if err != nil || len(raw) != len(n) {
		t.Fatalf("build the test's fence nonce from %q: %v", hexed, err)
	}
	copy(n[:], raw)
	return n
}
