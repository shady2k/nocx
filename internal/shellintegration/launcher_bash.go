package shellintegration

import "strings"

// bashRcfileTemplate is the rcfile the bash launcher installs via
// `bash --rcfile <(...)`. It reproduces the startup of an ordinary
// interactive non-login bash — the user's ~/.bashrc runs first and wins —
// then installs nocx's hooks last (ADR-0006: the prompt overlay must
// survive framework prompt initialisation). @ENV@ is replaced by the
// session environment block and @NOCX_BASH@ by the embedded nocx.bash.
//
// What this tier does and does not reproduce is declared in ONE place —
// fidelity.go's startupFidelity table — and not here. The three tiers each
// used to carry their own equivalence set in prose, which is two answers to
// one question in the sense AD-8 names; one of the three had been wrong for
// months and no test could say so. What belongs here is only what is local
// to this file:
//
//   - exported variables, cwd, umask, shell options, functions and aliases,
//     traps and history configuration are whatever the user's ~/.bashrc
//     leaves them; nocx resets none of them;
//   - $0 is "bash", non-login, interactive ([[ $- == *i* ]]) — the login
//     PHASE is reproduced by bashLoginPhase() rather than by the -l flag,
//     because --rcfile is the delivery vehicle and bash ignores it for a
//     login shell;
//   - if the user's ~/.bashrc execs or exits, control never reaches the
//     install below: user startup wins. That outcome is no longer silent —
//     the bootstrap progress descriptor carries "startup entered" before the
//     source and "user rc returned" after it, so the product can say the
//     startup did not return instead of reporting ten seconds of nothing
//     (nocx-yww2). A top-level `return` in the user's file stops only that
//     file — bash resumes the source — which is indistinguishable from
//     completion, so the install proceeds; that case is a reported
//     limitation, not a silent equivalence (nocx-xs1d). The same is true of
//     the system profile the login phase sources, which is inside the same
//     bracketed span for exactly that reason.
//
// BASH_ENV: the outer `bash -c` is non-interactive and would read BASH_ENV
// before this file exists, executing attacker-or-accident code (spec §4.3);
// the launcher strips it with `env -u BASH_ENV` in front of the
// `bash -c` form.
const bashRcfileTemplate = `# nocx launcher rcfile — bash, interactive non-login.
# Reproduces an ordinary interactive non-login bash startup, then installs
# nocx's hooks last. See the Go source for the declared equivalence set.

# Erase the transient rcfile before any user code runs — the same promise the
# zsh tier makes about its transient ZDOTDIR. Unlinking a file bash has already
# read is safe: bash slurps a regular rcfile whole before executing a line of
# it, and an open fd outlives its directory entry regardless.
#
# The guard is the point, not the rm: the path came from the launcher's own
# mktemp of "${TMPDIR:-/tmp}/nocx-bash.XXXXXX", so matching that shape means an
# empty or unexpected BASH_SOURCE removes nothing.
case "${BASH_SOURCE[0]:-}" in
    */nocx-bash.??????) rm -f "${BASH_SOURCE[0]}" 2>/dev/null ;;
esac
@ENV@

# Bootstrap progress fact 1 of 2 (nocx-yww2): this rcfile began executing.
# The descriptor is one-way, is NOT the lifecycle channel, and confers no
# authority at all — see internal/bootstrapprogress and ADR-0024 decision 4.
# Its whole job is to tell "the user's startup took the shell" apart from
# "the shell never started" and "our own bootstrap broke", which are one
# indistinguishable silence without it.
#
# The redirection is inside a group whose stderr is discarded, and the group
# is followed by an always-true fallback: a descriptor that is not open makes
# the REDIRECTION itself fail, which under an inherited errexit would end the
# session, and which prints "bad file descriptor" on the user's terminal
# unless the suppression covers the redirection rather than the command.
__nocx_bp_fd="${NOCX_BOOTSTRAP_FD:-}"
case "${__nocx_bp_fd}" in
    ''|*[!0-9]*) __nocx_bp_fd='' ;;
    *) { builtin printf 'startup-entered\n' >&"${__nocx_bp_fd}"; } 2>/dev/null || : ;;
esac

@LOGINPHASE@
# User startup — first, and it wins.
if [[ -f "${HOME}/.bashrc" ]]; then
    . "${HOME}/.bashrc"
fi

# nocx installs last. Run the install with errexit/xtrace temporarily off:
# a 'set -e'/'set -x' left by the user's rc must not abort or flood the
# install. The user's options are restored immediately after.
__nocx_old_opts="${-}"
set +e +x
# Bootstrap progress fact 2 of 2: the user's startup returned control. Its
# position is load-bearing twice over. It is written AFTER 'set +e', so a
# user rc that left errexit on cannot turn a failed write into a dead
# session; and it is written BEFORE __nocx_cap exists below, so no code the
# user's rc runs can see the capability — the fact costs the availability
# window not one byte. The descriptor is closed immediately afterwards, so
# no descendant of this shell inherits a writer for it.
if [ -n "${__nocx_bp_fd}" ]; then
    { builtin printf 'user-rc-returned\n' >&"${__nocx_bp_fd}"; } 2>/dev/null || :
    { eval "exec ${__nocx_bp_fd}>&-"; } 2>/dev/null || :
fi
unset __nocx_bp_fd
# An installer-era gate line in the user's rc may already have sourced an
# older integration mid-rc. Rewind its captures so the fresh install below
# chains to the user's original traps and PROMPT_COMMAND, not to our own
# wrappers.
if [[ -n "${__nocx_loaded:-}" ]]; then
    if [[ -n "${__nocx_old_debug:-}" ]]; then
        trap "${__nocx_old_debug}" DEBUG
    else
        trap - DEBUG
    fi
    if [[ -n "${__nocx_old_exit:-}" ]]; then
        trap "${__nocx_old_exit}" EXIT
    else
        trap - EXIT
    fi
    if [[ "${PROMPT_COMMAND-}" == "__nocx_prompt_command" ]]; then
        PROMPT_COMMAND="${__nocx_old_pc-}"
    fi
fi
unset __nocx_loaded __nocx_prompt_wrapped __nocx_owned_session \
      __nocx_arm_marker_only __nocx_preexec_done __nocx_in_prompt_command \
      __nocx_first_prompt
# The per-epoch capability and the one-shot recovery fence (ADR-0024
# decision 8) — never exported, never in the environment, never in a named
# file. The block below is one of the two forms in capability_source.go: on
# the remote path the shell READS an inherited, already-unlinked descriptor
# once and closes it; on the local child path the values are in this file's own
# text, which is itself delivered through a descriptor. An empty capability
# means no authenticated channel and a conventional session.
#
# The position is load-bearing: this is AFTER the user's startup file has run
# and returned, so nothing the user's rc executes can see either value. If
# the lifecycle channel dies mid-session, the shell writes the fence to the
# pty at the next prompt boundary and nocx matches it as the restoration
# acknowledgement; a hostile program cannot forge what it never saw.
@CAPSRC@
@NOCX_BASH@
case "${__nocx_old_opts}" in *e*) set -e;; esac
case "${__nocx_old_opts}" in *x*) set -x;; esac
unset __nocx_old_opts
`

