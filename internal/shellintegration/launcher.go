package shellintegration

import (
	"fmt"
	"strings"
)

// ShellKind names the far shell a launcher builds a start command for.
type ShellKind string

const (
	ShellBash    ShellKind = "bash"
	ShellZsh     ShellKind = "zsh"
	ShellUnknown ShellKind = "unknown"
	// ShellAuto means "the far host decides": the launcher emits a single
	// strictly-POSIX dispatcher that detects the login shell at runtime and
	// execs the matching tier (nocx-6rj0). It is a build-time intent, never
	// a detected result — StartCommand never returns it as a claim.
	ShellAuto ShellKind = "auto"
)

// RefusalReason is why integration did not happen, in a form the product
// renders. The empty string means "no refusal".
type RefusalReason string

const (
	ReasonNone             RefusalReason = ""
	ReasonUnsupportedShell RefusalReason = "unsupported-shell"
	ReasonNoSecureTemp     RefusalReason = "no-secure-temp"
)

// LaunchOptions carries what the start command must embed.
type LaunchOptions struct {
	SessionID string // NOCX_SESSION_ID for this session; never empty when Enhanced
	Enhanced  bool   // request marker-only prompt mode (ADR-0006)
	// The authenticated lifecycle channel (ADR-0024). Capability is the
	// per-epoch bearer. On the carrier path it travels as FRAME 2 and
	// reaches the shell through an inherited, already-unlinked descriptor
	// (stage1.go) — never the command, never argv, never the environment,
	// never a named file. On the local child path, and on the retired full
	// launcher P4 still owns, it is substituted into the rcfile TEXT
	// instead (capability_source.go names both forms and why each is
	// safe). Lane, Domain and Epoch are names, not secrets.
	// The transport is either an inherited descriptor (LifecycleFD, the
	// local path) or a loopback TCP port (LifecyclePort, the remote path);
	// zero means that side is absent. Empty Capability means no channel:
	// the session is conventional.
	Capability string
	// Recovery is the per-domain one-shot recovery fence (ADR-0024 decision
	// 8). It travels exactly like the capability and by the same two forms,
	// never exported to the environment. The shell writes it to the pty at
	// the next prompt boundary if the lifecycle channel dies mid-session;
	// nocx matches it as the restoration acknowledgement. Empty means no
	// recovery is offered.
	Recovery      string
	Lane          string
	Domain        string
	Epoch         uint64
	LifecycleFD   int
	LifecyclePort int
	// StageDigest is the lowercase hex SHA-256 of the stage-1 frame the
	// sender is about to write (carrier.go). It is an ADDRESSING value, not
	// a secret: it names public bytes, and knowing it yields nothing about
	// either bearer. The carrier embeds it and the far-side loader refuses
	// any frame that does not hash to it — so an unverified stage-1 is
	// never executed.
	//
	// It is set by the sender: RemoteLauncher.Prepare renders stage-1,
	// computes StageDigest over exactly those bytes, and the caller puts
	// the value here before asking for the command. The two are minted
	// together or not at all — a command whose digest names bytes nobody
	// will send is a far side that blocks on a frame that never arrives.
	StageDigest string
	// BootstrapFD is the inherited descriptor the rcfile writes its two
	// bootstrap progress facts to (internal/bootstrapprogress, nocx-yww2).
	// It is deliberately independent of the lifecycle fields above: the
	// progress channel is not the lifecycle channel, carries no authority
	// and no capability, and is exported on its own so nothing couples the
	// two. Zero means no progress reporting, which is what every remote
	// tier gets — there is no second descriptor to hand a far shell.
	BootstrapFD int
}

// RemoteLauncher builds the command string passed to an SSH session's
// Start() to bring up an integrated interactive shell on the far host.
type RemoteLauncher interface {
	// StartCommand returns the remote command for the given far shell.
	// ok is false when this shell cannot be integrated; reason then says
	// why, and the caller falls back to a plain shell.
	//
	// It is the bounded carrier (carrier.go): under 1 KiB, carrying no
	// bundle bytes and neither secret. What it used to return — the full
	// self-installing launcher — is FullBootstrapCommand below, which is
	// no longer on this seam because no managed session may emit it.
	StartCommand(shell ShellKind, opts LaunchOptions) (cmd string, reason RefusalReason, ok bool)
}

// remoteLauncher is the production RemoteLauncher.
type remoteLauncher struct{}

// NewRemoteLauncher returns the production RemoteLauncher.
func NewRemoteLauncher() RemoteLauncher { return remoteLauncher{} }

