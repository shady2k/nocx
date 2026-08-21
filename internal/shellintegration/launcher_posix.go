package shellintegration

import "strings"

// posixEnvFileTemplate is the ENV file the posix launcher writes into a
// transient, mode-700 directory and points ENV at. Interactive POSIX sh
// (dash, busybox ash, ksh) reads $ENV before the first prompt, so the file
// is the minimal tier's delivery vehicle — the equivalent of bash's
// --rcfile and zsh's transient ZDOTDIR. POSIX sh has no --rcfile and no
// process substitution, so a transient file is structural, exactly as the
// transient directory is for zsh (spec §4.1). @ENV@ is replaced by the
// session environment block and @NOCX_POSIX@ by the embedded nocx.posix.
//
// The file erases its own directory before any nocx code runs (the path is
// frozen into NOCX_POSIX_BOOTSTRAP by the outer script, so a user ~/.profile
// that rewrites ENV cannot make the cleanup remove the wrong file): the
// launcher makes exactly one write and it is gone before the user's first
// command. A nested interactive sh then inherits an ENV path that no longer
// exists and starts plain — the same shape the zsh launcher's ZDOTDIR
// erasure gives nested zshs without a persistent install.
//
// Ordering: dash and busybox ash read /etc/profile and ~/.profile BEFORE the
// ENV file, at the first prompt — so, unlike the bash and zsh launchers, the
// session environment block is NOT visible to ~/.profile, and the posix
// prompt is installed after the user's profile had its say, which is what
// makes the markers win.
const posixEnvFileTemplate = `# nocx launcher env file — sourced by an interactive POSIX sh whose
# ENV points at this transient file.
__nocx_bootstrap="${NOCX_POSIX_BOOTSTRAP-}"
rm -f "$__nocx_bootstrap" 2>/dev/null
rmdir "$(dirname "$__nocx_bootstrap")" 2>/dev/null || :
unset NOCX_POSIX_BOOTSTRAP __nocx_bootstrap
@ENV@
@NOCX_POSIX@
`

// posixOuterScript is the POSIX-sh script the launcher sends for
// ShellUnknown. It is parsed by an explicit `/bin/sh -c` (the login shell
// may be dash, ash, csh or restricted — never rely on it understanding the
// bootstrap), and every byte of the generated ENV file travels inside
// `printf %b "<…>"` printfBEscape-encoded, so the outer script contains no
// single quotes at all and stays parseable by csh. @POSIXENV@ is replaced
// by the encoded env file.
//
// `exec "${SHELL:-/bin/sh}" -l` rather than a named binary: for ShellUnknown
// the far shell's name is by definition not known, and $SHELL is the login
// process's own choice — the "start ordinary sh" of the spec's sh-only
// matrix row. Fail-open is absolute (ADR-0004): a host without mktemp or
// without a writable secure temp degrades to a plain login shell, never to
// a dead session; and a shell that ignores ENV (fish, csh, a bash or zsh
// misdetected as unknown) starts plain and unintegrated — exactly the
// refusal outcome this replaces.
const posixOuterScript = `old_umask=$(umask)
umask 077
d=$(mktemp -d "${TMPDIR:-/tmp}/nocx-posix.XXXXXX") 2>/dev/null || { umask "$old_umask"; exec "${SHELL:-/bin/sh}" -l; }
printf %b "@POSIXENV@" > "$d/env" 2>/dev/null || { umask "$old_umask"; exec "${SHELL:-/bin/sh}" -l; }
umask "$old_umask"
export NOCX_POSIX_BOOTSTRAP="$d/env"
ENV="$d/env" exec "${SHELL:-/bin/sh}" -l
`

// posixArg builds the minimal-tier outer script for a shell that is neither
// bash nor zsh (ShellUnknown, or the ShellAuto dispatcher's fallback arm).
// The outer form is the zsh launcher's own — `/usr/bin/env -u BASH_ENV
// /bin/sh -c '<script>'` — so a bash-as-/bin/sh host (macOS) cannot execute
// BASH_ENV code in the outer sh (spec §4.3), and the payload is POSIX-only:
// never bash/zsh syntax. It is the piece the ShellAuto dispatcher carries as
// its third positional argument; posixCommand wraps it with shellQuote.
//
// The deliberate decision behind ShellUnknown → posix, rather than keeping
// the old refusal: the spec (§6, D4) names `minimal` as a real, verified
// tier for dash / busybox ash / POSIX sh — precisely the shells that are
// neither bash nor zsh — and refusing them forever would strand every
// sh-only remote at no integration, contradicting the design. Refusal now
// means "no integration exists", and one does.
// posixEnvFile renders the minimal tier's ENV file from its template:
// @ENV@ is the session environment block and @NOCX_POSIX@ the nocx.posix
// body (embedded for the argv launchers, a source of the installed
// generation file for the launch carrier).
func posixEnvFile(envBlock, scriptSource string) string {
	env := strings.ReplaceAll(posixEnvFileTemplate, "@ENV@", envBlock)
	env = strings.ReplaceAll(env, "@NOCX_POSIX@", scriptSource)
	// Comment-stripped like the generation scripts: the ENV file ships
	// inside the bootstrap payload, and the far shell never reads the
	// template's prose (nocx-z9s9.17). Stripping the rendered text also
	// covers the substituted bodies.
	return stripShellComments(env)
}

// posixArgFor wraps a rendered ENV file in the minimal tier's pinned
// transport: the POSIX outer script writes it into a transient directory,
// points ENV at it and execs a login shell. The result is one physical line
// with no single quotes, so it can travel inside the launch carrier and the
// argv launchers' shellQuote.
func posixArgFor(envFile string) string {
	outer := strings.ReplaceAll(posixOuterScript, "@POSIXENV@", printfBEscape(envFile))
	// One physical line: a csh login shell splits multi-line quoted
	// tokens, so the payload must survive that parse (see singleLine).
	return singleLine(outer)
}

// The per-tier ARG method that used to sit here went with the command that
// consumed it (ADR-0035). It substituted the two bearers into the rcfile TEXT
// and that text travelled inside the remote COMMAND, so both reached the far
// host's process arguments — the defect this epic exists to remove. What
// survives is posixArgFor, which the installed launch carrier uses and which
// carries no per-session value at all.