// bashArg builds the script `bash -c` parses for the bash tier: write the
// escaped payload to a transient rcfile, then exec the interactive bash that
// reads it. It is the piece the ShellAuto dispatcher carries as its first
// positional argument; bashCommand wraps it with shellQuote.
// ok is false when the pinned Enhanced precondition fails.
//
// The pinned form of the full command is
//
//	env -u BASH_ENV bash -c '<bashOuterScript with the escaped init>'
//
// with three deliberate choices, all documented:
//   - `/usr/bin/env -u BASH_ENV` in front: the outer bash -c is
//     non-interactive and would read BASH_ENV, executing attacker-or-
//     accident code before the rcfile exists (spec §4.3). The inner
//     interactive bash never reads BASH_ENV, so stripping it for the whole
//     chain is exactly what a native session sees. /usr/bin/env exists on
//     every Linux and macOS host (NixOS ships a compatibility shim).
//   - bash is resolved by `env` through PATH, NOT named as /bin/bash.
//     NixOS and Guix keep bash in the store and have no /bin/bash at all, so
//     an absolute path refuses those hosts outright — and, measured on the
//     machine this was written on, skipped every test of this launcher, which
//     is the epic's primary path. `env` still guarantees the explicit
//     interpreter; that guarantee never needed an absolute path.
//   - the rcfile travels through `printf %b` with printfBEscape encoding, so
//     the payload rides inside the command itself and contains no NUL (bEscape
//     never emits one and the embedded script is text). It lands in a mktemp
//     file rather than a pipe — see bashOuterScript for the failure that
//     bought that.
//
// Naming bash explicitly is the point: sshd hands the remote command to the
// user's login shell, which may be dash, ash, csh or a restricted shell, and
// the rcfile this writes is bash's.
//
// bashRcfile renders the bash rcfile from its template. mode says whether
// this rcfile stands in for a login sshd never started (remoteLogin) or is a
// local child that never had one (localChild); @LOGINPHASE@ carries
// bashLoginPhase() in the first case and nothing in the second. @ENV@ is the session
// environment block (launcherEnvBlock for the argv launchers, empty for the
// launch carrier, which exports the stable variables itself before exec),
// @CAPSRC@ is where the capability and the fence come from
// (capability_source.go — a descriptor read on the remote path, a literal on
// the local child path), and @NOCX_BASH@ is the nocx.bash body (embedded for
// the argv launchers, a source of the installed generation file for the
// carrier).
func bashRcfile(mode startupMode, envBlock, scriptSource, capSource string) string {
	login := ""
	if mode == remoteLogin {
		login = loginPhase(ShellBash)
	}
	rc := strings.ReplaceAll(bashRcfileTemplate, "@ENV@", envBlock)
	rc = strings.ReplaceAll(rc, "@LOGINPHASE@", login)
	rc = strings.ReplaceAll(rc, "@CAPSRC@", capSource)
	rc = strings.ReplaceAll(rc, "@NOCX_BASH@", scriptSource)
	// The rendered rcfile ships inside the bootstrap payload, so its
	// template comments are stripped like the generation scripts'
	// (nocx-z9s9.17): the far shell never reads them. The strip runs on
	// the rendered text so the substituted bodies are covered too.
	return stripShellComments(rc)
}

