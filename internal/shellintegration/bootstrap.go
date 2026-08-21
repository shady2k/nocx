package shellintegration

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// The backend half of the frame protocol: the writer (design §6.1, §7).
//
// The loader and stage-1 read frames; this drives them. Two obligations are
// the writer's alone, because the far side cannot discharge them:
//
//	THE DEADLINES. Design §7 forbids remote sleep loops and remote work whose
//	duration the remote host decides, so a portable shell may not hold a timer
//	— which means ReceiverReadyDeadline and FrameCompletionDeadline are
//	enforced here or nowhere. They are driven by an injected timer, never by a
//	wall-clock sleep, so a test states "the deadline fires" instead of waiting
//	for it.
//
//	"NO BYTE AFTER A COMPLETE FRAME BECOMES SHELL INPUT." The reader consumes
//	exactly the declared length and stops; the writer must send nothing between
//	the end of a frame and the outcome it is waiting for. That is why the
//	timeout while WAITING FOR AN OUTCOME writes nothing at all.
//
// # What a deadline does, and why it is not a closed stdin
//
// A far side blocked in `dd` waiting for a header that never comes has no
// prompt and no way to get one. Closing our stdin would give it EOF and a
// named outcome — and would also end the session, because the native login
// shell it then execs would read EOF immediately. The user would lose the
// terminal to a protocol timeout, which is the opposite of fail-open.
//
// So a deadline that fires while the far side is BLOCKED READING sends
// abortHeader instead: exactly FrameHeaderLen bytes that are not a header of
// this protocol, so the reader names its protocol outcome and execs a native
// login shell with its stdin intact. The bytes are chosen to be harmless in
// the other place they could land — if the far side is not our loader at all,
// they are a shell comment.
// abortHeader is what a deadline sends to a far side blocked on a header
// read. It is FrameHeaderLen bytes so it satisfies exactly one pending read;
// it does not begin with FrameMagic, so the reader names
// OutcomeBootstrapProtocol; and it begins with '#' and ends with a newline,
// so if it lands anywhere else — a shell that is not running our loader — it
// is a comment and not a command. That last property is the reason it is not
// simply random bytes.
var abortHeader = func() []byte {
	b := []byte("#nocx-abort")
	for len(b) < FrameHeaderLen-1 {
		b = append(b, ' ')
	}
	return append(b, '\n')
}()

// AbortFrame is abortHeader, for a caller that must unblock a far side it is
// never going to send a frame to. The typed path reaches exactly that state:
// the user's own `ssh` connected and its loader is blocked on a header, and
// nocx then failed to prove ownership of the multiplex socket — so it may not
// deliver anything, and a far side left blocked would eat the user's next
// keystrokes as a frame. This is the one thing it may still send.
func AbortFrame() []byte { return append([]byte(nil), abortHeader...) }

// BootstrapStream is the session's byte streams as the bootstrap sees them:
// a line reader with a deadline, and a writer.
//
// It is an interface for two reasons. The transport is internal/ssh's — this
// package must not know what an SSH session is — and the deadline has to be
// enforced by whoever owns the read, because an io.Reader cannot be
// interrupted. The ssh side declares the identical interface and its
// implementation satisfies both.
type BootstrapStream interface {
	// ReadLine returns the next line with its line ending removed, or an
	// error. A timeout returns ErrBootstrapDeadline and leaves whatever
	// has been read where the next reader will find it: a deadline may
	// never consume the user's bytes.
	ReadLine(ctx context.Context, timeout time.Duration) (string, error)
	// Write writes to the session's input. It bypasses the input
	// quarantine, which exists to refuse the USER's bytes, not ours.
	Write(p []byte) (int, error)
}

// ErrBootstrapDeadline is what a BootstrapStream returns when the deadline
// passed before a line completed.
var ErrBootstrapDeadline = errors.New("shellintegration: bootstrap deadline")

