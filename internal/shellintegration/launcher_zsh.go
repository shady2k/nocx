package shellintegration

import "strings"

// zshCleanupTrap is the frozen removal of the transient directory, with the
// path substituted at the moment the trap is set. It is set in the .zshenv
// phase — before any user code at all — and re-armed in .zshrc, because a
// user startup file may legitimately install an EXIT trap of its own and
// replace ours. One constant, two set points: the alternative is two
// spellings of one cleanup, which is how a leak survives a reading.
const zshCleanupTrap = `trap "[[ \"$__nocx_bootstrap\" == */nocx-zsh.?????? ]] && rm -rf \"$__nocx_bootstrap\" 2>/dev/null" EXIT`

// zshEnvTemplate and zshProfileTemplate are the transient ZDOTDIR's .zshenv
// and .zprofile — the halves that close the login-shell gap (design §9).
//
// The gap existed because ZDOTDIR names a DIRECTORY: pointing it at ours to
// deliver one file shadowed all four of the user's, and only ~/.zshrc was
// ever replayed. So the user's ~/.zshenv and ~/.zprofile did not run at all,
// in a shell whose whole point was to look like their login. (Their
// ~/.zlogin did run, and had since the .zshrc phase began restoring
// ZDOTDIR — zsh resolves $ZDOTDIR again for every phase — which is what the
// tier's own comment got wrong for months and what a table with a test now
// prevents.)
//
// Each file replays the user's file of the same phase, so the sequence is
// exactly a native login's: /etc/zshenv, ~/.zshenv, /etc/zprofile,
// ~/.zprofile, /etc/zshrc, ~/.zshrc, /etc/zlogin, ~/.zlogin.
const zshEnvTemplate = `# nocx launcher zshenv — the .zshenv phase of the login zsh whose ZDOTDIR
# points at the launcher's transient directory.
__nocx_bootstrap="${ZDOTDIR}"
@TRAP@
__nocx_f="${NOCX_ZDOTDIR_ORIG:-$HOME}/.zshenv"
if [[ -f "$__nocx_f" ]]; then
    . "$__nocx_f"
fi
# ~/.zshenv is the ONE user file that may choose ZDOTDIR, and a native zsh
# reads every later phase from wherever it pointed. Record the new choice as
# the user's — so the phases below replay from there and .zshrc restores it —
# and take the directory back, so our own chain still resolves.
if [[ "${ZDOTDIR-}" != "$__nocx_bootstrap" ]]; then
    NOCX_ZDOTDIR_WAS_SET="${${ZDOTDIR+1}:-0}"
    NOCX_ZDOTDIR_ORIG="${ZDOTDIR-}"
    export NOCX_ZDOTDIR_WAS_SET NOCX_ZDOTDIR_ORIG
fi
export ZDOTDIR="$__nocx_bootstrap"
unset __nocx_f
`

const zshProfileTemplate = `# nocx launcher zprofile — the .zprofile phase, replaying the user's own.
__nocx_f="${NOCX_ZDOTDIR_ORIG:-$HOME}/.zprofile"
if [[ -f "$__nocx_f" ]]; then
    . "$__nocx_f"
fi
unset __nocx_f
`

