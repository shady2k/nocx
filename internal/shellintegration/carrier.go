package shellintegration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The carrier: the bounded loader that is the ONLY remote command a managed
// session emits (design §4.1, "The bundle travels on the channel, not in the
// command").
//
// What it replaces and why. The managed path used to publish the bundle over
// SFTP and then send the full self-installing launcher, which carried the
// same bundle a second time inline — 92,204 bytes for ShellAuto, measured in
// this repository — and substituted the per-epoch capability and the one-shot
// recovery fence into the rcfile text, so both reached the far host's process
// arguments and any recorder of the exec request. A consumer of an exec
// request has to serialize the command as ONE field of ONE record; a
// component that publishes an unbounded value into somebody else's
// single-record field has not made a size mistake, it has failed to state a
// contract. So the contract is stated here: at most MaxCarrierLen bytes, no
// payload, no secret, and everything of variable size travels as bounded
// channel frames instead.
//
// Three properties are the whole of it, and each is asserted:
//
//  1. UNCONDITIONAL. The command tests nothing about the far side. The guard
//     it replaces — `[ -x "$HOME/.nocx/launch" ]` placed first — loses a race
//     it cannot win: on a host with nothing committed the publish is still in
//     flight when the test runs, so the test fails, the session degrades to
//     conventional, and the publish then succeeds. The far side stays the
//     owner of "is this installation valid"; that verification runs after the
//     bootstrap has settled, inside stage-1, not in the command.
//  2. CAPABILITY-FREE AND PAYLOAD-FREE. Only addressing arguments travel:
//     session id, lane, domain, epoch, lifecycle port, the stage descriptor
//     number and the stage-1 digest. Those are names, not secrets — the
//     digest names public bytes, and knowing it yields nothing about either
//     bearer.
//  3. BOUNDED. Under MaxCarrierLen for every ShellKind, and the assertion is
//     a GRAMMAR as well as a length, so an encoded payload cannot satisfy it
//     by being short.
//
// # The seam
//
// This file owns the loader and the FRAME PROTOCOL both sides speak. It does
// NOT own stage-1, the secret frame, or the Go sender that writes frames into
// the session — those are the next package (design §12, P2) and belong to
// another worker. The seam between them is named and narrow:
//
//   - LaunchOptions.StageDigest is the digest the sender commits to. The
//     sender computes it with StageDigest over the exact bytes it is about to
//     write and puts it here; the loader refuses anything else.
//   - FrameHeader builds the header the loader parses. Everything the writer
//     needs — the magic, the sequence numbers, the caps, the deadlines and
//     the tokens the loader emits — is a named constant below.
//
// Until that sender exists, a managed session emits this command, the loader
// reaches its frame read, no frame arrives, and the session lands on a native
// login shell with a named outcome. That is the fail-open this design
// promises, not a silent degrade — and P2 closes it.
const (
	// MaxCarrierLen is the stated bound on the remote command: 1 KiB, for
	// every ShellKind and every combination of addressing arguments. It is
	// not an argv limit — Linux's MAX_ARG_STRLEN is two orders above it —
	// it is the size a consumer of the exec request has to be able to carry
	// whole. A command that would exceed it is refused, never truncated.
	MaxCarrierLen = 1024

	// StageFD is the descriptor the loader opens the verified stage-1 on
	// before unlinking its name. It is chosen from the single-digit range
	// POSIX sh guarantees, it is not a secret, and it travels in the
	// command so stage-1 knows which descriptor it was sourced from.
	StageFD = 9
)

// ---------------------------------------------------------------------------
// The frame protocol
// ---------------------------------------------------------------------------

