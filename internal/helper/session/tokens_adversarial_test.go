package session

// Independent adversarial tests for tokenBook and checkToken (nocx-6q1uh.13,
// "the owner and token" part). Written from the spec (§6.2) against the
// merged tree at nocx-6q1uh.6, deliberately stronger or differently-shaped
// than tokens_test.go's own coverage rather than a duplicate of it — each
// test's own doc says which existing test it extends and why the extension
// is not redundant.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// ---------------------------------------------------------------------------
// Forgery: an altered kind, a token from another SESSION (not merely another
// generation of the same one).
// ---------------------------------------------------------------------------

// TestVerifyRefusesAnAlteredTargetKind completes tokens_test.go's own
// TestVerifyRefusesATamperedOrExpiredToken, which tampers the MAC and the
// row range but never the kind — a field the MAC signs (tokens.go's mac
// method, writeMacString(h, string(t.Kind))) but that this book's own Verify
// never inspects directly.
func TestVerifyRefusesAnAlteredTargetKind(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book) // minted as sessionruntime.TargetRegion

	bad := tok
	bad.Kind = sessionruntime.TargetInput
	if err := book.Verify(bad); !errors.Is(err, ErrForged) {
		t.Fatalf("a token with its kind altered: got %v, want ErrForged", err)
	}
}

// TestVerifyRefusesATokenFromAnotherSession is tokens_test.go's own
// TestVerifyRefusesATokenFromAnotherIncarnationsKey restated over two
// genuinely distinct sessions (different sessionruntime.SessionID AND
// different proto.HostSessionID), rather than two generations of one
// session — the wording spec §6.2 and this task's own brief use ("a token
// presented for another session"). The book's key is drawn per incarnation
// (newTokenBook), so this is the same mechanism under a different scenario,
// worth stating explicitly since bindSession's own field (Token.Session) is
// carried into the MAC but never compared against b.session directly —
// cross-session forgery is caught ONLY because the two books never share a
// key, which this test is what actually checks.
func TestVerifyRefusesATokenFromAnotherSession(t *testing.T) {
	sessionA := newTokenBook(sessionruntime.Incarnation{Session: "sess-A", Generation: 1}, time.Now)
	sessionA.bindSession(proto.HostSessionID{Session: "host-a-session-id", Generation: proto.GenerationID("gen-a")})
	sessionB := newTokenBook(sessionruntime.Incarnation{Session: "sess-B", Generation: 1}, time.Now)
	sessionB.bindSession(proto.HostSessionID{Session: "host-b-session-id", Generation: proto.GenerationID("gen-b")})

	tok := mintOne(t, sessionA)
	if err := sessionB.Verify(tok); !errors.Is(err, ErrForged) {
		t.Fatalf("a token minted for one session, verified against a different one: got %v, want ErrForged", err)
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentConsumeOfTheSameIntentAt64GoroutinesLetsExactlyOneProceed
// ---------------------------------------------------------------------------

// TestConcurrentConsumeOfTheSameIntentAt64GoroutinesLetsExactlyOneProceed is
// tokens_test.go's own TestConcurrentConsumeOfTheSameIntentLetsExactlyOneProceed
// at this task's own named schedule size (64 goroutines), rather than that
// test's 16 — a worse interleaving, per this task's brief.
func TestConcurrentConsumeOfTheSameIntentAt64GoroutinesLetsExactlyOneProceed(t *testing.T) {
	clock := newFakeClock()
	book := newTokenBook(testIncarnation(), clock.Now)
	tok := mintOne(t, book)
	canon := canonicalIntent{Kind: sessionruntime.IntentKindKey, Payload: []byte("same-64")}

	const n = 64
	var wg sync.WaitGroup
	var proceeded, other, unexpected int32
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorded, inProgress, err := book.Consume(tok.ID, canon)
			switch {
			case err != nil:
				atomic.AddInt32(&unexpected, 1)
			case recorded == nil && !inProgress:
				atomic.AddInt32(&proceeded, 1)
			default:
				atomic.AddInt32(&other, 1)
			}
		}()
	}
	wg.Wait()

	if proceeded != 1 {
		t.Fatalf("proceeded = %d, want exactly 1 (unexpected = %d, other = %d)", proceeded, unexpected, other)
	}
	if unexpected != 0 {
		t.Fatalf("a same-intent Consume returned an error %d times, want 0", unexpected)
	}
	if other != n-1 {
		t.Fatalf("inProgress-or-recorded = %d, want %d", other, n-1)
	}
}