// zshRcfileTemplate is the generated .zshrc the zsh launcher writes into a
// transient, mode-700 directory and points ZDOTDIR at. zsh has no --rcfile;
// ZDOTDIR names a directory and cannot name a pipe, so the transient
// directory is structural (spec §4.1). Order is pinned: capture the
// bootstrap dir and the original ZDOTDIR, erase the transient dir before
// any user code runs, restore ZDOTDIR preserving its unset-versus-set
// state, source the user's real startup file from the original location,
// and only then install nocx's hooks. @ENV@ is replaced by the session
// environment block and @NOCX_ZSH@ by the embedded nocx.zsh.
//
// What this tier does and does not reproduce is declared in ONE place —
// fidelity.go's startupFidelity table — and not here. What belongs here is
// only what is local to this file:
//
//   - exported variables, cwd, umask, shell options, functions and aliases,
//     traps and history configuration are whatever the user's real startup
//     files leave them; nocx resets none of them;
//   - $0 reports the invoked name ("zsh"); login status comes from the -l
//     flag, so every system startup phase runs natively, and the transient
//     ZDOTDIR replays the USER's file of each phase from their own location
//     (zshEnvTemplate and zshProfileTemplate below) so the order a native
//     `zsh -l` would run is the order that runs;
//   - history: zsh reads the history file before startup files run, so the
//     transient ZDOTDIR shadowed it; HISTFILE is defaulted to the native
//     location and loaded with fc -R when nothing was read, so an
//     exit-time save (which replaces the file) preserves the old history.
//     SAVEHIST and the read/write options are the user's own;
//   - if the user's startup file execs or exits, control never reaches the
//     install below: user startup wins. That outcome is no longer silent —
//     the bootstrap progress descriptor carries "startup entered" before the
//     source and "user rc returned" after it, so the product can say the
//     startup did not return instead of reporting ten seconds of nothing
//     (nocx-yww2). A top-level `return` in the user's file stops only that
//     file — zsh resumes the source — which is indistinguishable from
//     completion, so the install proceeds; that case is a reported
//     limitation, not a silent equivalence (nocx-xs1d).
const zshRcfileTemplate = `# nocx launcher zshrc — runs at the .zshrc phase of the login zsh whose
# ZDOTDIR points at a transient directory created by the launcher.
__nocx_bootstrap="${ZDOTDIR}"
# The frozen trap again (zshCleanupTrap), re-armed: it was set in the
# .zshenv phase, and the user's own .zshenv or .zprofile may have replaced
# the EXIT trap since. The directory is removed a few lines below anyway;
# this covers a startup that dies between here and there.
@TRAP@

# Restore the original ZDOTDIR state (set vs unset), then drop the carrier
# variables before any user code runs.
__nocx_user_zdotdir="${NOCX_ZDOTDIR_ORIG-}"
if [[ "${NOCX_ZDOTDIR_WAS_SET:-}" == 1 ]]; then
    export ZDOTDIR="$__nocx_user_zdotdir"
else
    unset ZDOTDIR
fi
unset NOCX_ZDOTDIR_WAS_SET NOCX_ZDOTDIR_ORIG

# Erase the transient directory before any user code runs (D1: the only
# write the launcher makes, and it is gone before the user's rc).
#
# Removed whole, not file by file. This used to delete the two files the
# launcher writes and then rmdir, which assumed nothing else would ever be in
# there — and something is: zsh has already run /etc/zshenv, /etc/zshrc and
# zshenv by this point, and any of them may write to ZDOTDIR, which is exactly
# where this transient directory is pointing. Debian and Ubuntu ship an
# /etc/zsh/zshrc that runs compinit, whose .zcompdump lands here; rmdir then
# failed and every session left a directory in TMPDIR and a complaint on the
# user's terminal.
#
# The guard is the point, not the rm: the path came from the launcher's own
# mktemp -d of "${TMPDIR:-/tmp}/nocx-zsh.XXXXXX", so matching that shape before
# a recursive delete means an empty or unexpected variable removes nothing.
if [[ "$__nocx_bootstrap" == */nocx-zsh.?????? ]]; then
    rm -rf "$__nocx_bootstrap" 2>/dev/null \
        || print -u2 "nocx: could not remove transient dir $__nocx_bootstrap"
fi

@ENV@

# Bootstrap progress fact 1 of 2 (nocx-yww2): this rcfile began executing.
# The bash tier's block, verbatim in intent and in wire vocabulary — one
# reader parses both (internal/bootstrapprogress), and a second spelling
# would be a second answer to the same question. The descriptor is one-way,
# is NOT the lifecycle channel and confers no authority (ADR-0024 decision
# 4). The group's stderr is discarded and an always-true fallback follows it
# because a descriptor that is not open makes the REDIRECTION fail, which zsh
# reports on the user's terminal and which an rc-set errexit would turn into
# a dead session.
__nocx_bp_fd="${NOCX_BOOTSTRAP_FD:-}"
case "${__nocx_bp_fd}" in
    ''|*[!0-9]*) __nocx_bp_fd='' ;;
    *) { builtin printf 'startup-entered\n' >&"${__nocx_bp_fd}"; } 2>/dev/null || : ;;
esac

# User startup — first, and it wins.
__nocx_user_rc="${__nocx_user_zdotdir:-$HOME}/.zshrc"
if [[ -f "$__nocx_user_rc" ]]; then
    . "$__nocx_user_rc"
fi

# Bootstrap progress fact 2 of 2: the user's startup returned control.
# Written BEFORE __nocx_cap is assigned below, so nothing the user's rc runs
# can see the capability — the fact costs the availability window not one
# byte — and the descriptor is closed immediately after, so no descendant of
# this shell inherits a writer for it.
if [ -n "${__nocx_bp_fd}" ]; then
    { builtin printf 'user-rc-returned\n' >&"${__nocx_bp_fd}"; } 2>/dev/null || :
    { eval "exec ${__nocx_bp_fd}>&-"; } 2>/dev/null || :
fi
unset __nocx_bp_fd

# History: the transient ZDOTDIR shadowed the default history file (zsh
# read history before startup files ran). Default HISTFILE to the native
# location and load it when nothing was read, so an exit-time save replaces
# the file with old+session rather than only this session.
if [[ -z "${HISTFILE:-}" ]]; then
    HISTFILE="${HOME}/.zsh_history"
fi
if (( ${#history} == 0 )); then
    fc -R "${HISTFILE}" 2>/dev/null
fi

# nocx installs last. Re-sourcing after an installer-era gate in the
# user's file is idempotent (add-zsh-hook dedupes; state is unset first).
unset __nocx_loaded __nocx_prompt_wrapped __nocx_owned_session
# The per-epoch capability and the one-shot recovery fence — never exported,
# never in the environment, never in a named file. The block below is one of
# the two forms in capability_source.go: on the remote path the shell READS an
# inherited, already-unlinked descriptor once and closes it; on the local
# child path the values are in this file's own text, which is itself delivered
# through a descriptor. zsh has no export -n — typeset +x removes the
# attribute, and typeset -n is a nameref and must never be used here. An empty
# capability means no authenticated channel and a conventional session.
@CAPSRC@
@NOCX_ZSH@
`

