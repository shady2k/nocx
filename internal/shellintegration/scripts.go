package shellintegration

import _ "embed"

// The embedded scripts are the AUTHORED forms, comments and all — the
// reasoning they carry is why the repo keeps them (nocx-z9s9.17). What ships
// is the stripped form: the remote host is sent the bootstrap payload on
// every launch and never reads a comment, and 62% of nocx.bash was measured
// to be prose. One strip at embed time means every carrier — the argv
// prelude, the Go publisher's bundle, the local install and the inband
// payload — ships the SAME bytes, so the manifest hashes, the publisher's
// byte-identity and the version digest all describe exactly what the far
// side receives.
//
//go:embed scripts/nocx.zsh
var zshScriptRaw string

//go:embed scripts/nocx.bash
var bashScriptRaw string

//go:embed scripts/nocx.posix
var posixScriptRaw string

var (
	zshScript   = stripShellComments(zshScriptRaw)
	bashScript  = stripShellComments(bashScriptRaw)
	posixScript = stripShellComments(posixScriptRaw)
)

// version is the integration script version. Bump when scripts change;
// EnsureInstalled/EnsureInstalledRemote compare this against the installed
// VERSION file and rewrite scripts when they differ. nocx-6b3x: an edited
// script without a bump reaches no shell — every existing install keeps
// sourcing the copy installed the last time the number changed.
//
// 18: the shipped scripts are comment-stripped at embed time (nocx-z9s9.17)
// — same code, no prose — so every install rewrites to the smaller bytes.
//
// 19: the recovery seam (ADR-0024 decision 8) — a failed lifecycle send at a
// prompt boundary clears the active latch, restores a visible native prompt,
// and emits the one-shot recovery fence (nocx-u7uh.15).
//
// 20: __nocx_snapshot_wait_ms is declared once per shell, not once per
// source, so the rcfile's deliberate re-source over an installer-era install
// no longer errors (nocx-u7uh.22).
//
// 21: the handshake wait is a real poll (nocx-u7uh.10). `read -N 0` with a
// nonzero -t returns immediately on an open fd, so a kernel that accepted
// the connection but never answered left the shell blocked in dd with no
// prompt at all; the bounded wait now polls with `read -t 0 -N 0` and a
// sleep loop, and a silent peer times out with the native prompt visible
// (ADR-0024 decision 3/9).
//
// 22: nested environments (nocx-u7uh.11): the parent detects sudo/su/ssh in
// its preexec hook, requests a child domain (domain_request), reads the
// grant (the opaque bootstrap), suspends, and launches the child; the
// parent re-activates only after the child closes. extdebug is on so the
// DEBUG trap can skip the original command, the refresh poll hands the
// channel stream to the child (the §9 ownership interval), and a JSON
// decoder for the grant's bootstrap rides here.
//
// 23: the readiness passport is deleted (nocx-u7uh.11): no OSC 636 P, no
// NOCX_ENVIRONMENT_ID, no nocx_env= tagged marker — the environment
// identity now rides the authenticated lifecycle channel (ADR-0024), and
// the env-id machinery that fed the passport-era renderer is gone.
//
// 24: the zsh tier gets the nested-domain machinery (nocx-u7uh.28): a zsh
// parent entering sudo/su/ssh requests a child domain (domain_request),
// reads the grant, suspends, and launches the child — the bash tier's
// nocx-u7uh.11 flow ported. The interception mechanism differs (the
// accept-line widget replaces the DEBUG-trap skip — zsh's DEBUG trap
// cannot suppress a command), and the descriptor staging is zsh's own
// (exec {var}< <(...) — measured non-CLOEXEC, where bash's coproc/{var}
// are close-on-exec).
//
// 25: the zsh nested launch binds its commands' stdin to /dev/tty and runs
// the precmd chain at the widget's end (nocx-u7uh.28). zle executes a
// widget's commands with stdin at /dev/null (measured), which sent the
// child shell to EOF — bash's EOF behaviour displays `exit` and the child
// never established — and zle does not run the precmd hooks for a line a
// widget consumed, which delayed domain_activated past §9's boundary.
//
// 26: the hello declares max_frame 262144 rather than 65536 (nocx-beib).
// The kernel→shell direction carries the child domain's opaque bootstrap,
// which is a full remote launcher with the bundle embedded (~77 KiB and
// growing). At 64 KiB the grant frame was never written at all, and the
// parent shell sat out its grant timeout before running the user's ssh
// conventionally — the five silent seconds the owner reported. The Go side
// is lifecycle.MaxFrameBytes; the two are held together by
// lifecyclecodec's TestMaxFrameBytes_ShellsDeclareTheSameBound.
//
// 27: the same bound now guards the READER too (nocx-beib). v26 raised the
// advertised max_frame and left `__len <= 65536` in __nocx_lc_read_frame, so
// the shell rejected the ssh child's grant — a whole remote launcher, ~77
// KiB — before parsing it. read_grant failed instantly, the parent ran the
// user's ssh conventionally, and the only visible symptom was that blocks
// never appeared. Each script declares __nocx_lc_max_frame once and uses it
// in both places.
//
// 28: the grant frame is parsed with a shortest-match expansion (nocx-beib).
// `${frame##*"bootstrap":"}` walks every position of a ~78 KiB frame —
// measured 1.65 s per expansion against 1 ms — so the shell spent ten
// seconds between reading the child's grant and using it, and the tab sat
// still after the user typed `ssh host`. The first occurrence is also the
// correct one: the field name precedes its own value.
// 29: the bash-4-only staging constructs (`coproc NAME { }`, `exec {var}>&-`)
// are `eval`ed rather than written inline (nocx-cn86). A version guard cannot
// protect SYNTAX — bash parses a function body whole before running any of it
// — so macOS's bash 3.2 rejected the script at the `coproc` token and every
// shell on the platform this product ships to first came up with no
// integration at all.
// 32: two decoding defects the nested launch died on (nocx-aupk). The zsh
// decoder built \NNN escapes without zero-padding, so a \uXXXX whose octal
// is short swallowed the next character when it was a digit — Go escapes >
// as \u003e, `>0` read as \760, and the byte became 0xF0. And the bash
// probe handed its descriptor to the helper by NUMBER, which a close-on-exec
// channel does not survive: select() on a descriptor that is not open is
// indistinguishable from "no data", so the probe reported empty forever on a
// channel with a frame waiting. It arrives on the helper's stdin now, by the
// same redirection dd uses.
//
// 31: the grant's JSON is decoded by the same helper (nocx-aupk). bash 3.2's
// ${var//pattern/replacement} is quadratic and this decodes a whole rcfile —
// measured 4655ms for 22 KiB against 8ms on bash 5, a factor of 580 — so a
// nested launch spent seconds decoding while the user watched a frozen
// terminal. Same cliff as nocx-beib, different string operation.
//
// 30: the readable-probe has one owner and a per-version answer
// (nocx-sw4p). `read -N` is bash 4.1+, so on macOS's 3.2 the probe could
// never succeed, the accept was never read and the channel never activated —
// a defect a parse check cannot see, because `-N` is an option and not
// syntax. bash 3.2 gets perl's select(); the descriptor and port are now
// pinned to digits, since the first of those is interpolated into a program.
// 33: the session nonce, the staging file and the hello are minted ONCE PER
// SHELL rather than once per source (nocx-cbtc). The launcher rcfile unsets
// __nocx_loaded and sources this script a second time on purpose — that is
// every local enhanced session — and the second pass announced a second
// hello with a fresh nonce. command-snapshot.ts keeps the FIRST hello by
// design (accepting a re-hello is the re-anchoring its forgery defence
// exists to prevent), so the snapshot, emitted from a prompt with the second
// nonce, failed the match and was discarded. The store stayed `unavailable`
// for the life of every such session and command completion never learned a
// single command name — the bash twin of nocx-qduc, and what
// completion.spec.ts:139 had been reporting all along.
// The latch is the shell's own pid, not the nonce's presence: "is the
// variable set" cannot tell a re-source from an INHERITED value, and those
// need opposite answers. A nonce that reached the environment (a user rc
// under `set -a`) would otherwise silence a legitimately new child shell for
// good — no hello, so no snapshot ever accepted — which is fail-closed, the
// wrong direction for this file. Both directions are pinned by tests, so
// neither can be satisfied by dropping the other.
//
// 34: an interrupt no longer announces nocx's own line as the user's command
// (nocx-678o). extdebug fires the DEBUG trap inside functions, and the
// wrapper suppresses that two ways — a command text starting `__nocx_`, and
// __nocx_in_prompt_command. `__nocx_prompt_command`'s status capture,
// `local __nocx_exit=$?`, satisfied NEITHER: it begins with `local`, and the
// flag went up four lines below it. One unguarded command per prompt cycle,
// invisible after a real command (the C latch is disarmed by then) and NOT
// after an interrupt, where nothing ran and __nocx_precmd armed the latch at
// the previous prompt. So Ctrl-C emitted an OSC 133 C and sent the kernel a
// start naming `local __nocx_exit=$?` with a complete carrying SIGINT's
// status. The capture is now a `__nocx_`-prefixed global, so its own text
// matches the skip, and the flag goes up on the line after it, so everything
// below is covered by the flag instead; `local` cannot come first because it
// would reset $?.
// 35: the ssh options the user typed reach the line that actually runs
// (nocx-c6z0). The detector NAMES -i, -o, -F, -J, -l, -e, -b, -c and -m —
// it accepts a line carrying them and skips each one's argument — and then
// sent only host, user and port, which is all composeSSHChildLine had to
// rebuild the line from. So `ssh -i ~/.ssh/prod -J bastion host` went out as
// a bare `ssh host`: the wrong key, no jump host, the default host-key
// policy. Silent in both directions, because the block a user sees shows the
// line they TYPED. The detector now collects the tokens it already
// recognises and sends them as `opts`; -p is excluded because it is modelled
// as the port, and -t because the composer adds its own and ssh reads a
// second one as -tt.
// 37: zsh emits the command-existence snapshot (nocx-qduc). nocx.zsh had the
// OSC 133 markers, OSC 7 and the whole authenticated channel, and no hello
// and no snapshot at all — so on a zsh session the frontend's snapshot store
// stayed `unavailable` for the life of the tab and the completion dropdown
// answered "Command names are still loading" forever. macOS's default login
// shell is zsh, which is where that matters. The protocol is the bash tier's
// unchanged — one hello before the first prompt, one snapshot under that
// nonce, the same escaping and the same caps — because the frontend has one
// parser and AD-8 wants one owner for the format; what differs is mechanism,
// and each difference is named where it is made: zsh keeps one parameter per
// command table (so the enumeration is their union), sorts and dedupes in the
// shell rather than through `sort -u`, sleeps with zsh/zselect rather than
// forking `sleep`, disowns with `&!`, and chains its cleanup onto the zshexit
// hook array instead of saving the user's EXIT trap.
// 38: the accept-line chain invokes a WIDGET rather than a function name
// (nocx-wwz0). `zle -lL accept-line` reports the function that implements the
// widget, and the interception called that name straight back through `zle`,
// which only works when a framework happens to have registered a widget of the
// same name. fast-syntax-highlighting, zsh-syntax-highlighting and
// zsh-autosuggestions do not — so on a machine with any of them, pressing
// Enter printed "No such widget `_zsh_highlight_widget_orig-…-accept-line'" and
// the command never ran. Latent until nocx-wwz0 gave the local tier a zsh to
// run at all, and then reproduced on the first real machine it met. The
// previous implementation is now registered under a name nocx owns, guarded
// against a completion widget, against our own function (the launcher's
// deliberate second source would chain to itself), and against an
// implementation that is not a callable function.
// 39: nested sudo is capability-gated before the authenticated parent is
// suspended. sudo implementations without --preserve-fds now run the user's
// original command conventionally instead of rejecting nocx's generated argv
// and consuming the command. BASHOPTS is scrubbed at the privileged child
// boundary so nocx's internal extdebug never makes the child attempt bashdb.
// 40: the snapshot carries the SESSION-LOCAL tables only — aliases,
// builtins, keywords and functions — and no longer the PATH executables
// (nocx-m8jwn.6, carrier design §8). The split is by whose truth it is: a
// function belongs to one shell and may never be cached for another, while
// the PATH set is identical for every session to the same host and is
// expensive to enumerate. That half is now computed once per host by
// internal/commandnames, shared across every tab, and invalidated on the
// mtime of each PATH directory — ten tabs in an hour used to mean ten full
// scans of thousands of files, and the 250 ms budget in front of them
// stopped the WAIT rather than the WORK. The name cap on this payload drops
// from 8192 to 4096 with it: the session-local half has its own bound and
// the shared half has another.
const version = "41"