// The frame protocol is declared here because the loader is its first
// reader and a later package is its writer. Both halves must agree on all
// of it:
//
//		<magic> <seq> <length right-aligned in 8 columns>\n<length bytes>
//
//	  - A LENGTH PREFIX, so the reader never has to recognise a terminator
//	    inside the payload.
//	  - A FIXED-WIDTH HEADER — FrameHeaderLen bytes, always — because the
//	    reader must not over-consume. A shell's `read` builtin is entitled to
//	    pull a buffer's worth off a terminal and hand back one line, and it
//	    does: the first draft used `read` and the body it had already
//	    swallowed left dd(1) blocked forever waiting for bytes that had
//	    already arrived. Both the header and the body are therefore read with
//	    `dd bs=1 count=N`, which performs exactly N one-byte reads and cannot
//	    take a byte it was not asked for. The length is right-aligned rather
//	    than zero-padded on purpose: a leading zero makes test(1) and dd(1)
//	    read the number as octal.
//	  - AN ENCODING THAT NEVER REQUIRES HOLDING BINARY IN A SHELL VARIABLE:
//	    the body is copied byte-for-byte from the terminal into a file with
//	    dd(1) and never enters a shell variable at all, so there is nothing
//	    for a NUL or a newline to break. The payload must be text the far
//	    shell can source; it is not re-encoded, and re-encoding it would be
//	    the compression this design removed the reason for.
//	  - THE CAP IS ENFORCED WHILE ACCUMULATING, not after: the length is
//	    known before a single body byte is read, it is checked against the
//	    cap first, and dd(1) reads exactly that many bytes and no more. The
//	    reader can never hold more than the cap.
//	  - A DEADLINE ON COMPLETING THE FRAME, not on each read. It is the
//	    WRITER's, and deliberately: a portable shell has no timed read
//	    without a sleep loop, and design §7 forbids remote work whose
//	    duration is decided by the remote host. The Go side holds
//	    ReceiverReadyDeadline and FrameCompletionDeadline against an injected
//	    clock and stops writing; the loader then sees EOF.
//	  - EOF BEFORE THE FRAME COMPLETES is OutcomeBootstrapInterrupted. So is
//	    a body shorter than its header declared.
//	  - NO BYTE AFTER A COMPLETE FRAME EVER BECOMES SHELL INPUT. Two halves:
//	    the reader consumes exactly the declared length and stops, and the
//	    writer sends nothing between the end of a frame and the outcome it is
//	    waiting for. The second half is the writer's obligation and is stated
//	    here because only the writer can honour it.
//	  - AND NO BYTE BEFORE ONE DOES EITHER. The writer must never send a body
//	    the reader will refuse BEFORE reading it, because a refusal execs a
//	    native login shell and everything still queued on the terminal becomes
//	    that shell's typed input. The reader's side of it is that the only
//	    pre-body refusal a real far host reaches — no secure temp — now happens
//	    before READY (see carrierLoaderTemplate). The writer's side is that the
//	    other two cannot be produced: FrameHeader refuses to build an over-cap
//	    frame, and every header it does build parses.
const (
	// FrameMagic opens every header. It is also the prefix of the tokens
	// the loader emits, so one word identifies this protocol in a
	// recorded stream.
	FrameMagic = "NOCX1"

	// FrameHeaderLen is the exact width of every header, in bytes. The
	// reader consumes precisely this many before it knows anything about
	// the body, so the width is part of the contract and not an artefact
	// of the format string: magic, space, one sequence digit, space, eight
	// columns of right-aligned length, newline.
	FrameHeaderLen = len(FrameMagic) + 1 + 1 + 1 + 8 + 1

	// FrameStageSeq is frame 1: stage-1 itself.
	FrameStageSeq = 1
	// FrameSecretSeq is frame 2: the secret (design §5.2). The loader never
	// reads it — stage-1 does — and it is declared here so the two frames
	// are never confused by either side.
	FrameSecretSeq = 2

	// MaxStageFrameLen caps frame 1 at 32 KiB (design §7): measured stage-1
	// with room to grow, three orders below the command it replaces.
	MaxStageFrameLen = 32 * 1024
	// MaxSecretFrameLen caps frame 2 at 4 KiB (design §7): a small delivery
	// must not inherit a publish-sized ceiling.
	MaxSecretFrameLen = 4 * 1024

	// LoaderReadyToken is emitted by the loader once it owns the terminal —
	// termios saved, traps installed, raw with echo off — and never before.
	// It is the writer's signal that a frame may now be sent.
	LoaderReadyToken = FrameMagic + " LOADER_READY"
	// StageReadyToken belongs to stage-1 and is declared here so the loader
	// can be asserted never to emit it: the backend must always know which
	// of the two it received.
	StageReadyToken = FrameMagic + " STAGE_READY"
	// OutcomePrefix prefixes every terminal outcome the loader names.
	OutcomePrefix = FrameMagic + " OUTCOME "
)

