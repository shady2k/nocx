package shellintegration

import (
	"fmt"
	"strings"
)

// ShellKind names the far shell a launcher builds a start command for.
type ShellKind string

const (
	ShellBash ShellKind = "bash"
	ShellZsh  ShellKind = "zsh"
	// Fish uses the helper-hosted inherited-descriptor adapter; it does not
	// claim the helper-less remote carrier's TCP transport.
	ShellFish    ShellKind = "fish"
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
	ReasonNone                   RefusalReason = ""
	ReasonUnsupportedShell       RefusalReason = "unsupported-shell"
	ReasonNoSecureTemp           RefusalReason = "no-secure-temp"
	ReasonUnsupportedFishStartup RefusalReason = "unsupported-fish-startup"
)

// ToolSocketEnvVar is the one spelling of the environment variable a shell
// reads its nocx tool endpoint from (scripts/nocx.bash, scripts/nocx.zsh).
// Every writer of it — this file's two renderers and, locally, the
// composition root that hands the running endpoint's path down to the
// helper daemon that forks the shell (internal/app/helper_local.go,
// cmd/nocx-helper/main.go) — names it through this constant rather than
// its own copy of the string, so the shell and every Go writer of the
// variable can never spell it two different ways (nocx-2tesu).
const ToolSocketEnvVar = "NOCX_TOOL_SOCKET"

// LaunchOptions carries what the start command must embed.
type LaunchOptions struct {
	SessionID string // NOCX_SESSION_ID for this session; never empty when Enhanced
	Enhanced  bool   // request marker-only prompt mode (ADR-0006)
	// Agents is the ENABLED agent set this shell's wrappers are generated from
	// (nocx-t5e7d): the record's own answer, carried to the helper as a LIST by
	// the spawn request that asks for the pane.
	//
	// It is a list rather than a directory to go and read, deliberately. The
	// helper is a separate process and is built WITHOUT the release tag, so
	// storage.ConfigDir() in it would resolve the development profile under a
	// release app; handing it a list keeps "which agents does this machine
	// offer" a fact the backend owns and the helper receives, and keeps the
	// helper out of the app directory altogether.
	//
	// Empty means this BUILD's set, which is what a caller with no record to
	// read — a test, or the in-memory launch of a build that has none — must
	// get rather than a shell that quietly stopped offering anything.
	Agents []string
	// AgentHelperPath and AgentToolSocketPath are non-secret paths used by the
	// launch-owned MCP bridge. They are exported only when supplied; the
	// lifecycle capability and report rendezvous remain outside this config.
	AgentHelperPath     string
	AgentToolSocketPath string
	// EnvOverrides is the pending pane's explicit environment, before the
	// startup adapter adds its own entries. Fish temporarily replaces its
	// function path and must restore the effective original, not the helper's.
	EnvOverrides []string
	// AgentToolsAbsent says why this pane's agent has NO tool surface, from
	// the closed set in agenttools.go. It is set exactly when both paths above
	// are empty and nocx knows the reason — an ssh pane on a host with no
	// installed helper, and any nested ssh (AgentToolsNoHelperOnHost) — and it
	// travels as a non-secret environment entry so the shell's stage can name
	// the reason to the person instead of reporting a path it was never given.
	//
	// Empty means "nocx did not say", which is a different state from any code
	// in the set: a local pane whose coordinator runs no endpoint is a
	// configuration a person can change, and the shell keeps its own sentence
	// for it rather than being told the host has no helper.
	AgentToolsAbsent AgentToolsAbsent
	// AgentToolToken is the pane's tool bearer (nocx-50w7p.16): what the far
	// agent's MCP bridge presents to be admitted, and what the pane's epoch
	// bounds. It is LOWER-CASE HEX and it is a SECRET, so it travels by one of
	// the two bearer transports and no others (capability_source.go, design
	// D4): on the remote path it is a line in frame 2's payload, read once
	// into a NON-EXPORTED shell variable and staged into the launch
	// directory's mcp.json — never in this struct's env block, never in argv,
	// never exported, and never a name on a filesystem outside that directory.
	//
	// It is a different value from Capability and is not a second spelling of
	// it: Capability addresses the lifecycle channel, this admits tool calls,
	// and each is bounded by its own interval.
	AgentToolToken string
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
	// (ADR-0049), and with the caller went the launcher, the publish
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
	forLauncherEnv(opts, func(name, value string) {
		b.WriteString("export " + name + "=" + ShellQuote(value) + "\n")
	})
	return b.String()
}

// forLauncherEnv owns the public launch fields for every shell dialect.
// Bearers are deliberately absent: their only carrier is private bootstrap I/O.
func forLauncherEnv(opts LaunchOptions, emit func(name, value string)) {
	emit("NOCX_SHELL_INTEGRATION", "1")
	if opts.Enhanced {
		emit("NOCX_PROMPT_MODE", "marker-only")
		emit("NOCX_SESSION_ID", opts.SessionID)
	}
	if opts.AgentHelperPath != "" {
		emit("NOCX_AGENT_HELPER_PATH", opts.AgentHelperPath)
	}
	if opts.AgentToolSocketPath != "" {
		emit(ToolSocketEnvVar, opts.AgentToolSocketPath)
	}
	if opts.AgentToolsAbsent != "" {
		emit(AgentToolsAbsentEnvVar, opts.AgentToolsAbsent.String())
	}
	if opts.Lane != "" && opts.Domain != "" && opts.Epoch != 0 && opts.Capability != "" {
		emit("NOCX_LIFECYCLE_LANE", opts.Lane)
		emit("NOCX_LIFECYCLE_DOMAIN", opts.Domain)
		emit("NOCX_LIFECYCLE_EPOCH", fmt.Sprintf("%d", opts.Epoch))
		if opts.LifecycleFD > 0 {
			emit("NOCX_LIFECYCLE_FD", fmt.Sprintf("%d", opts.LifecycleFD))
		}
		if opts.LifecyclePort > 0 {
			emit("NOCX_LIFECYCLE_PORT", fmt.Sprintf("%d", opts.LifecyclePort))
		}
	}
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