// zshOuterScript is the POSIX-sh script the launcher sends for ShellZsh. It
// is parsed by an explicit `/bin/sh -c` (the login shell may be dash, ash,
// csh or restricted — never rely on it understanding the bootstrap), and
// every byte of the generated .zshrc travels inside `printf %b "<…>"`
// printfBEscape-encoded, so the outer script contains no single quotes at
// all and stays parseable by csh. @ZSHCRC@ is replaced by the encoded rc.
//
// Fail-open is absolute (ADR-0004): a host without mktemp or without a
// writable secure temp degrades to a plain login zsh, never to a dead
// session. ReasonNoSecureTemp exists in the pinned API but cannot be
// emitted from the client — remote temp availability is unknowable at
// build time — so the failure is handled inside the remote script instead.
// The umask is captured before the bootstrap and restored before every
// exec: the session must inherit the user's umask, not the bootstrap's.
const zshOuterScript = `old_umask=$(umask)
umask 077
d=$(mktemp -d "${TMPDIR:-/tmp}/nocx-zsh.XXXXXX") 2>/dev/null || { umask "$old_umask"; exec zsh -l; }
printf %b "@ZSHENV@" > "$d/.zshenv" 2>/dev/null || { umask "$old_umask"; rm -rf "$d"; exec zsh -l; }
printf %b "@ZPROFILE@" > "$d/.zprofile" 2>/dev/null || { umask "$old_umask"; rm -rf "$d"; exec zsh -l; }
printf %b "@ZSHCRC@" > "$d/.zshrc" 2>/dev/null || { umask "$old_umask"; rm -rf "$d"; exec zsh -l; }
umask "$old_umask"
if [ "${ZDOTDIR+x}" = x ]; then export NOCX_ZDOTDIR_WAS_SET=1; else export NOCX_ZDOTDIR_WAS_SET=0; fi
export NOCX_ZDOTDIR_ORIG="${ZDOTDIR-}"
export ZDOTDIR="$d"
exec zsh -l
`