// StartCommand implements RemoteLauncher: the bounded carrier, and nothing
// else. See carrier.go for what it is and why the command it replaced could
// not stay.
func (remoteLauncher) StartCommand(shell ShellKind, opts LaunchOptions) (string, RefusalReason, bool) {
	return carrierCommand(shell, opts)
}

// FullBootstrapCommand is the pre-carrier delivery: the self-installing
// launcher that publishes the bundle from inside the remote command and
// then execs the tier. It is ~90 KiB, it carries the capability and the
// recovery fence in its text, and NO managed session may emit it — that is
// the whole point of carrier.go.
//
// It is still here because it is still reachable, from exactly one live
// caller: internal/app/childdomain.go, which composes the `ssh` line a
// nested typed `ssh` runs (ADR-0022). That path has no frame sender yet, so
// converting it now would break nested `ssh` with nothing to replace it;
// design §12 gives it to P4, which retires this function and with it
// fullBootstrapLauncher, maxFullLauncherLen and the publish prelude.
//
// Selection is deliberate per kind: bash and zsh get their launchers,
// ShellUnknown gets the minimal tier — the posix launcher (spec §6: dash /
// busybox ash / POSIX sh are a real tier, verified; refusing them forever
// would contradict D4) — and ShellAuto gets the dispatcher, which carries
// all three tiers and lets the far login shell choose at runtime (the
// only layer that knows which shell it is; nocx-6rj0). The default arm is
// the tripwire for a future ShellKind with no launcher: refuse loudly
// rather than guess.
func FullBootstrapCommand(shell ShellKind, opts LaunchOptions) (string, RefusalReason, bool) {
	switch shell {
	case ShellBash:
		return remoteLauncher{}.bashCommand(opts)
	case ShellZsh:
		return remoteLauncher{}.zshCommand(opts)
	case ShellUnknown:
		return remoteLauncher{}.posixCommand(opts)
	case ShellAuto:
		return remoteLauncher{}.autoCommand(opts)
	default:
		// Never a best-effort guess: an unmapped shell kind is refused
		// outright and the caller falls back to a plain shell.
		return "", ReasonUnsupportedShell, false
	}
}

func launcherEnvBlock(opts LaunchOptions) string {
	var b strings.Builder
	b.WriteString("NOCX_SHELL_INTEGRATION=1\n")
	if opts.Enhanced {
		b.WriteString("NOCX_PROMPT_MODE=marker-only\n")
		b.WriteString("NOCX_SESSION_ID=" + ShellQuote(opts.SessionID) + "\n")
	}
	// Lifecycle channel addressing and transport (ADR-0024). The capability
	// is deliberately NOT here: it reaches the shell by one of the two forms
	// in capability_source.go and must never appear in /proc/<pid>/environ.
	if opts.Lane != "" && opts.Domain != "" && opts.Epoch != 0 && opts.Capability != "" {
		b.WriteString("NOCX_LIFECYCLE_LANE=" + ShellQuote(opts.Lane) + "\n")
		b.WriteString("NOCX_LIFECYCLE_DOMAIN=" + ShellQuote(opts.Domain) + "\n")
		b.WriteString("NOCX_LIFECYCLE_EPOCH=" + fmt.Sprintf("%d\n", opts.Epoch))
		if opts.LifecycleFD > 0 {
			b.WriteString("NOCX_LIFECYCLE_FD=" + fmt.Sprintf("%d\n", opts.LifecycleFD))
		}
		if opts.LifecyclePort > 0 {
			b.WriteString("NOCX_LIFECYCLE_PORT=" + fmt.Sprintf("%d\n", opts.LifecyclePort))
		}
	}
	// The bootstrap progress descriptor (nocx-yww2), in its own block and
	// gated on nothing else: a fd NUMBER is not a secret, it authenticates
	// nothing, and a shell that has this and no lifecycle channel still
	// reports how far its startup got.
	if opts.BootstrapFD > 0 {
		b.WriteString("NOCX_BOOTSTRAP_FD=" + fmt.Sprintf("%d\n", opts.BootstrapFD))
	}
	b.WriteString("export NOCX_SHELL_INTEGRATION")
	if opts.Enhanced {
		b.WriteString(" NOCX_PROMPT_MODE NOCX_SESSION_ID")
	}
	if opts.Lane != "" && opts.Domain != "" && opts.Epoch != 0 && opts.Capability != "" {
		b.WriteString(" NOCX_LIFECYCLE_LANE NOCX_LIFECYCLE_DOMAIN NOCX_LIFECYCLE_EPOCH")
		if opts.LifecycleFD > 0 {
			b.WriteString(" NOCX_LIFECYCLE_FD")
		}
		if opts.LifecyclePort > 0 {
			b.WriteString(" NOCX_LIFECYCLE_PORT")
		}
	}
	if opts.BootstrapFD > 0 {
		b.WriteString(" NOCX_BOOTSTRAP_FD")
	}
	b.WriteString("\n")
	return b.String()
}