// The writer's deadlines (design §7). They are the Go side's because the far
// side has no portable timer; a test drives them from an injected clock, and
// no test may wait on a duration.
const (
	// ReceiverReadyDeadline bounds the wait for LoaderReadyToken.
	ReceiverReadyDeadline = 3 * time.Second
	// FrameCompletionDeadline bounds COMPLETING a frame once the receiver
	// is ready — not each write.
	FrameCompletionDeadline = 3 * time.Second
)

// Outcome is a terminal outcome the loader names on the wire. The set is
// closed: every failure path below names exactly one of these and then execs
// a native login shell, so a refusal is never log-only and never leaves the
// user without a prompt (design D7).
//
// The Outcome value is the design's own vocabulary and is what the product
// renders. What travels in the command is the shorter TOKEN beside it in
// outcomeTokens — the two are one table with one owner, and the split exists
// for one measured reason: the nine spec names cost 179 bytes of a 1 KiB
// command, and the tokens cost 75. The command stays legible with them, and
// arguably more so, because `||R bad-digest` sits directly after the digest
// comparison that produces it.
type Outcome string

const (
	// OutcomeLoaderTermiosUnavailable: the terminal state could not be
	// saved or could not be put into raw mode, so the loader cannot promise
	// to give the terminal back.
	OutcomeLoaderTermiosUnavailable Outcome = "loader-termios-unavailable"
	// OutcomeBootstrapInterrupted: EOF, a short body, or a catchable signal
	// before the frame completed.
	OutcomeBootstrapInterrupted Outcome = "bootstrap-interrupted"
	// OutcomeBootstrapProtocol: the header was not a frame header of this
	// protocol.
	OutcomeBootstrapProtocol Outcome = "bootstrap-protocol"
	// OutcomeStageTooLarge: the header declared more than MaxStageFrameLen.
	// Refused before a single body byte is read.
	OutcomeStageTooLarge Outcome = "stage-too-large"
	// OutcomeNoSecureTemp: no secure temporary file could be created.
	OutcomeNoSecureTemp Outcome = Outcome(ReasonNoSecureTemp)
	// OutcomeStageDigestUnavailable: neither sha256sum nor shasum is
	// present, so the frame cannot be verified. Unverified stage-1 is never
	// executed.
	OutcomeStageDigestUnavailable Outcome = "stage-digest-unavailable"
	// OutcomeStageDigestMismatch: the frame's digest is not the one the
	// command committed to. It is also what an ABSENT commitment produces,
	// which is deliberate: "there is nothing to verify against" and "this is
	// not what I asked for" have the same safe answer, and inventing a
	// separate name for the first would widen a closed set for no gain.
	OutcomeStageDigestMismatch Outcome = "stage-digest-mismatch"
	// OutcomeStageFDUnavailable: the descriptor could not be opened, or
	// /dev/fd/N is not readable on this platform. An unreachable stage-1 is
	// never executed.
	OutcomeStageFDUnavailable Outcome = "stage-fd-unavailable"
	// OutcomeStageSourceFailed: stage-1 was sourced and returned instead of
	// taking over the session.
	OutcomeStageSourceFailed Outcome = "stage-source-failed"

	// The members below are STAGE-1's and the BACKEND's (stage1.go,
	// bootstrap.go). They are in this table rather than a second one
	// because the set is closed across the whole bootstrap, not per
	// component: a reader of a session's output turns one token into one
	// outcome, and OutcomeForToken cannot have two owners. Their tokens are
	// their own names, unlike the loader's: the token abbreviations exist to
	// buy bytes in a 1 KiB COMMAND, and none of these is named in one.

	// OutcomeBootstrapAccepted is the success terminal outcome, emitted by
	// the launch carrier once the generation it is about to exec has been
	// re-proved. It is the event that closes the input quarantine (§5.3);
	// nothing else does, and READY explicitly does not.
	OutcomeBootstrapAccepted Outcome = "bootstrap-accepted"
	// OutcomeSecretTooLarge: frame 2's header declared more than
	// MaxSecretFrameLen. Refused before a body byte is read.
	OutcomeSecretTooLarge Outcome = "secret-too-large"
	// OutcomeSecretMalformed: frame 2 parsed but a bearer is not the shape
	// a bearer has.
	OutcomeSecretMalformed Outcome = "secret-malformed"
	// OutcomeSecretNotForThisSession: frame 2 named a different session,
	// domain or epoch than the command addressed — including a frame
	// replayed at a session it was not minted for.
	OutcomeSecretNotForThisSession Outcome = "secret-not-for-this-session"
	// OutcomeCapabilityFDUnavailable: the read or the write descriptor for
	// the capability could not be opened. Nothing has been written.
	OutcomeCapabilityFDUnavailable Outcome = "capability-fd-unavailable"
	// OutcomeCapabilityUnlinkFailed: the temp file's name could not be
	// removed, so nothing is written at all (design §5.2 case 4) — the
	// capability never reaches a filesystem object anything can open by
	// name.
	OutcomeCapabilityUnlinkFailed Outcome = "capability-unlink-failed"
	// OutcomeCapabilityWriteFailed: the write or the close of the write
	// descriptor failed, so the bootstrap did not succeed.
	OutcomeCapabilityWriteFailed Outcome = "capability-write-failed"
	// OutcomeGenerationUnavailable: there is no executable launch carrier
	// on the far host, so there is nothing to exec. The session gets a
	// native login shell; the next connection publishes and bootstraps
	// again.
	OutcomeGenerationUnavailable Outcome = "generation-unavailable"

	// OutcomeReceiverUnready and OutcomeBootstrapTimeout are the BACKEND's
	// alone: the far side never speaks them, because a portable shell has
	// no timed read without a sleep loop and design §7 forbids remote work
	// whose duration the remote host decides. The deadlines are the
	// writer's, and so are the outcomes they produce.
	OutcomeReceiverUnready  Outcome = "receiver-unready"
	OutcomeBootstrapTimeout Outcome = "bootstrap-timeout"
	// OutcomeBootstrapOutOfOrder is the BACKEND's too, and it is §6.1's
	// first rule against a forged readiness token made into an outcome:
	// each token of the closed set is accepted AT MOST ONCE AND ONLY IN ITS
	// ORDER, and a repeat or an out-of-order token is a named bootstrap
	// failure rather than a second trigger. The far side never speaks it —
	// the loader emits each of its tokens once, by construction — so a
	// session that produces it has had a token written into it by somebody
	// who is not our loader.
	OutcomeBootstrapOutOfOrder Outcome = "bootstrap-out-of-order"
	// OutcomeChannelUnavailable: the lifecycle channel could not be opened,
	// so nothing was minted (design §6.1) and stage-1 received a non-secret
	// refusal. The shell still comes up integrated in the prompt sense; it
	// simply has no authenticated channel.
	OutcomeChannelUnavailable Outcome = "channel-unavailable"
)