// bashOuterScript is the bash tier's transport: write the rcfile to a real
// file, then exec the interactive bash that reads it. @RC@ is the
// printfBEscape-encoded rcfile.
//
// A FILE, not a process substitution. The pinned form used to be
//
//	exec bash --rcfile <(printf %b "<escaped>") -i
//
// which is a pipe carrying the whole embedded nocx.bash (~21KB). bash reads a
// regular rcfile in one go, but from a pipe it reads what is there, and a
// reader that outpaces its writer gets a short one — so on the macOS CI runner
// the rcfile arrived cut off mid-construct:
//
//	bash: /dev/fd/63: line 415: syntax error: unexpected end of file
//
// and everything past that line, which is the entire nocx install, never ran.
// Scheduling, not syntax: identical bytes on an identical runner image ran
// green before and red after, and it reproduces on demand simply by making the
// payload larger than a pipe can hold. The user-visible defect is a busy
// machine getting a shell with no integration and no error (nocx-azxe.1).
//
// The shape is launcher_zsh.go's, deliberately: that tier already delivered its
// rc as a file through mktemp and removed it from inside, and this one had
// forked a second answer to the same question.
//
// Fail-open is absolute (ADR-0004): a host without mktemp or without a writable
// secure temp degrades to a plain interactive bash, never to a dead session.
// The umask is captured before the bootstrap and restored before every exec —
// the session must inherit the user's umask, not the bootstrap's.
//
// No single quote appears anywhere in it, by construction here and by
// printfBEscape in the payload, so it still travels single-quoted inside the
// launch carrier as well as inside the argv launchers' shellQuote.
const bashOuterScript = `old_umask=$(umask)
umask 077
__nocx_rc=$(mktemp "${TMPDIR:-/tmp}/nocx-bash.XXXXXX") 2>/dev/null || { umask "$old_umask"; exec bash -i; }
printf %b "@RC@" > "$__nocx_rc" 2>/dev/null || { umask "$old_umask"; rm -f "$__nocx_rc"; exec bash -i; }
umask "$old_umask"
exec bash --rcfile "$__nocx_rc" -i
`

// bashArgFor wraps a rendered rcfile in the bash tier's transport (see
// bashOuterScript for why it is a file rather than a pipe).
func bashArgFor(rc string) string {
	return strings.ReplaceAll(bashOuterScript, "@RC@", printfBEscape(rc))
}

// The per-tier ARG method that used to sit here went with the command that
// consumed it (ADR-0035). It substituted the two bearers into the rcfile TEXT
// and that text travelled inside the remote COMMAND, so both reached the far
// host's process arguments — the defect this epic exists to remove. What
// survives is bashArgFor, which the installed launch carrier uses and which
// carries no per-session value at all.