// ShellQuote wraps s in single quotes, escaping embedded quotes with the
// POSIX '\” idiom. This is a real escaper, not concatenation that happens
// to work on today's payloads: the launcher strings are built quote-free by
// construction (see printfBEscape), so under a POSIX login shell this is
// usually the identity, but any future payload change that introduces a
// quote stays correct under dash/ash/bash and the other POSIX login shells
// sshd may hand the remote command to.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// printfBEscape encodes payload bytes for transport through the bash
// builtin's `printf %b "<…>"`, which is how the bash launcher delivers its
// rcfile. Inside the double-quoted argument the bytes must survive two
// layers unchanged: the outer `bash -c`'s double-quote processing (where
// `"`, `$`, backtick and backslash are special) and printf's `%b` escape
// scan (where backslash + one of the recognized letters is an escape).
// set becomes `\0` plus exactly three octal digits. The leading zero is
// load-bearing: `%b` parses `\0ddd` as the zero plus up to three further
// octal digits, so `\0` + three digits is exactly four consumed characters
// and never bleeds into a following octal digit (`\012` + `3` would read
// as octal 0123 = 'S'; `\0012` + `3` reads as newline + `3`). Single
// quotes are escaped too, so the whole argument contains no `'` and stays
// parseable by csh login shells as well.
func printfBEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	// Byte-wise on purpose: ranging the string directly iterates per rune
	// and would skip the continuation bytes of multi-byte UTF-8.
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c <= 0x7e && c != '"' && c != '$' && c != '`' && c != '\\' && c != '\'' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('\\')
		b.WriteByte('0')
		b.WriteByte('0' + ((c >> 6) & 7))
		b.WriteByte('0' + ((c >> 3) & 7))
		b.WriteByte('0' + (c & 7))
	}
	return b.String()
}

// maxFullLauncherLen caps the full bootstrap launcher.
//
// It is KEPT, deliberately, and the reasoning is worth a line because the
// carrier design retires everything it guards. The cap and its erosion test
// belong to FullBootstrapCommand, which is no longer on the managed path but
// is still reachable from internal/app/childdomain.go — a live caller whose
// replacement is design §12's P4. A cap deleted while the thing it bounds is
// still emitted would be the worst of both: the code stays, the guard goes.
// P4 removes the caller, this function, the prelude and this number together.
//
// The bound below is not the carrier's. The carrier states its own, two
// orders smaller, in carrier.go (MaxCarrierLen).
//
// The original reasoning: it caps the publish prelude
// (which carries the three generation scripts, the launch carrier and the
// publish logic) plus the tier command. The whole remote command travels as
// ONE argv word (the staged file is command-substituted into the ssh line),
// so Linux's MAX_ARG_STRLEN of 128 KiB per argument is the binding bound —
// macOS's per-argument cap is the whole 256 KiB block on older releases. The
// prelude's embedded payloads are the only inputs that scale with this
// number; a bundle that outgrows the cap must refuse rather than emit a
// command the far host cannot exec. A var, not a const, so tests can prove
// the refusal path.
//
// 120 KiB, raised from 112 KiB on 2026-08-07 (nocx-z9s9.18). The old comment
// said the ShellAuto form was "~97 KiB today", which had stopped being true
// without anybody noticing: measured at the raise it was 112,676 bytes
// against a 114,688-byte cap — 98.2% full, 2 KB of headroom for THREE
// scripts. The intended margin had been eaten a few hundred bytes at a time,
// and nothing failed until a script grew by 2 KB and every remote launch
// refused at once. Adding a line of shell had become impossible, which is
// not a state a limit should be able to reach quietly.
//
// So the number moved, and TestFullLauncherStaysUnderArgLimit now asserts the
// MARGIN rather than the ceiling — erosion is the failure mode, and only a
// test that watches the gap can report it while there is still room to act.
// The real fix landed with nocx-z9s9.17: the shipped payloads are
// comment-stripped at embed time (stripShellComments), so the remote host
// receives the code and none of the ~22 KB of prose the three scripts used
// to carry — every tier now sits ~64 KB under this cap.
var maxFullLauncherLen = 120 * 1024