// outcomeTokens is the whole of the wire vocabulary: one entry per Outcome,
// and no Outcome without one. A reader of the session's output turns a token
// back into the product's name with OutcomeForToken.
var outcomeTokens = map[Outcome]string{
	OutcomeLoaderTermiosUnavailable: "termios",
	OutcomeBootstrapInterrupted:     "interrupted",
	OutcomeBootstrapProtocol:        "protocol",
	OutcomeStageTooLarge:            "too-large",
	OutcomeNoSecureTemp:             "no-temp",
	OutcomeStageDigestUnavailable:   "no-digest",
	OutcomeStageDigestMismatch:      "bad-digest",
	OutcomeStageFDUnavailable:       "no-fd",
	OutcomeStageSourceFailed:        "no-source",

	OutcomeBootstrapAccepted:       "accepted",
	OutcomeSecretTooLarge:          "secret-too-large",
	OutcomeSecretMalformed:         "secret-malformed",
	OutcomeSecretNotForThisSession: "secret-not-for-this-session",
	OutcomeCapabilityFDUnavailable: "capability-fd-unavailable",
	OutcomeCapabilityUnlinkFailed:  "capability-unlink-failed",
	OutcomeCapabilityWriteFailed:   "capability-write-failed",
	OutcomeGenerationUnavailable:   "generation-unavailable",

	OutcomeReceiverUnready:     "receiver-unready",
	OutcomeBootstrapTimeout:    "bootstrap-timeout",
	OutcomeBootstrapOutOfOrder: "bootstrap-out-of-order",
	OutcomeChannelUnavailable:  "channel-unavailable",
}

// OutcomeToken returns the token the loader emits for o, or the empty string
// for a value that is not in the closed set.
func OutcomeToken(o Outcome) string { return outcomeTokens[o] }

