// Package remoteprobe is the closed set of shell probes nocx runs on a remote
// host, and the vocabulary their failures answer in. It is a LEAF: standard
// library only, no internal imports.
//
// # Why the commands live here and not where they are run
//
// A probe used to be built by the process that asked for it and executed by the
// process that held the ssh connection — the same process, so the command text
// existed once. D3 moved the execution to the helper (plan §3: no free-form
// exec crosses the helper wire), which splits builder from runner, and a
// command written down twice is a drift with a delay fuse: the two copies agree
// everywhere anybody looks and disagree on the host where it matters.
//
// So the text is here, once, and both ends link it. What crosses the wire is a
// NAME (a probe member, a phase) and typed arguments — never a command, never
// an argv, never a script. The helper composes the command from this package
// and runs it; the coordinator keeps the parsing, the ladder and the framing
// checks, which are decisions about the ANSWER rather than about the command.
//
// # What "a probe" means here
//
// One bounded command whose output is the answer to one question somebody inside
// nocx asked: what platform is this host, where is its home, which ports are
// listening, what does its shell think the line should be. None of them takes
// user-supplied shell text: the completion probe carries the typed line as an
// ARGUMENT to a fixed script, quoted, which is a data question and not a command
// one.
//
// # Framing
//
// Two of the families below frame their output between fixed marker lines so a
// login banner, an MOTD or a chatty rc file cannot be half-parsed into an
// answer. The markers are derived from a per-invocation nonce and the helpers
// that spell them are HERE, next to the scripts that print them, because the
// parser that looks for a marker and the script that writes it must never be
// able to disagree.
package remoteprobe

import (
	"fmt"
	"strings"
)

// # The failure vocabulary
//
// Every way a probe can fail to run is one of these, because every consumer
// tells them apart differently: discovery caches a refused session, completion
// softens it into a sentence, commandnames reports a deadline. The spellings
// are the ones internal/discovery already used for the same facts (AD-8: one
// vocabulary per set of facts), and the ssh layer converts its own sentinels to
// them at the one place the transport is known.

// Kind classifies why a probe did not run. Zero is not a kind: a probe that ran
// returns a Result and no error.
type Kind int

const (
	// KindSessionRefused means the far side would not give us a new session
	// channel — MaxSessions reached, ordinarily because the user's own
	// interactive shell holds the only one. It is NOT "the tool is absent" and
	// not "there are no ports".
	KindSessionRefused Kind = iota + 1
	// KindExecProhibited means the far side refuses exec requests at all
	// (ForceCommand, a restricted shell, an sshd policy).
	KindExecProhibited
	// KindConnectionLost means the transport died under the probe.
	KindConnectionLost
	// KindLeaseClosed means the CALLER closed the lease while the probe was in
	// flight. It is a fact about us and never about the host, which is why it
	// is separate from KindConnectionLost even though both end the same way.
	KindLeaseClosed
	// KindCommandTooLong means the composed command exceeded the bound a remote
	// execve can carry. It is refused by nocx before anything is sent.
	KindCommandTooLong
)

// String names a kind for a log line. It is not the wire's spelling — the wire
// uses the ssh service's own refusal codes — and exists so a failure is
// readable wherever it is reported.
func (k Kind) String() string {
	switch k {
	case KindSessionRefused:
		return "session-refused"
	case KindExecProhibited:
		return "exec-prohibited"
	case KindConnectionLost:
		return "connection-lost"
	case KindLeaseClosed:
		return "lease-closed"
	case KindCommandTooLong:
		return "command-too-long"
	}
	return "unknown"
}

// Error is a classified probe failure. Kind is the transport-neutral fact; Err
// carries the underlying cause so errors.Is still reaches it.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return "remote probe: " + e.Kind.String()
	}
	return "remote probe: " + e.Kind.String() + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Result is one probe's answer: the captured streams, the remote exit status,
// and whether a capture bound was hit — a truncated answer is a PREFIX, and a
// partial answer must never surface as a complete one.
type Result struct {
	Stdout     []byte
	Stderr     []byte
	ExitStatus int
	Truncated  bool
}

// MaxCommandLen bounds a composed probe command.
//
// A command that outgrows it dies in the remote execve at MAX_ARG_STRLEN with
// nothing a person can act on, so nocx refuses it itself and says so. The
// helper enforces it where the command is composed, which is now the only place
// that knows its size before it is sent.
//
// # Why this is not ssh.MaxRemoteCommandLen, and why that is not a drift
//
// internal/ssh owns that number (1024) for the LAUNCHER CARRIER and the mux
// control socket, and internal/app has a test keeping it equal to
// shellintegration.MaxCarrierLen — one number, three packages, because those
// three are one question: what a command that STARTS A SESSION may be, and the
// answer is deliberately small because a carrier is payload-free.
//
// A probe is the other question. It is a fixed script from this repository plus
// typed arguments, and the largest of them is the completion script — measured
// 6,100 bytes of shell before a single argument is quoted into it. Bounding it
// at the carrier's number is not a stricter version of the same rule; it is a
// different rule, and applying it silently is how the completion probe stopped
// being able to run at all: the command was composed, handed to the transport
// that guarded it with the carrier's 1024, and refused there — so the SSH
// completion dropdown answered "completion unavailable" on every host, with the
// cause a constant in another package (nocx-50w7p.9).
//
// 32 KiB is internal/completion's own former bound for exactly this command
// (maxCompletionCmdLen), which is the largest probe there is. The test beside
// this constant measures every probe against it, so growth is a decision
// somebody takes rather than a drift nobody sees.
const MaxCommandLen = 32 * 1024

// MaxOutputBytes bounds what one probe's stdout and stderr may capture.
//
// It is the bound internal/ssh applied to the same probes when they ran in the
// coordinator (execOutputCap, 64 KiB), kept as ONE number and moved here
// because the commands are: a wedged or hostile command that writes for ever
// must stop the probe rather than grow this process, and reaching the bound
// sets Truncated instead of quietly returning less.
//
// It bounds each STREAM, not the two together — the same choice the bound it
// replaces made, and the reason a caller checks Truncated rather than a size.
const MaxOutputBytes = 64 << 10

// ShellQuote wraps s in single quotes, escaping embedded single quotes with the
// POSIX '\” idiom. It is the one quoting rule the probe builders share: a value
// that reaches a shell as an ARGUMENT must arrive as one argument and nothing
// else.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// heredoc renders "command << 'DELIM'\nbody\nDELIM\n": the shape both framing
// families use, because it needs no temp file on the far side, no printf
// escaping, and a delimiter that cannot collide with the body when it carries
// the invocation's nonce.
func heredoc(command, delim, body string) string {
	var b strings.Builder
	b.WriteString(command)
	b.WriteString(" << '")
	b.WriteString(delim)
	b.WriteString("'\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(delim)
	b.WriteString("\n")
	return b.String()
}

// sentinelPrintf composes one of the port probes' sentinel-wrapped commands.
//
// The wrapper is not decoration: the leading and trailing marker are what make
// an answer whole, and the explicit `exit "$e"` is what preserves the probe's
// own status across the trailing printf — the shell's exit status is the LAST
// command's, so without it every probe would report success.
func sentinelPrintf(probe string) string {
	return fmt.Sprintf("printf '%s\\n'; %s; e=$?; printf '%s\\n'; exit \"$e\"", Sentinel, probe, Sentinel)
}
