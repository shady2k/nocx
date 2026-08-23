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
	// never a named file. On the local child path — sudo and su, where the
	// rcfile travels in a preserved descriptor rather than a command — it is
	// substituted into the rcfile TEXT instead (capability_source.go names
	// both forms and why each is safe). Lane, Domain and Epoch are names,
	// not secrets.
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
	// bundle bytes and neither secret. The full self-installing launcher it
	// replaced is gone from the repository: its last caller, the nested
	// typed `ssh` in internal/app/childdomain.go, emits this instead
	// (ADR-0035), and with the caller went the launcher, the publish
	// prelude and the argument-length cap that guarded them.
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