// AllOutcomes is the closed set itself, in a stable order.
//
// It is derived from outcomeTokens rather than restated, which is the whole
// point: a consumer that has to answer "and every one of these" — the product
// vocabulary, the wire enum — asks the table that already exists, so a member
// added here cannot be missed by a check that keeps its own copy of the list.
// The order is by token so a failure names the same member twice in a row.
func AllOutcomes() []Outcome {
	out := make([]Outcome, 0, len(outcomeTokens))
	for o := range outcomeTokens {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return outcomeTokens[out[i]] < outcomeTokens[out[j]] })
	return out
}

// outcomeByToken is the reverse direction, built once from the closed set.
//
// It is built rather than scanned because "a reader of a session's output
// turns one token into one outcome, and OutcomeForToken cannot have two
// owners" is stated above as an invariant, and until this existed nothing
// held it: outcomeTokens is keyed by Outcome, so two outcomes sharing a token
// were accepted silently, and the scan that answered the reverse question
// ranged over a Go map — whose iteration order is randomised per run. A
// session refused for one reason would then be named after either of the two
// outcomes, differently on each read of the same line, and the user would be
// shown a diagnosis that changed while they were looking at it.
//
// AllOutcomes is what it enumerates, for the reason that function gives:
// asking the table that already exists is what stops a member added there
// from being missed by a consumer keeping its own copy of the list. A
// collision panics at init, in the same breath and for the same reason
// stageDigestRE compiles at init — it is a mistake in this file's own table,
// every test binary that loads the package trips it, and no build carrying
// one can start.
var outcomeByToken = func() map[string]Outcome {
	m := make(map[string]Outcome, len(outcomeTokens))
	for _, o := range AllOutcomes() {
		tok := outcomeTokens[o]
		if first, dup := m[tok]; dup {
			panic("shellintegration: outcomes " + string(first) + " and " + string(o) +
				" share the token " + tok + "; a token names exactly one outcome")
		}
		m[tok] = o
	}
	return m
}()

// OutcomeForToken maps a token read off the session back to the outcome the
// product names. ok is false for anything that is not one of ours — which a
// reader must treat as "not an outcome", never as a refusal it invented.
func OutcomeForToken(token string) (Outcome, bool) {
	o, ok := outcomeByToken[token]
	return o, ok
}

// FrameHeader builds the header for one frame. It is the writer's half of
// the caps: a payload past its frame's ceiling is refused HERE as well as by
// the loader, so an over-cap frame cannot be produced by accident and the
// only way to send one is to bypass this function deliberately (which is
// what the loader's own refusal is tested with).
func FrameHeader(seq, length int) (string, error) {
	var limit int
	switch seq {
	case FrameStageSeq:
		limit = MaxStageFrameLen
	case FrameSecretSeq:
		limit = MaxSecretFrameLen
	default:
		return "", fmt.Errorf("shellintegration: unknown frame sequence %d", seq)
	}
	if length < 1 || length > limit {
		return "", fmt.Errorf("shellintegration: frame %d length %d outside 1..%d", seq, length, limit)
	}
	h := fmt.Sprintf("%s %d %8d\n", FrameMagic, seq, length)
	if len(h) != FrameHeaderLen {
		return "", fmt.Errorf("shellintegration: header %q is %d bytes, want %d", h, len(h), FrameHeaderLen)
	}
	return h, nil
}

// StageDigest is the digest the command commits to and the loader
// recomputes: lowercase hex SHA-256 over the exact frame payload. The sender
// calls it over the bytes it is about to write; anything else is a mismatch
// and no stage-1 runs.
func StageDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// stageDigestRE is the only shape the loader can compare against. A value of
// any other shape is normalised to empty rather than travelling: the loader
// then names OutcomeStageDigestMismatch and starts a native login shell,
// which is the correct outcome for "there is nothing to verify against" and
// needs no separate vocabulary for it.
var stageDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---------------------------------------------------------------------------
// The loader
// ---------------------------------------------------------------------------