// SecretSource mints the pair, at the one point in §6.1 where it may be
// minted.
//
// Design §6.1 fixes the order: frame 1 received and verified, THEN the
// lifecycle transport and its receiver fully ready, THEN the publish attempt
// at a terminal outcome, and only after all of those is anything minted. Mint
// is called at exactly that point — after StageReadyToken and after
// BootstrapPlan.Ordered has returned, and never before either — so the
// ordering is expressed in the code rather than in a comment.
//
// Steps 4 and 5 are NOT this interface's: they are the plan's barrier, because
// they order frame 2 rather than the mint and a session with nothing to mint
// waits behind them too. What is left here is the mint itself, and an error
// means NOTHING WAS MINTED: stage-1 is told so with a RefusalFrame rather than
// being left to time out — the shape that matters, because the alternative
// hands a bearer across a boundary before establishing that it has any use.
type SecretSource interface {
	Mint(ctx context.Context) (payload []byte, err error)
}

// SecretFunc adapts a function to SecretSource.
type SecretFunc func(ctx context.Context) ([]byte, error)

func (f SecretFunc) Mint(ctx context.Context) ([]byte, error) { return f(ctx) }

// BootstrapPlan is one session's delivery: the stage-1 bytes the carrier
// committed to, the ordering barrier frame 2 waits behind, and where the
// secret comes from when the time is right.
type BootstrapPlan struct {
	// Stage1 is frame 1's payload. StageDigest over exactly these bytes is
	// what the command carries, so the two are minted together or not at
	// all.
	Stage1 []byte
	// Ordered is §6.1 steps 4 and 5 as one barrier: it returns once the
	// lifecycle receiver has answered AND the publish attempt has reached a
	// terminal outcome, and it returns an error when nothing may be minted.
	//
	// It is a field of the PLAN rather than a step of the mint because that
	// is where §6.1 puts it. Step 5 precedes step 6 (the mint) and step 7
	// (frame 2 delivered) alike, and step 8 — the far side re-proving the
	// generation as it now stands — follows frame 2 on EVERY path. A session
	// with nothing to mint therefore waits here too: it has no bearer to
	// hand over and it still has a generation to be verified against.
	//
	// Nil means there is no ordering to wait for, which is true of a plan
	// with no publisher behind it and of the frame-protocol tests.
	Ordered func(ctx context.Context) error
	// Secret mints frame 2. Nil means "no lifecycle channel": stage-1 gets
	// a refusal frame, and nothing is minted anywhere.
	Secret SecretSource
}