// ---------------------------------------------------------------------------
// checkToken's digest sensitivity: identity changes are global, content
// changes are row-local. Direct, pure tests of checkToken/Digest — no
// owner, no runtime, no PTY needed.
// ---------------------------------------------------------------------------

func textRow(s string) emulator.Row {
	cells := make([]emulator.Cell, len(s))
	for i := 0; i < len(s); i++ {
		cells[i] = emulator.Cell{Grapheme: string(s[i]), Width: emulator.WidthNarrow, HasText: true}
	}
	return emulator.Row{Cells: cells}
}

// buildToken builds a Token directly from a digest computed the same way
// tokenBook.Mint does — everything checkToken actually reads (Identity,
// Rows, Digest, IncludeCursor) — without needing a book, a MAC or a mint
// call: these tests are about the digest's own sensitivity, not about the
// book's bookkeeping around it.
func buildToken(identity sessionruntime.ScreenIdentity, rows []emulator.Row, cur emulator.Cursor, kind sessionruntime.TargetKind, rr sessionruntime.RowRange) Token {
	includeCursor := kind == sessionruntime.TargetInput
	return Token{
		Identity:      identity,
		Kind:          kind,
		Rows:          rr,
		Digest:        sessionruntime.Digest(identity, rows, rr, cur, includeCursor),
		IncludeCursor: includeCursor,
	}
}

func checkSnapshot(identity sessionruntime.ScreenIdentity, rows []emulator.Row, cur emulator.Cursor) sessionruntime.Snapshot {
	return sessionruntime.Snapshot{Identity: identity, Rows: rows, Cursor: cur}
}

// TestCheckTokenRefusesOnAltScreenSwitchRegardlessOfRows is spec §6.2's
// "buffer switch ... since the snapshot -> incomparable", checked against
// TWO targets — one on the rows nearest whatever changed, one on rows
// nothing ever touches — to show the refusal is NOT row-scoped the way a
// content change is (digest.go's own doc: "a spinner two rows below a menu
// must never invalidate that menu's target" — but an identity change
// invalidates every live token on that screen, including the spinner's own).
func TestCheckTokenRefusesOnAltScreenSwitchRegardlessOfRows(t *testing.T) {
	inc := testIncarnation()
	before := sessionruntime.ScreenIdentity{At: inc, Cols: 10, Rows: 6}
	rows := []emulator.Row{textRow("menu-line-"), textRow("option-one"), textRow("spinner---"), textRow("unrelated1")}
	cur := emulator.Cursor{}

	near := buildToken(before, rows, cur, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 1})
	far := buildToken(before, rows, cur, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 3, Last: 3})

	after := before
	after.AltScreen = true // rows unchanged; only the buffer identity moved

	for name, tok := range map[string]Token{"target on rows near the change": near, "target on rows never touched": far} {
		t.Run(name, func(t *testing.T) {
			if err := checkToken(tok)(checkSnapshot(after, rows, cur)); !errors.Is(err, errIncomparable) {
				t.Fatalf("%s: got %v, want errIncomparable", name, err)
			}
		})
	}
}

// TestCheckTokenRefusesOnResizeRegardlessOfRows is the same structural
// promise for a geometry change instead of a buffer switch.
func TestCheckTokenRefusesOnResizeRegardlessOfRows(t *testing.T) {
	inc := testIncarnation()
	before := sessionruntime.ScreenIdentity{At: inc, Cols: 10, Rows: 6}
	rows := []emulator.Row{textRow("menu-line-"), textRow("option-one"), textRow("spinner---"), textRow("unrelated1")}
	cur := emulator.Cursor{}

	near := buildToken(before, rows, cur, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 1})
	far := buildToken(before, rows, cur, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 3, Last: 3})

	after := before
	after.Cols = 20

	for name, tok := range map[string]Token{"target on rows near the change": near, "target on rows never touched": far} {
		t.Run(name, func(t *testing.T) {
			if err := checkToken(tok)(checkSnapshot(after, rows, cur)); !errors.Is(err, errIncomparable) {
				t.Fatalf("%s: got %v, want errIncomparable", name, err)
			}
		})
	}
}