// ScriptVersion is the integration script version other packages may read.
// Command discovery puts it in its cache key (internal/commandnames): the
// scripts decide what the session-local half enumerates, so a name set
// computed under one version must never be served to a session running
// another.
func ScriptVersion() string { return version }

// promptModeEnvVar is the env var that selects the prompt mode.
const promptModeEnvVar = "NOCX_PROMPT_MODE"

// promptModeMarkerOnly is the marker-only prompt mode value.
const promptModeMarkerOnly = "marker-only"

// sessionIDEnvVar is the env var for the nocx session identifier.
const sessionIDEnvVar = "NOCX_SESSION_ID"

// dirName is the directory name inside the user's home.
const dirName = ".nocx"

// versionFile is the marker file written alongside the scripts.
const versionFile = "VERSION"

// activationEnvVar is the env var the shell rc gate checks.
const activationEnvVar = "NOCX_SHELL_INTEGRATION"

// gateLineZsh is appended to ~/.zshrc to load the integration.
const gateLineZsh = `# nocx terminal shell integration
[[ -n "$NOCX_SHELL_INTEGRATION" ]] && source "$HOME/.nocx/shell-integration.zsh"`

// gateLineBash is appended to ~/.bashrc to load the integration.
const gateLineBash = `# nocx terminal shell integration
[[ -n "$NOCX_SHELL_INTEGRATION" ]] && source "$HOME/.nocx/shell-integration.bash"`

// scripts maps installed filename → embedded script content. Every entry is
// installed by EnsureInstalled and EnsureInstalledRemote; adding one means
// deciding its markers (scriptMarkers) and bumping `version`, or existing
// installs never receive it.
var scripts = map[string]string{
	"shell-integration.zsh":   zshScript,
	"shell-integration.bash":  bashScript,
	"shell-integration.posix": posixScript,
}

// rcGate maps rc filename → gate line to append.
var rcGate = map[string]string{
	".zshrc":  gateLineZsh,
	".bashrc": gateLineBash,
}
