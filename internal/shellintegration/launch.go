package shellintegration

import "strings"

// The compact carrier ~/.nocx/launch (design §3.3): a stable POSIX sh
// script, mode 0700, installed once and never rewritten by a generation
// publish. It reads ONLY manifest.json — the activation pointer — and
// re-proves the activation (every generation file exists with the recorded
// hash) before exec'ing the integrated shell. Any refusal — a missing,
// truncated, hash-mismatched or protocol-incompatible manifest — execs a
// native login shell and emits no passport: the session is ordinary, and the
// next connection bootstraps again (§3.2). An `ssh` that fails with 127 is a
// bug in this design, never a user-visible outcome.
//
// The argument is the session id (AD-7), exported as NOCX_SESSION_ID and
// validated by the shell scripts themselves — an absent id means no
// marker-only mode (fail-open). The nocx-mlm7 P7 amendment of the
// 2026-08-05 delivery-modes design: the compact path carries the session
// id exactly like the argv launchers do, so the ownership handshake is
// not degraded on installed hosts.
//
// Unlike the argv launchers this is a FILE, so it is authored multi-line with
// comments; only the three tier payloads (@BASH_ARG@ etc.) must stay free of
// single quotes, because they are embedded single-quoted here and travel
// through printf %b decoding inside the tier transports.