// TestCheckTokenRefusesOnAStyleOnlyChangeWithinItsOwnRowsButNotElsewhere is
// spec §6.2's "the same screen, different content" (stale_target), paired
// with digest.go's own row-locality promise: a style-only change (no
// grapheme change at all) inside a target's own rows invalidates it, while
// an unrelated target elsewhere survives even while THAT change AND an
// unrelated spinner tick both happen outside its own rows in the same
// frame.
func TestCheckTokenRefusesOnAStyleOnlyChangeWithinItsOwnRowsButNotElsewhere(t *testing.T) {
	inc := testIncarnation()
	identity := sessionruntime.ScreenIdentity{At: inc, Cols: 10, Rows: 6}
	before := []emulator.Row{textRow("menu-line-"), textRow("option-one"), textRow("spinner---"), textRow("unrelated1")}
	cur := emulator.Cursor{}

	menuTok := buildToken(identity, before, cur, sessionruntime.TargetMenu, sessionruntime.RowRange{First: 0, Last: 1})
	elsewhereTok := buildToken(identity, before, cur, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 3, Last: 3})

	after := make([]emulator.Row, len(before))
	copy(after, before)
	styledRow0 := make([]emulator.Cell, len(before[0].Cells))
	copy(styledRow0, before[0].Cells)
	styledRow0[0].Style.Attributes |= emulator.AttrInverse // a selection highlight, say — no grapheme change
	after[0] = emulator.Row{Cells: styledRow0}
	after[2] = textRow("SPINNING!!") // an unrelated spinner tick, outside every token's own rows

	if err := checkToken(menuTok)(checkSnapshot(identity, after, cur)); !errors.Is(err, errStaleTarget) {
		t.Fatalf("menu target over the styled row: got %v, want errStaleTarget", err)
	}
	if err := checkToken(elsewhereTok)(checkSnapshot(identity, after, cur)); err != nil {
		t.Fatalf("target on untouched rows while row 0's style and the spinner both changed: got %v, want nil", err)
	}
}

// TestCheckTokenRefusesOnACursorOnlyMoveForInputButNotForRegion is spec
// §6.2's cursor sensitivity: only an `input` target folds the cursor into
// its digest (tokens.go's Mint: includeCursor := kind == TargetInput), so a
// cursor-only move (no cell changes at all) invalidates an `input` target
// and never a `region` target over the identical rows and the identical
// move.
func TestCheckTokenRefusesOnACursorOnlyMoveForInputButNotForRegion(t *testing.T) {
	inc := testIncarnation()
	identity := sessionruntime.ScreenIdentity{At: inc, Cols: 10, Rows: 6}
	rows := []emulator.Row{textRow("input-box-")}
	before := emulator.Cursor{X: 0, Y: 0, Visible: true}
	after := emulator.Cursor{X: 5, Y: 0, Visible: true}

	inputTok := buildToken(identity, rows, before, sessionruntime.TargetInput, sessionruntime.RowRange{First: 0, Last: 0})
	regionTok := buildToken(identity, rows, before, sessionruntime.TargetRegion, sessionruntime.RowRange{First: 0, Last: 0})

	if err := checkToken(inputTok)(checkSnapshot(identity, rows, after)); !errors.Is(err, errStaleTarget) {
		t.Fatalf("input target after a cursor-only move: got %v, want errStaleTarget", err)
	}
	if err := checkToken(regionTok)(checkSnapshot(identity, rows, after)); err != nil {
		t.Fatalf("region target after the SAME cursor-only move: got %v, want nil (region never includes the cursor)", err)
	}
}

// TestCheckTokenIgnoresASpinnerOutsideItsOwnRowsAcrossManyFrames is
// digest.go's own promise stated directly, driven across several distinct
// spinner frames rather than one: a menu target's digest must never move no
// matter how many times, or how differently, an unrelated row two lines
// below it redraws.
func TestCheckTokenIgnoresASpinnerOutsideItsOwnRowsAcrossManyFrames(t *testing.T) {
	inc := testIncarnation()
	identity := sessionruntime.ScreenIdentity{At: inc, Cols: 10, Rows: 6}
	base := []emulator.Row{textRow("menu-line-"), textRow("option-one"), textRow("spinner---")}
	cur := emulator.Cursor{}

	tok := buildToken(identity, base, cur, sessionruntime.TargetMenu, sessionruntime.RowRange{First: 0, Last: 1})

	frames := []string{"spinner1--", "SPIN!SPIN!", "..........", "spinner---"}
	for i, spin := range frames {
		rows := make([]emulator.Row, len(base))
		copy(rows, base)
		rows[2] = textRow(spin)
		if err := checkToken(tok)(checkSnapshot(identity, rows, cur)); err != nil {
			t.Fatalf("spinner frame %d (%q) outside the menu's own rows invalidated it: %v", i, spin, err)
		}
	}
}