// DeliverBootstrap drives one session's bootstrap to exactly one terminal
// outcome. It never returns before naming one, and it never writes a byte
// after the last frame it sent.
//
// The returned Outcome is the whole result: OutcomeBootstrapAccepted means the
// far side re-proved its generation and is exec'ing the integrated shell;
// anything else is a named refusal, and in every one of them the far side is
// on its way to a working native login shell (design D7). The caller re-enables
// input on this return, whatever it says — that is §5.3's "input is re-enabled
// on the terminal outcome, never on READY".
func DeliverBootstrap(ctx context.Context, lg log.Logger, s BootstrapStream, plan BootstrapPlan) Outcome {
	w := &bootstrapWriter{s: s, lg: lg}

	// 1. The receiver announces itself. Until LOADER_READY the far side has
	//    not yet taken the terminal — a frame sent before it would be
	//    echoed straight back at us.
	if out, ok := w.awaitToken(ctx, LoaderReadyToken, ReceiverReadyDeadline, OutcomeReceiverUnready); !ok {
		return out
	}

	// 2. Frame 1, header and payload in ONE write: nothing of ours may
	//    interleave between a header and the body it describes.
	if out, ok := w.writeFrame(FrameStageSeq, plan.Stage1); !ok {
		return out
	}

	// 3. Stage-1 is running: it verified against the digest the command
	//    carried, or the loader would have named an outcome instead.
	if out, ok := w.awaitToken(ctx, StageReadyToken, FrameCompletionDeadline, OutcomeBootstrapTimeout); !ok {
		return out
	}

	// 4. Only now is anything minted (design §6.1): frame 1 is verified, the
	//    receiver exists and the publish has settled — the last two behind
	//    the plan's own gate. A source that refuses hands stage-1 a
	//    NON-SECRET refusal — never a secret we then discard.
	//
	//    The seal is re-checked immediately before the mint and not only
	//    before the write, because minting is itself an act with a cost: it
	//    puts a live per-epoch bearer into backend memory for a session that
	//    has already ended. Rule 2 of §6.1 is "no frame is written after an
	//    observed terminal outcome"; this is the same rule one step earlier,
	//    where it is cheaper.
	if w.sealed() {
		return w.outcome
	}
	frame, refused, mintErr := frameTwo(ctx, plan)
	if mintErr != nil {
		lg.Warn("shellintegration: no capability was minted for this session; the shell starts without a channel",
			"error", mintErr)
	}
	if out, ok := w.writeFrame(FrameSecretSeq, frame); !ok {
		zero(frame)
		return out
	}
	// The attempt's own copy of the frame closes here, which is the FIRST of
	// the capability's per-copy confidentiality events (§5.3): the buffer has
	// been handed to the transport and this process has no further use for
	// it. The other copies close on their own events, and the validity
	// interval is separate from all of them.
	zero(frame)

	// 5. The terminal outcome. Nothing is written from here on, whatever
	//    happens: the far side is no longer blocked on us, so a byte now
	//    would be a byte after a complete frame.
	out := w.awaitOutcome(ctx, FrameCompletionDeadline)

	// 6. And when frame 2 was a REFUSAL, that refusal is the session's
	//    outcome — not the far side's report that its shell came up.
	//
	//    The two answer different questions. Stage-1 execs the launcher on a
	//    refusal exactly as it does on a secret, so an accepted bootstrap
	//    after a refusal frame is stage-1 telling the truth about the SHELL:
	//    it is up, it is integrated in the prompt sense, and it has no
	//    authenticated channel. Only this side knows the second half, and
	//    §6.4's row is written on it — "nothing minted; native shell;
	//    channel-unavailable". Letting `accepted` win reported a session as
	//    integrated when no domain would ever be established behind it, and
	//    left the axis in `starting` for the life of the tab, because the
	//    hard invalidation that moves it out runs only on a named refusal.
	//
	//    A far side that names a failure of its OWN keeps that name. It is
	//    the more specific answer and it is a second thing the user can act
	//    on: "there is no generation on this host" is not made truer or less
	//    actionable by our also having had no channel to offer.
	if refused != "" && out == OutcomeBootstrapAccepted {
		return refused
	}
	return out
}

// zero overwrites a frame buffer in place. It is not a defence against a
// process that can read this one's memory — §5.4 says plainly that active
// same-user inspection is undefeated — it is the closing event of one named
// copy, so that "the confidentiality interval closes at the LAST of the
// per-copy events" is a statement with code behind each of them rather than a
// summary nobody can check.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---------------------------------------------------------------------------
// The writer's state: §6.1's two rules against a forged readiness token
// ---------------------------------------------------------------------------

// bootstrapWriter is the driver's whole state, and it exists because both
// rules §6.1 adds are STATE and not a check that can live at a call site.
//
//	RULE 1 — each token of the closed set is accepted at most once and only
//	in its order. A repeat or an out-of-order token is a named bootstrap
//	failure (OutcomeBootstrapOutOfOrder), not a second trigger. Without it a
//	token the backend is not waiting for is silently dropped, so a forger
//	learns nothing from sending one and pays nothing for it — and a second
//	READY is indistinguishable from the first.
//
//	RULE 2 — no frame is written after an observed terminal outcome. This
//	alone converts the no-race attack into a race: the loader refuses on an
//	absent hasher, a digest mismatch or an unreachable /dev/fd/N, and a
//	STAGE_READY sent AFTERWARDS would otherwise make the backend mint and
//	write a capability into a session that reached no stage-1. §5.2's "after
//	a terminal outcome a frame is never recognised again" is a rule for the
//	FAR-SIDE READER; this is the matching rule for our writer, which was
//	missing.
//
// What is left after both is a genuine race — a forged STAGE_READY arriving
// BEFORE the honest refusal — and it cannot be closed by framing, because
// winning it requires writing the session's terminal, which is also enough to
// read the frame. What bounds it is §5.3's hard invalidation, one layer up: a
// refusal or a timeout invalidates the capability, so what a winner holds is a
// bearer that dies with the outcome it forged past.
type bootstrapWriter struct {
	s  BootstrapStream
	lg log.Logger
	// outcome is the terminal outcome once one has been OBSERVED, and the
	// seal: it is set exactly once and nothing is written or minted after.
	outcome Outcome
	// accepted records which tokens of the closed set have been consumed, in
	// order. The set is small and ordered, so the position is the whole
	// state: 0 = nothing yet, 1 = LOADER_READY consumed, 2 = STAGE_READY
	// consumed.
	accepted int
}