const launchCarrierTemplate = `#!/bin/sh
# nocx launch carrier — the compact activation entry point (design §3.3).
# Reads manifest.json only; refuses an incomplete or protocol-incompatible
# generation and in that case execs a native login shell.
__nocx_root="${HOME}/.nocx"
__nocx_manifest="$__nocx_root/manifest.json"
__nocx_protocol_version="1"
# Captured before __nocx_outcome unsets it: the banner and the outcome are
# gated on the same fact, that this run came from a bootstrap.
__nocx_boot="${@BOOTENV@:-}"

# The terminal outcome of the bootstrap (design §5.3), and the only place it
# is decided: this script is the one that knows whether the generation it is
# about to exec has just been re-proved. It closes the input quarantine, so
# exactly one of these is emitted per bootstrap and nothing else emits one.
#
# It is gated on @BOOTENV@, which stage-1 exports and this function unsets, so
# a launch that did NOT come from a bootstrap — anything that runs this
# carrier directly — cannot put protocol tokens on a user's terminal. The
# unset also keeps the marker out of the shell the user ends up with.
__nocx_outcome() {
    if [ -n "${@BOOTENV@:-}" ]; then
        printf '@OUTPFX@%s\n' "$1"
    fi
    unset @BOOTENV@
}

# The login banner sshd skipped (design §9; fidelity.go owns the set of
# differences and both paths below). sshd prints @MOTD@ when it starts an
# interactive session itself; nocx hands it a COMMAND, so it updates the
# login records and prints nothing — and ~/@HUSH@, the file the user
# already uses to say "not on this host", is never consulted either. Both
# halves are reproduced here.
#
# Here, and only here: this script is the one point every tier passes
# through, so the banner is printed once per bootstrap whichever tier runs
# and whether or not the generation was accepted. Three tier rcfiles would
# have been three answers to one question, and a user who reaches the
# native fallback still logged in.
#
# It runs AFTER the outcome line, which is what closes the bootstrap window
# (design §5.5): the reader matches a closed token vocabulary as framed
# bytes, and the banner is somebody else's arbitrary text.
__nocx_motd() {
    [ -n "$__nocx_boot" ] || return 0
    [ -e "${HOME}/@HUSH@" ] && return 0
    cat "@MOTD@" 2>/dev/null || :
}

__nocx_native() { __nocx_outcome @NOGEN@; __nocx_motd; exec "${SHELL:-/bin/sh}" -l; }

# --- the manifest must exist and parse as ours -------------------------------
[ -f "$__nocx_manifest" ] || __nocx_native
# Both writers' manifests are parsed: the Go publisher writes pretty-printed
# JSON, the sh publish writes compact. Every manifest string value is a safe
# name, a hex hash, an octal mode or an integer — none may contain whitespace
# — so stripping all whitespace normalises either format to the same compact
# text the extractions below match.
__nocx_m=$(tr -d '[:space:]' < "$__nocx_manifest" 2>/dev/null)
[ -n "$__nocx_m" ] || __nocx_native
__nocx_protocol=$(printf '%s' "$__nocx_m" | grep -o '"protocol":[0-9][0-9]*' | head -n 1 | cut -d: -f2)
[ "$__nocx_protocol" = "$__nocx_protocol_version" ] || __nocx_native
__nocx_generation=$(printf '%s' "$__nocx_m" | grep -o '"generation":"[^"]*"' | head -n 1 | cut -d\" -f4)
case "$__nocx_generation" in [A-Za-z0-9][A-Za-z0-9._-]*) ;; *) __nocx_native ;; esac
[ "${#__nocx_generation}" -le 64 ] || __nocx_native

# --- per-file proof: every manifest entry exists with the recorded hash ------
__nocx_sha() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" 2>/dev/null | cut -d" " -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" 2>/dev/null | cut -d" " -f1
    fi
}
for __nocx_f in nocx.bash nocx.zsh nocx.posix; do
    __nocx_expected=$(printf '%s' "$__nocx_m" | grep -o "\"$__nocx_f\":{\"hash\":\"[^\"]*\"" | head -n 1 | cut -d\" -f6)
    [ -n "$__nocx_expected" ] || __nocx_native
    __nocx_file="$__nocx_root/integration/$__nocx_generation/$__nocx_f"
    [ -f "$__nocx_file" ] && [ ! -L "$__nocx_file" ] || __nocx_native
    __nocx_actual=$(__nocx_sha "$__nocx_file")
    [ -n "$__nocx_actual" ] || __nocx_native
    [ "$__nocx_expected" = "sha256:$__nocx_actual" ] || __nocx_native
done
export NOCX_SESSION_ID="${1-}"
export NOCX_GENERATION="$__nocx_generation"
export NOCX_SHELL_INTEGRATION=1
export NOCX_PROMPT_MODE=marker-only
# The generation is proved and about to run: this is the accepted outcome, and
# it is emitted BEFORE the exec because after it there is no longer a script
# here to emit anything.
__nocx_outcome @ACCEPTED@
__nocx_motd
# $2 is the profile's pinned shell, carried by stage-1 (design §4.1 keeps a
# shell name out of the command, and there is no room for one there). It wins
# over $SHELL when it names a tier: a user who says "this host runs zsh" knows
# something the far side's $SHELL does not. Empty is not a missing value — it
# is ShellAuto, "the far host decides", which is exactly the $SHELL dispatch.
case "${2-}" in
    bash)    exec /usr/bin/env -u BASH_ENV bash -c '@BASH_ARG@' ;;
    zsh)     exec /usr/bin/env -u BASH_ENV /bin/sh -c '@ZSH_ARG@' ;;
    unknown) exec /usr/bin/env -u BASH_ENV /bin/sh -c '@POSIX_ARG@' ;;
esac
case "${SHELL:-/bin/sh}" in
    */bash) exec /usr/bin/env -u BASH_ENV bash -c '@BASH_ARG@' ;;
    */zsh)  exec /usr/bin/env -u BASH_ENV /bin/sh -c '@ZSH_ARG@' ;;
    *)      exec /usr/bin/env -u BASH_ENV /bin/sh -c '@POSIX_ARG@' ;;
esac
`

// launchSourceLine returns the line that sources one generation file: the
// carrier knows the committed generation at runtime (NOCX_GENERATION, exported
// above) and the rcfile templates place the source after the user's startup
// files, so the user's rc still runs first and still wins. The POSIX dot is
// deliberate: the minimal tier's ENV file is parsed by dash / busybox ash /
// ksh, none of which know bash's `source`; `.` is understood by all of them
// and by bash and zsh.
//
// stderr is suppressed on purpose: when the publish prelude failed,
// NOCX_GENERATION is unset, the path names no file, and the session must
// land in a CLEAN visible native prompt (ADR-0024 decision 4) — not a
// conventional terminal with a shell error line on it.
func launchSourceLine(name string) string {
	return `. "${HOME}/.nocx/integration/${NOCX_GENERATION}/` + name + `" 2>/dev/null`
}