// carrierLoaderTemplate is the loader, authored multi-line and joined into
// one physical line before it ships (singleLine): sshd hands the remote
// command to the user's login shell, and a csh login shell splits a
// single-quoted token containing a newline. For the same reason it contains
// NO single quote anywhere — it travels single-quoted inside the outer
// command.
//
// The order of the first statements is the contract, not a style. The loader
// is the SOLE owner of the original termios, so it saves it, installs its
// cleanup trap IMMEDIATELY — a signal arriving between the save and the trap
// would leave the terminal raw with nothing to restore it — enters raw with
// echo off, which is also what stops the frame from being echoed back to the
// sender, and only then says it is ready. Stage-1 never runs `stty -g`
// again: by then the state is the loader's, not the user's, and a second
// save would record the loader's state as if it were the user's.
//
// THE TEMP FILE IS CREATED BEFORE READY, and that is part of the same
// contract: READY means "I can receive a frame", not "I have started".
//
// It was the other way round, and the ordering was wrong in a way only a
// non-bash shell made visible. The writer sends the header and the body in one
// write as soon as it sees READY, so a refusal that happens AFTER READY but
// BEFORE the body is read leaves 1.6 KiB of stage-1 in the terminal's input
// queue — and the native login shell this loader then execs reads it as
// TYPED COMMANDS. Measured in the containerized image, where /bin/sh is dash:
// the shell announced STAGE_READY on the user's terminal, ran stage-1's own
// `dd` and SWALLOWED THE USER'S NEXT TYPED LINE, then printed three parse
// errors. "Any refusal leaves a working native login shell" (design D7) was
// false for that path.
//
// Creating the temp file first closes it at zero cost in bytes: mktemp is the
// only pre-body refusal a real far host reaches (a read-only or full /tmp, or
// no mktemp at all), and moving it above READY means the outcome is named
// while the writer is still waiting to be told it may send. The writer then
// sends nothing at all.
//
// The two remaining pre-body refusals — a header that is not ours, and a
// declared length over the cap — stay after READY, and are unreachable from
// our own writer by construction: FrameHeader refuses to build an over-cap
// frame, and every header it does build parses. That makes it a WRITER
// OBLIGATION, stated with the others in the frame-protocol comment: never
// send a body the reader will refuse before reading it.
//
// R is the only exit. It restores the terminal, closes the descriptor and
// removes the temp name BEFORE it names the outcome — design §5.3's fourth
// interval: no outcome is ever reported over a half-released terminal — and
// then execs a native login shell, so it cannot run twice and cleanup is
// idempotent by construction. Every failure path reaches it, including every
// catchable signal.
//
// Two economies are worth naming because they look like carelessness:
//
//   - stderr is sent to /dev/null for the whole bootstrap and restored with
//     `2>&1` before stage-1 is sourced and inside R. `2>&1` is a faithful
//     restore here and not an approximation: sshd gives a PTY session one
//     terminal, so stdout and stderr are the same file. It buys the ~120
//     bytes that eleven separate `2>/dev/null` redirections would cost.
//   - `[ "$L" -gt 0 ]` is the numeric validation of the declared length as
//     well as its lower bound: test(1) fails with a non-integer, which the
//     `||` arm catches. A length that somehow passed it and is still not a
//     count fails at dd, which then leaves a short file, which the length
//     check refuses. The cap is checked BEFORE any body byte is read, so the
//     reader can never hold more than MaxStageFrameLen.
//   - `[ $(wc -c<"$F") -eq $L ]` is deliberately unquoted. wc(1) on some
//     platforms pads its count with spaces, and field splitting is what
//     removes them; a wc that failed altogether substitutes nothing, test(1)
//     then fails on a missing operand, and the `||` arm refuses — the safe
//     answer either way.
//
// The addressing arguments are the shell's positional parameters and are
// deliberately left untouched, so the sourced stage-1 inherits them exactly
// as the command stated them. @DIGARG@ is the digest's position.
const carrierLoaderTemplate = `R(){ stty "$T";exec 2>&1 @FD@<&-;rm -f "$F";printf "@OUTPFX@%s\n" "$1";exec "${SHELL:-/bin/sh}" -l;}
exec 2>/dev/null
T=$(stty -g)
trap "R @INTERRUPTED@" HUP INT QUIT TERM
[ -n "$T" ]&&stty raw -echo||R @TERMIOS@
F=$(mktemp "${TMPDIR:-/tmp}/nocx.XXXXXX")||R @NOTEMP@
printf "@READY@\n"
X=$(dd bs=1 count=@HDRLEN@)
[ -n "$X" ]||R @INTERRUPTED@
[ "${X#@MAGIC@ @SEQ@ }" != "$X" ]||R @PROTOCOL@
L=${X##* }
[ "$L" -gt 0 ]||R @PROTOCOL@
[ "$L" -le @MAX@ ]||R @TOOLARGE@
dd bs=1 count=$L >"$F"
[ $(wc -c<"$F") -eq $L ]||R @INTERRUPTED@
H=$(sha256sum "$F")||H=$(shasum -a 256 "$F")||R @NODIGEST@
[ "${H%% *}" = "$@DIGARG@" ]||R @MISMATCH@
exec @FD@<"$F"&&[ -r /dev/fd/@FD@ ]||R @NOFD@
rm -f "$F";F=
exec 2>&1
. /dev/fd/@FD@
R @SOURCEFAILED@`