// tokenOrder is the closed set in the ONE order it may arrive in. A token is
// accepted only at its own index, which makes "at most once" and "only in its
// order" the same check rather than two.
var tokenOrder = []string{LoaderReadyToken, StageReadyToken}

// sealed reports whether a terminal outcome has been observed.
func (w *bootstrapWriter) sealed() bool { return w.outcome != "" }

// seal records the terminal outcome. The FIRST outcome wins: it is the one
// that actually ended the attempt, and a later one is either a forgery or the
// far side repeating itself.
func (w *bootstrapWriter) seal(o Outcome) Outcome {
	if w.outcome == "" {
		w.outcome = o
	}
	return w.outcome
}

// writeFrame writes one frame as a single Write, and refuses to write at all
// once the window is sealed (rule 2). ok=false means the caller is finished
// and the returned Outcome is terminal.
func (w *bootstrapWriter) writeFrame(seq int, payload []byte) (Outcome, bool) {
	if w.sealed() {
		w.lg.Debug("shellintegration: frame suppressed after a terminal outcome",
			"seq", seq, "outcome", w.outcome)
		return w.outcome, false
	}
	h, err := FrameHeader(seq, len(payload))
	if err != nil {
		w.lg.Warn("shellintegration: frame header could not be built", "seq", seq, "error", err)
		return w.seal(OutcomeBootstrapInterrupted), false
	}
	buf := make([]byte, 0, len(h)+len(payload))
	buf = append(buf, h...)
	buf = append(buf, payload...)
	n, werr := w.s.Write(buf)
	zero(buf)
	if werr != nil {
		w.lg.Warn("shellintegration: frame could not be written", "seq", seq, "error", werr)
		return w.seal(OutcomeBootstrapInterrupted), false
	}
	if n != len(h)+len(payload) {
		w.lg.Warn("shellintegration: short frame write", "seq", seq, "wrote", n, "want", len(h)+len(payload))
		return w.seal(OutcomeBootstrapInterrupted), false
	}
	return "", true
}

// awaitToken reads until the far side says want, names an outcome of its own,
// breaks rule 1, or the deadline passes. ok=false means the caller is finished
// and the returned Outcome is terminal.
//
// onDeadline is the outcome for a timeout, and the timeout also sends
// abortHeader: at both call sites the far side is blocked on a header read, so
// the abort is what turns a hang into a named refusal and a usable prompt. The
// abort is a write, so it too is refused once the window is sealed — and it
// cannot be reached with the window sealed, because a sealed window has
// already returned.
func (w *bootstrapWriter) awaitToken(ctx context.Context, want string, timeout time.Duration, onDeadline Outcome) (Outcome, bool) {
	for {
		line, err := w.s.ReadLine(ctx, timeout)
		if err != nil {
			if errors.Is(err, ErrBootstrapDeadline) {
				// The far side is waiting for bytes that are not
				// coming. Unblock it with something it must
				// refuse, so it reaches a prompt.
				if !w.sealed() {
					if _, werr := w.s.Write(abortHeader); werr != nil {
						w.lg.Debug("shellintegration: abort header could not be written", "error", werr)
					}
				}
				w.lg.Warn("shellintegration: bootstrap deadline passed", "waiting_for", want, "outcome", onDeadline)
				return w.seal(onDeadline), false
			}
			w.lg.Warn("shellintegration: bootstrap stream ended before the far side answered",
				"waiting_for", want, "error", err)
			return w.seal(OutcomeBootstrapInterrupted), false
		}
		if out, ok := outcomeInLine(line); ok {
			return w.seal(out), false
		}
		if pos, isToken := tokenPosition(line); isToken {
			// Rule 1. The token is ours, so it is either the one we
			// are waiting for at the position we are waiting for it,
			// or it is a named failure. It is never dropped: a
			// dropped token costs a forger nothing and tells the
			// product nothing.
			if pos != w.accepted || line != want {
				w.lg.Warn("shellintegration: bootstrap token out of order",
					"token", line, "waiting_for", want, "accepted", w.accepted)
				return w.seal(OutcomeBootstrapOutOfOrder), false
			}
			w.accepted = pos + 1
			return "", true
		}
		// Anything else is not ours. It is logged and dropped rather
		// than acted on: the far side may be a shell that never ran
		// our loader at all, and a line of its output is not a
		// protocol event.
		if strings.TrimSpace(line) != "" {
			w.lg.Debug("shellintegration: unexpected line during bootstrap", "line", line)
		}
	}
}