// zshArg builds the outer script the zsh tier runs: a strictly-POSIX sh
// script (parsed by an explicit /bin/sh — the login shell may be dash, ash,
// csh or restricted — never rely on it understanding the bootstrap) that
// writes the transient .zshrc and execs a login zsh with ZDOTDIR pointing at
// it. Every byte of the generated .zshrc travels inside `printf %b "<…>"`
// printfBEscape-encoded, so the outer script contains no single quotes at
// all and stays parseable by csh. It is the piece the ShellAuto dispatcher
// carries as its second positional argument; zshCommand wraps it with
// shellQuote.
//
// Fail-open is absolute (ADR-0004): a host without mktemp or without a
// writable secure temp degrades to a plain login zsh, never to a dead
// session. ReasonNoSecureTemp exists in the pinned API but cannot be
// emitted from the client — remote temp availability is unknowable at
// build time — so the failure is handled inside the remote script instead.
// The umask is captured before the bootstrap and restored before every
// exec: the session must inherit the user's umask, not the bootstrap's.
// zshRcfile renders the generated .zshrc from its template: @ENV@ is the
// session environment block, @CAPSRC@ is where the capability and the fence
// come from (capability_source.go — a descriptor read on the remote path, a
// literal on the local child path) and @NOCX_ZSH@ the nocx.zsh body
// (embedded for the argv launchers, a source of the installed generation
// file for the carrier).
func zshRcfile(envBlock, scriptSource, capSource string) string {
	rc := strings.ReplaceAll(zshRcfileTemplate, "@TRAP@", zshCleanupTrap)
	rc = strings.ReplaceAll(rc, "@ENV@", envBlock)
	rc = strings.ReplaceAll(rc, "@CAPSRC@", capSource)
	rc = strings.ReplaceAll(rc, "@NOCX_ZSH@", scriptSource)
	// Comment-stripped like the generation scripts: the generated .zshrc
	// ships inside the bootstrap payload, and the far shell never reads
	// the template's prose (nocx-z9s9.17). Stripping the rendered text
	// also covers the substituted bodies.
	return stripShellComments(rc)
}

// zshArgFor wraps a rendered .zshrc in the zsh tier's pinned transport: the
// POSIX outer script writes it, and the two login-phase files beside it,
// into a transient ZDOTDIR and execs a login zsh. The three are written
// before ZDOTDIR is exported, and a failed write of any of them removes the
// directory and falls open to a plain login zsh — a transient ZDOTDIR
// holding only some of the phases would shadow the user's other files with
// nothing at all, which is the very gap these two files close. The result is one physical line with no single quotes, so it can
// travel inside the launch carrier and the argv launchers' shellQuote.
func zshArgFor(rc string) string {
	outer := strings.ReplaceAll(zshOuterScript, "@ZSHENV@",
		printfBEscape(stripShellComments(strings.ReplaceAll(zshEnvTemplate, "@TRAP@", zshCleanupTrap))))
	outer = strings.ReplaceAll(outer, "@ZPROFILE@",
		printfBEscape(stripShellComments(zshProfileTemplate)))
	outer = strings.ReplaceAll(outer, "@ZSHCRC@", printfBEscape(rc))
	// One physical line: a csh login shell splits multi-line quoted
	// tokens, so the payload must survive that parse (see singleLine).
	return singleLine(outer)
}

// The per-tier ARG method that used to sit here went with the command that
// consumed it (ADR-0035). It substituted the two bearers into the rcfile TEXT
// and that text travelled inside the remote COMMAND, so both reached the far
// host's process arguments — the defect this epic exists to remove. What
// survives is zshArgFor, which the installed launch carrier uses and which
// carries no per-session value at all.
