package shellintegration

import (
	"context"
	"errors"
	"fmt"
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

// SecretSource mints the pair, and is the §6.1 ordering seam.
//
// Design §6.1 fixes the order: frame 1 received and verified, THEN the
// lifecycle transport and its receiver fully ready, THEN the publish attempt
// at a terminal outcome, and only after all of those is anything minted. Mint
// is called at exactly that point — after StageReadyToken and never before —
// so the ordering is expressed in the code rather than in a comment.
//
// What is NOT yet true is that the minting itself happens there: today the
// lifecycle channel is established, and the pair minted, before the session is
// opened at all, so this returns a value that already exists. Moving the mint
// behind this call is design §12's P5, and this seam is where it lands. The
// shape it must preserve is the error path: an error here means nothing was
// minted, and stage-1 is told so with a RefusalFrame rather than being left to
// time out.
type SecretSource interface {
	Mint() (payload []byte, err error)
}

// SecretFunc adapts a function to SecretSource.
type SecretFunc func() ([]byte, error)

func (f SecretFunc) Mint() ([]byte, error) { return f() }

// BootstrapPlan is one session's delivery: the stage-1 bytes the carrier
// committed to, and where the secret comes from when the time is right.
type BootstrapPlan struct {
	// Stage1 is frame 1's payload. StageDigest over exactly these bytes is
	// what the command carries, so the two are minted together or not at
	// all.
	Stage1 []byte
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
	// 1. The receiver announces itself. Until LOADER_READY the far side has
	//    not yet taken the terminal — a frame sent before it would be
	//    echoed straight back at us.
	if out, ok := awaitToken(ctx, lg, s, LoaderReadyToken, ReceiverReadyDeadline, OutcomeReceiverUnready); !ok {
		return out
	}

	// 2. Frame 1, header and payload in ONE write: nothing of ours may
	//    interleave between a header and the body it describes.
	if err := writeFrame(s, FrameStageSeq, plan.Stage1); err != nil {
		lg.Warn("shellintegration: stage-1 frame could not be written", "error", err)
		return OutcomeBootstrapInterrupted
	}

	// 3. Stage-1 is running: it verified against the digest the command
	//    carried, or the loader would have named an outcome instead.
	if out, ok := awaitToken(ctx, lg, s, StageReadyToken, FrameCompletionDeadline, OutcomeBootstrapTimeout); !ok {
		return out
	}

	// 4. Only now is anything minted (design §6.1): frame 1 is verified and
	//    the receiver exists. A source that refuses hands stage-1 a
	//    NON-SECRET refusal — never a secret we then discard.
	frame, seq, mintErr := mintFrame(plan)
	if mintErr != nil {
		lg.Warn("shellintegration: no capability was minted for this session; the shell starts without a channel",
			"error", mintErr)
	}
	if err := writeFrame(s, seq, frame); err != nil {
		lg.Warn("shellintegration: secret frame could not be written", "error", err)
		return OutcomeBootstrapInterrupted
	}

	// 5. The terminal outcome. Nothing is written from here on, whatever
	//    happens: the far side is no longer blocked on us, so a byte now
	//    would be a byte after a complete frame.
	return awaitOutcome(ctx, lg, s, FrameCompletionDeadline)
}

// mintFrame returns frame 2 and its sequence number. A nil source, or one
// that refuses, produces the non-secret refusal — the same frame number,
// because it is the same slot in the conversation.
func mintFrame(plan BootstrapPlan) ([]byte, int, error) {
	if plan.Secret == nil {
		return RefusalFrame(OutcomeChannelUnavailable), FrameSecretSeq, errors.New("no lifecycle channel")
	}
	payload, err := plan.Secret.Mint()
	if err != nil {
		return RefusalFrame(OutcomeChannelUnavailable), FrameSecretSeq, err
	}
	return payload, FrameSecretSeq, nil
}

// writeFrame writes one frame as a single Write.
func writeFrame(s BootstrapStream, seq int, payload []byte) error {
	h, err := FrameHeader(seq, len(payload))
	if err != nil {
		return err
	}
	buf := make([]byte, 0, len(h)+len(payload))
	buf = append(buf, h...)
	buf = append(buf, payload...)
	n, err := s.Write(buf)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return fmt.Errorf("shellintegration: short frame write, %d of %d bytes", n, len(buf))
	}
	return nil
}

// awaitToken reads until the far side says want, names an outcome of its own,
// or the deadline passes. ok=false means the caller is finished and the
// returned Outcome is terminal.
//
// onDeadline is the outcome for a timeout, and the timeout also sends
// abortHeader: at both call sites the far side is blocked on a header read,
// so the abort is what turns a hang into a named refusal and a usable prompt.
func awaitToken(ctx context.Context, lg log.Logger, s BootstrapStream, want string, timeout time.Duration, onDeadline Outcome) (Outcome, bool) {
	for {
		line, err := s.ReadLine(ctx, timeout)
		if err != nil {
			if errors.Is(err, ErrBootstrapDeadline) {
				// The far side is waiting for bytes that are not
				// coming. Unblock it with something it must
				// refuse, so it reaches a prompt.
				if _, werr := s.Write(abortHeader); werr != nil {
					lg.Debug("shellintegration: abort header could not be written", "error", werr)
				}
				lg.Warn("shellintegration: bootstrap deadline passed", "waiting_for", want, "outcome", onDeadline)
				return onDeadline, false
			}
			lg.Warn("shellintegration: bootstrap stream ended before the far side answered",
				"waiting_for", want, "error", err)
			return OutcomeBootstrapInterrupted, false
		}
		if line == want {
			return "", true
		}
		if out, ok := outcomeInLine(line); ok {
			return out, false
		}
		// Anything else is not ours. It is logged and dropped rather
		// than acted on: the far side may be a shell that never ran
		// our loader at all, and a line of its output is not a
		// protocol event.
		if strings.TrimSpace(line) != "" {
			lg.Debug("shellintegration: unexpected line during bootstrap", "line", line)
		}
	}
}

// awaitOutcome reads until the far side names a terminal outcome. It writes
// nothing: the far side is not blocked on us here, and a byte now would be a
// byte after a complete frame.
func awaitOutcome(ctx context.Context, lg log.Logger, s BootstrapStream, timeout time.Duration) Outcome {
	for {
		line, err := s.ReadLine(ctx, timeout)
		if err != nil {
			if errors.Is(err, ErrBootstrapDeadline) {
				lg.Warn("shellintegration: no terminal outcome inside the frame deadline")
				return OutcomeBootstrapTimeout
			}
			lg.Warn("shellintegration: bootstrap stream ended before a terminal outcome", "error", err)
			return OutcomeBootstrapInterrupted
		}
		if out, ok := outcomeInLine(line); ok {
			return out
		}
		if strings.TrimSpace(line) != "" {
			lg.Debug("shellintegration: unexpected line while awaiting the outcome", "line", line)
		}
	}
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
