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

__nocx_native() { __nocx_outcome @NOGEN@; exec "${SHELL:-/bin/sh}" -l; }

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
	s := strings.ReplaceAll(launchCarrierTemplate, "@BASH_ARG@",
		bashArgFor(bashRcfile("", launchSourceLine("nocx.bash"),
			capabilityFromDescriptor(bashUnsetExport))))
	s = strings.ReplaceAll(s, "@ZSH_ARG@",
		zshArgFor(zshRcfile("", launchSourceLine("nocx.zsh"),
			capabilityFromDescriptor(zshUnsetExport))))
	s = strings.ReplaceAll(s, "@POSIX_ARG@",
		posixArgFor(posixEnvFile("", launchSourceLine("nocx.posix"))))
	// The bootstrap vocabulary the far side speaks, from the constants that
	// declare it: the marker stage-1 exports, the outcome prefix and the two
	// outcomes this script can name.
	s = strings.NewReplacer(
		"@BOOTENV@", BootstrapEnv,
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