// awaitOutcome reads until the far side names a terminal outcome. It writes
// nothing: the far side is not blocked on us here, and a byte now would be a
// byte after a complete frame. A token of the closed set arriving here is a
// repeat by definition — both have been consumed — so rule 1 names it.
func (w *bootstrapWriter) awaitOutcome(ctx context.Context, timeout time.Duration) Outcome {
	for {
		line, err := w.s.ReadLine(ctx, timeout)
		if err != nil {
			if errors.Is(err, ErrBootstrapDeadline) {
				w.lg.Warn("shellintegration: no terminal outcome inside the frame deadline")
				return w.seal(OutcomeBootstrapTimeout)
			}
			w.lg.Warn("shellintegration: bootstrap stream ended before a terminal outcome", "error", err)
			return w.seal(OutcomeBootstrapInterrupted)
		}
		if out, ok := outcomeInLine(line); ok {
			return w.seal(out)
		}
		if _, isToken := tokenPosition(line); isToken {
			w.lg.Warn("shellintegration: bootstrap token repeated after the pair was delivered", "token", line)
			return w.seal(OutcomeBootstrapOutOfOrder)
		}
		if strings.TrimSpace(line) != "" {
			w.lg.Debug("shellintegration: unexpected line while awaiting the outcome", "line", line)
		}
	}
}

// tokenPosition reports where a line sits in the closed token set, and
// whether it is in it at all.
func tokenPosition(line string) (int, bool) {
	for i, tok := range tokenOrder {
		if line == tok {
			return i, true
		}
	}
	return 0, false
}

// frameTwo waits out §6.1's ordering and returns frame 2, the outcome the
// backend has already decided for this session when that frame is a refusal
// rather than the pair, and why.
//
// The barrier comes first and applies to both kinds, because it is the
// delivery it orders and not the mint (see BootstrapPlan.Ordered). A nil
// source, one that refuses, and a barrier that reports the receiver will never
// be ready all produce the SAME frame — the non-secret refusal, in the same
// slot in the conversation — and the same outcome: nothing was minted, so this
// session has no authenticated channel, which is what channel-unavailable
// says. The closed set has one member for "nothing was minted" and this is
// deliberately it: the causes differ, they all reach the user as the same
// sentence, and widening the set to distinguish them would name a difference
// nothing acts on.
func frameTwo(ctx context.Context, plan BootstrapPlan) (frame []byte, refused Outcome, err error) {
	if plan.Ordered != nil {
		if oerr := plan.Ordered(ctx); oerr != nil {
			return RefusalFrame(OutcomeChannelUnavailable), OutcomeChannelUnavailable, oerr
		}
	}
	if plan.Secret == nil {
		return RefusalFrame(OutcomeChannelUnavailable), OutcomeChannelUnavailable,
			errors.New("no lifecycle channel")
	}
	payload, merr := plan.Secret.Mint(ctx)
	if merr != nil {
		return RefusalFrame(OutcomeChannelUnavailable), OutcomeChannelUnavailable, merr
	}
	return payload, "", nil
}

// outcomeInLine reads a terminal outcome off one line of far-side output. A
// token that is not one of ours is NOT an outcome — never a refusal the
// reader invented — so it is reported as no match and the line is dropped.
func outcomeInLine(line string) (Outcome, bool) {
	if !strings.HasPrefix(line, OutcomePrefix) {
		return "", false
	}
	return OutcomeForToken(strings.TrimSpace(strings.TrimPrefix(line, OutcomePrefix)))
}