// carrierArgCount is how many addressing arguments the command carries, and
// the digest is the last of them. Both numbers are load-bearing: the loader
// reads the digest as a positional parameter, and the grammar assertion
// counts the words.
const carrierArgCount = 7

// carrierLoader renders the loader text. Every value it substitutes comes
// from a constant above, so the Go side and the shell side cannot drift.
func carrierLoader() string {
	return singleLine(strings.NewReplacer(
		"@FD@", strconv.Itoa(StageFD),
		"@OUTPFX@", OutcomePrefix,
		"@READY@", LoaderReadyToken,
		"@MAGIC@", FrameMagic,
		"@SEQ@", strconv.Itoa(FrameStageSeq),
		"@HDRLEN@", strconv.Itoa(FrameHeaderLen),
		"@MAX@", strconv.Itoa(MaxStageFrameLen),
		"@DIGARG@", strconv.Itoa(carrierArgCount),
		"@TERMIOS@", OutcomeToken(OutcomeLoaderTermiosUnavailable),
		"@INTERRUPTED@", OutcomeToken(OutcomeBootstrapInterrupted),
		"@PROTOCOL@", OutcomeToken(OutcomeBootstrapProtocol),
		"@TOOLARGE@", OutcomeToken(OutcomeStageTooLarge),
		"@NOTEMP@", OutcomeToken(OutcomeNoSecureTemp),
		"@NODIGEST@", OutcomeToken(OutcomeStageDigestUnavailable),
		"@MISMATCH@", OutcomeToken(OutcomeStageDigestMismatch),
		"@NOFD@", OutcomeToken(OutcomeStageFDUnavailable),
		"@SOURCEFAILED@", OutcomeToken(OutcomeStageSourceFailed),
	).Replace(carrierLoaderTemplate))
}

// carrierCommand renders the remote command for one session: the loader,
// then the addressing arguments in the order stage-1 reads them.
//
// The ShellKind is taken and validated but does not change a byte of the
// result, and that is the design's decision rather than an oversight: the
// far side dispatches on the shell it actually is, after stage-1 has run,
// and a shell name is not on §4.1's list of what may travel in the command.
// A profile pin therefore reaches the far side with stage-1 — on the
// channel, where the pin can be acted on — not here. The unmapped-kind arm
// stays a tripwire: a new ShellKind is a decision, not a fallback.
func carrierCommand(shell ShellKind, opts LaunchOptions) (string, RefusalReason, bool) {
	switch shell {
	case ShellBash, ShellZsh, ShellUnknown, ShellAuto:
	default:
		return "", ReasonUnsupportedShell, false
	}
	if opts.Enhanced && opts.SessionID == "" {
		// Pinned contract, unchanged by this design: a marker-only session
		// with no id cannot anchor the ownership protocol, so fail closed
		// rather than emit one that half-works.
		return "", ReasonUnsupportedShell, false
	}

	sessionID := ""
	if opts.Enhanced {
		sessionID = opts.SessionID
	}
	digest := opts.StageDigest
	if !stageDigestRE.MatchString(digest) {
		digest = ""
	}
	args := [carrierArgCount]string{
		sessionID,
		opts.Lane,
		opts.Domain,
		strconv.FormatUint(opts.Epoch, 10),
		strconv.Itoa(opts.LifecyclePort),
		strconv.Itoa(StageFD),
		digest,
	}

	var b strings.Builder
	b.WriteString("/usr/bin/env -u BASH_ENV /bin/sh -c ")
	b.WriteString(ShellQuote(carrierLoader()))
	b.WriteString(" nocx-loader")
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(ShellQuote(a))
	}
	cmd := b.String()
	if len(cmd) >= MaxCarrierLen {
		// The bound is the contract, so it is enforced rather than
		// documented: an addressing value long enough to breach it is
		// refused whole, never truncated into a command that means
		// something else. The same shape as the full launcher's own
		// overflow refusal.
		return "", ReasonUnsupportedShell, false
	}
	return cmd, ReasonNone, true
}
