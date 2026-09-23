package session

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// The session runtime, in the helper (ADR-0066, nocx-ygxjv.12).
//
// A session's terminal state is not the property of whichever window happens
// to be open, so the one emulator lives BESIDE THE PTY: it is created with the
// host session, before the first byte of output is read, and it is destroyed
// when the session ends. Nothing here depends on enrolment (ADR-0041's
// carve-out keeps its observation authority and loses only its monopoly on
// creating terminal state) or on a coordinator being connected — a program
// that asks its terminal a question is answered whether or not anybody is
// watching, which is the whole point of the runtime living here rather than in
// the coordinator.
//
// # Why this package, and not nocx-server
//
// The runtime reaches libghostty-vt through a CGo binding, so whoever imports
// this package needs CGO_ENABLED=1. nocx-server does not (go list -deps: zero),
// and it must not start to: the helper is a separate binary and the PTY whose
// screen this is lives in THIS process.

// ScreenFactory builds the emulator a session's runtime is created over: the
// parser, the modes, the cells and the answers to the program's own questions
// (ADR-0065 chooses which emulator; this is the seam the choice is reached
// from).
//
// It is an ALIAS of paneview.ScreenFactory and not a second declaration of the
// same signature: the replay op builds its terminal over the same seam, and two
// named function types with one signature would be two vocabularies for one
// choice — the shape AD-8 forbids, one step away from two emulators.
//
// [Options.Screen] is nil in production and [defaultScreen] is what that
// means, so the choice is named exactly once. A test replaces it when it needs
// a screen whose behaviour it can read back — the same shape as Options.Now
// and Options.NewID, and the reason the seam exists rather than a direct call
// to the adapter at the use site.
type ScreenFactory = paneview.ScreenFactory

// defaultScreen is ADR-0065's adapter behind ADR-0066's placement.
var defaultScreen ScreenFactory = ghostty.New

// ptyTerminal is the runtime's write boundary — [sessionruntime.Terminal] —
// over this package's own [Process]: the bytes the runtime decided, and the
// size it commits.
//
// It takes NO lock of the host session's, and that is load-bearing rather than
// incidental. Its Write is what a reply to the program's own query travels on,
// and it is called from the pump's ingest path — while hostSession.write holds
// s.mu from lease validation through proc.Write. A terminal that took s.mu here
// would deadlock the moment a reply could not be delivered at once: the pump
// would park behind the very write it exists to drain, and a program that is
// blocked writing has stopped reading the answer that would unblock it.
type ptyTerminal struct{ proc Process }

var _ sessionruntime.Terminal = ptyTerminal{}

func (t ptyTerminal) Write(p []byte) (int, error) { return t.proc.Write(p) }

// Resize applies a committed size to the PTY. The context is Background
// because the runtime's geometry commit takes none: this is an ioctl on an fd
// this process owns, and there is no partial state for a cancellation to leave
// behind.
//
// The ioctl's pixel fields are the WHOLE text area (TIOCSWINSZ's
// ws_xpixel/ws_ypixel), while the committed geometry's are PER CELL — the
// reverse half of the one conversion cellGeometry owns, and the reason the
// multiplication happens here rather than at the commit.
func (t ptyTerminal) Resize(g emulator.Geometry) error {
	cols, err := ptyDimension(g.Cols)
	if err != nil {
		return err
	}
	rows, err := ptyDimension(g.Rows)
	if err != nil {
		return err
	}
	xpixel, err := ptyDimension(g.CellWidthPx * g.Cols)
	if err != nil {
		return err
	}
	ypixel, err := ptyDimension(g.CellHeightPx * g.Rows)
	if err != nil {
		return err
	}
	return t.proc.Resize(context.Background(), cols, rows, xpixel, ypixel)
}

// ptyDimension converts a committed cell count to the width the kernel's own
// ioctl carries. The runtime validates the geometry before it commits it, so a
// value that does not fit is a bug here rather than a caller error — and it is
// refused rather than truncated, because a silent wrap would resize the
// terminal to a number nobody asked for.
func ptyDimension(n int) (uint16, error) {
	if n < 0 || n > math.MaxUint16 {
		return 0, fmt.Errorf("session: %d does not fit the kernel's uint16", n)
	}
	return uint16(n), nil
}

// cellGeometry turns a client's reported size — cells plus TIOCSWINSZ's
// whole-text-area pixels, zero meaning "not measured yet" — into the
// geometry the runtime commits, whose pixel fields are PER CELL. This is the
// ONE boundary between the two units: the wire (proto.SpawnParams,
// proto.ResizeParams) and the pty's winsize speak whole-area pixels because
// that is what TIOCSWINSZ defines, and everything downstream of a commit —
// the emulator's own size answers, the published frames, the client's
// pixel-to-cell mapping — speaks per cell.
//
// The decode rounds to the nearest whole pixel per cell, so the frame never
// carries a fraction the wire could not have meant: a report of
// cols × c pixels decodes back to c for a whole c, and to within half a
// pixel for anything else — which is all a uint16 report ever carried.
// Zeros decode to zeros and are legitimate: a client that has not measured
// itself reports no metric, and the session keeps running with none rather
// than inventing one.
func cellGeometry(cols, rows, xpixel, ypixel uint16) sessionruntime.Geometry {
	g := sessionruntime.Geometry{Cols: int(cols), Rows: int(rows)}
	if cols > 0 && xpixel > 0 {
		g.CellWidthPx = int(math.Round(float64(xpixel) / float64(cols)))
	}
	if rows > 0 && ypixel > 0 {
		g.CellHeightPx = int(math.Round(float64(ypixel) / float64(rows)))
	}
	return g
}

// newSessionRuntime builds the runtime a spawned session is directed by, over
// the PTY it was just started on, and answers the screen as well because the
// caller owns that object's lifetime.
//
// Its predecessors in spawn's order have already happened: the process exists
// and not one byte of its output has been read. That is what lets completeness
// be stated rather than assumed — this runtime sees the stream from its first
// byte — and it is the reason the runtime is created HERE rather than lazily
// on the first attach, where a program's question asked before anybody
// attached would have been answered by nobody.
// expireIn and expireAfter are the bounded missing-fence wait the runtime's
// rendezvous runs under (design §6.4) — the service's Options threading,
// handed down so the policy is stated at the composition root and a test's
// trigger is the one the session actually arms.
func newSessionRuntime(newScreen ScreenFactory, proc Process, id string, cols, rows, xpixel, ypixel uint16, expireIn time.Duration, expireAfter func(d time.Duration, f func()) (stop func() bool)) (*sessionruntime.Session, emulator.Terminal, error) {
	g := cellGeometry(cols, rows, xpixel, ypixel)
	screen, err := newScreen(g)
	if err != nil {
		return nil, nil, err
	}
	rt, err := sessionruntime.New(sessionruntime.Config{
		// The incarnation is the session the PTY belongs to, at generation 1:
		// the helper mints one session per PTY, so generation counts restarts
		// of a session that this process has exactly one of.
		Incarnation: sessionruntime.Incarnation{
			Session:    sessionruntime.SessionID(id),
			Generation: 1,
		},
		Geometry:         g,
		Terminal:         ptyTerminal{proc: proc},
		Emulator:         screen,
		Completeness:     sessionruntime.CompletenessComplete,
		RendezvousExpiry: expireIn,
		ExpireAfter:      expireAfter,
	})
	if err != nil {
		// The screen was built and the runtime refused it, so the screen is
		// this call's to release: nothing downstream ever saw it.
		screen.Close()
		return nil, nil, err
	}
	return rt, screen, nil
}