// tierArg is the payload one tier ships: the rendered startup file wrapped in
// that tier's pinned transport, with the rcfile body SOURCING the installed
// generation file rather than embedding it.
//
// It is one function rather than three expressions inlined into launchCarrier
// because "what does tier X ship" is one question, and the carrier is no
// longer the only thing that asks it — a test asking the same question by
// re-deriving the expression is a second answer that agrees until the day it
// does not (AD-8). Neither bearer appears in any of the three: the capability
// and the fence reach the far shell through an inherited descriptor, which is
// what capabilityFromDescriptor renders and what the per-tier ARG METHODS
// that used to build these payloads did not (ADR-0035).
//
// envBlock is the session environment the tier exports. The carrier passes ""
// — it is published once and reused by every session, so it can carry nothing
// per-session at all; a caller rendering a payload for one session passes
// launcherEnvBlock(opts). An unknown kind returns "", which the carrier's
// template would show as an empty arm rather than a wrong one.
func tierArg(kind ShellKind, envBlock string) string {
	switch kind {
	case ShellBash:
		return bashArgFor(bashRcfile(remoteLogin, envBlock, launchSourceLine("nocx.bash"),
			capabilityFromDescriptor(bashUnsetExport)))
	case ShellZsh:
		return zshArgFor(zshRcfile(envBlock, launchSourceLine("nocx.zsh"),
			capabilityFromDescriptor(zshUnsetExport)))
	case ShellUnknown:
		return posixArgFor(posixEnvFile(envBlock, launchSourceLine("nocx.posix")))
	default:
		return ""
	}
}

// launchCarrier renders the compact carrier: the template with the three tier
// payloads substituted. The payloads are the same pinned transports the argv
// launchers use, but the rcfile bodies source the INSTALLED generation files
// instead of embedding the scripts — a stable carrier over a changing bundle.
// The rendered text is comment-stripped like the generation scripts: the
// carrier ships inside the bootstrap payload on every launch (and to every
// install), and the far side never reads the prose (nocx-z9s9.17). The
// template keeps its comments — the carrier is a FILE, authored multi-line
// for the humans who read the source — and only the shipped bytes shrink.
// The shebang survives the strip, so `exec "$HOME/.nocx/launch"` keeps
// working.
func launchCarrier() string {
	s := strings.ReplaceAll(launchCarrierTemplate, "@BASH_ARG@", tierArg(ShellBash, ""))
	s = strings.ReplaceAll(s, "@ZSH_ARG@", tierArg(ShellZsh, ""))
	s = strings.ReplaceAll(s, "@POSIX_ARG@", tierArg(ShellUnknown, ""))
	// The bootstrap vocabulary the far side speaks, from the constants that
	// declare it: the marker stage-1 exports, the outcome prefix and the two
	// outcomes this script can name.
	s = strings.NewReplacer(
		"@BOOTENV@", BootstrapEnv,
		"@MOTD@", motdPath(),
		"@HUSH@", hushloginFile,
		"@OUTPFX@", OutcomePrefix,
		"@ACCEPTED@", OutcomeToken(OutcomeBootstrapAccepted),
		"@NOGEN@", OutcomeToken(OutcomeGenerationUnavailable),
	).Replace(s)
	return stripShellComments(s)
}

// launchBundle assembles the bundle descriptor both carriers publish (AD-8):
// the three generation scripts (data, 0600) and the launch carrier (0700).
// The version is the embedded scripts' own (scripts.go), so the manifest and
// the passport generation field stay in lockstep with what the shells source.
// validateBundle's constraints are the contract; a change here must satisfy
// them and the bidirectional conformance tests.
func launchBundle() Bundle {
	return Bundle{
		Protocol: ProtocolVersion,
		Version:  version,
		Files: []BundleFile{
			{Name: "nocx.bash", Mode: 0o600, Data: []byte(bashScript)},
			{Name: "nocx.zsh", Mode: 0o600, Data: []byte(zshScript)},
			{Name: "nocx.posix", Mode: 0o600, Data: []byte(posixScript)},
			{Name: launchName, Mode: 0o700, Data: []byte(launchCarrier())},
		},
	}
}
