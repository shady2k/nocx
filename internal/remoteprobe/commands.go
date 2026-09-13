package remoteprobe

import (
	_ "embed"
	"fmt"
)

// # The probes, one family at a time
//
// Each family below is one question nocx asks a remote host. The command that
// answers it is here, fixed, and carries no caller-supplied shell text — what a
// caller supplies arrives as an ARGUMENT to a fixed script (the completion
// probe) or is not supplied at all.

// Sentinel is the fixed framing marker the port probes print first and last. A
// sample without it is rejected WHOLE: a forced command, a login banner or a
// policy wrapper can prepend text, and we never scan arbitrary stdout for
// plausible-looking port numbers. Versioned — bump it when the probe protocol
// changes, so an old parser rejects new output instead of misreading it.
const Sentinel = "NOCX-PD/1"

// UnameCommand answers the platform probe (D20): the kernel and machine in one
// line, which the caller maps onto Go's GOOS/GOARCH vocabulary.
const UnameCommand = "uname -s -m"

// HomeCommand answers "where is this account's home". It is asked over a shell
// because there is no portable other way, and it is a fixed command because the
// question is fixed — the caller trims the answer and refuses an empty one.
const HomeCommand = "echo $HOME"

// HomeFallbackCommand is the second half of the same question, and it exists for
// the host whose shell answers nothing: $HOME unset is not "no home directory",
// and `~` still is one.
//
// It is a SEPARATE command rather than one compound script on purpose. The far
// side's login shell is somebody else's (a tcsh account is a real case here),
// and `h=$HOME; [ -n "$h" ] || ...` would be a syntax error there while `echo
// $HOME` and `cd ~ && pwd` are not.
const HomeFallbackCommand = "cd ~ && pwd"

// HomeCommands is the home probe: the commands to run IN ORDER, stopping at the
// first answer that is not empty.
//
// It is a list here so that the order and the membership have one owner: the
// helper, the script-mode carrier path and the typed mux path all ask the same
// question, and three copies of "try this, then that" is the drift this package
// exists to prevent. An implementation trims each answer; an empty result from
// the last command means the question could not be answered.
var HomeCommands = []string{HomeCommand, HomeFallbackCommand}

// PortProbe names one rung of the port-discovery ladder. The spellings are
// internal/discovery's own, because the name is what a person sees reported as
// "which dialect produced this sample".
type PortProbe string

const (
	// PortSS is `ss -H -lntp`, the modern default.
	PortSS PortProbe = "ss"
	// PortNetstat is `netstat -lntp`, whose flags are verified by the run
	// itself rather than hoped for.
	PortNetstat PortProbe = "netstat"
	// PortBusyboxNetstat is `netstat -ltn`: busybox's, without -p, so process
	// evidence is unsupported rather than absent.
	PortBusyboxNetstat PortProbe = "busybox-netstat"
	// PortLsof is `lsof -nP -iTCP -sTCP:LISTEN`, which exits 1 when nothing
	// matches — a valid empty sample rather than a failure.
	PortLsof PortProbe = "lsof"
	// PortSockstat is the BSD fallback.
	PortSockstat PortProbe = "sockstat"
)

// portCommands is the ladder, in selection order, each with the command that
// runs it. It is a TABLE and not a switch so that "which probes exist" has one
// spelling that both the helper's params validation and the caller's ladder
// read.
//
// It is PRIVATE, and that is the invariant rather than tidiness: an exported
// slice is a closed set any caller could edit at runtime, and this one carries
// the members the helper's `sample-ports` op validates against. The exported
// surface is the lookup below, which cannot be rewritten from outside.
var portCommands = []struct {
	Probe   PortProbe
	Command string
}{
	{PortSS, sentinelPrintf(`LC_ALL=C ss -H -lntp`)},
	{PortNetstat, sentinelPrintf(`netstat -lntp`)},
	{PortBusyboxNetstat, sentinelPrintf(`netstat -ltn`)},
	{PortLsof, sentinelPrintf(`lsof -nP -iTCP -sTCP:LISTEN`)},
	{PortSockstat, sentinelPrintf(`sockstat -4 -l`)},
}

// PortCommand answers the fixed command for one port probe.
func PortCommand(p PortProbe) (string, bool) {
	for _, e := range portCommands {
		if e.Probe == p {
			return e.Command, true
		}
	}
	return "", false
}

// CommandNamesPhase names which half of the PATH enumeration to run. They are
// two commands and not one with a flag: the cheap half runs once per session to
// invalidate the cache, the expensive half runs once per target and is shared,
// and a single command with a mode would have to be bounded by the slower
// half's deadline.
type CommandNamesPhase string

const (
	// CommandNamesProbe is the cheap invalidation probe.
	CommandNamesProbe CommandNamesPhase = "probe"
	// CommandNamesScan is the full enumeration of executable names on PATH.
	CommandNamesScan CommandNamesPhase = "scan"
)

// UnstampedToken is what the invalidation probe emits for a PATH directory it
// could not stamp with any rung of its ladder. It is exported because the
// PARSER must recognise the same word: two of them compare equal forever, which
// is why an entry built from them may never be called current.
const UnstampedToken = "unstamped"

// parseCommandNamesScript is the cheap per-session invalidation probe: who the
// far side thinks we are, what shell family the session belongs to, the
// effective PATH, and one stamp per PATH directory.
//
// The stamp ladder exists because there is no portable stat, and it is SELECTED
// ONCE and then used for every directory — the same shape the port ladder uses,
// and for the same reason: five rungs tried per directory would turn the cheap
// half into 160 process spawns on the host where the first rung fails, which is
// exactly the host least able to afford them.
//
// The rungs, in order: GNU coreutils with nanosecond mtime, GNU with second
// mtime, BSD/macOS with fractional mtime, BSD with second mtime, and finally an
// `ls -ld` line. Sub-second precision leads because a second-granular stamp
// cannot see two changes inside one second. Nothing parses a time out of any
// rung; only equality is ever asked.
//
// The directory count is bounded here as well as in the caller: a PATH with 200
// entries must not turn the cheap half into the expensive one.
const parseCommandNamesScript = `
nonce=$1
printf 'NOCX_CN %s BEGIN\n' "$nonce"
printf 'V 1\n'
printf 'U %s\n' "$(id -un 2>/dev/null || printf '%s' "${USER:-unknown}")"
printf 'F %s\n' "${SHELL##*/}"
printf 'P %s\n' "$PATH"
IFS=:
set -f
set -- $PATH
IFS=' '
set +f
st=""
for c in "stat -c %.9Y" "stat -c %Y" "stat -f %Fm" "stat -f %m" "ls -ld"; do
  if $c / >/dev/null 2>&1; then st="$c"; break; fi
done
n=0
for d in "$@"; do
  [ -n "$d" ] || d=.
  n=$((n+1))
  [ "$n" -le 32 ] || break
  if [ ! -e "$d" ]; then
    s=absent
  elif [ -z "$st" ]; then
    s=unstamped
  else
    s=$($st "$d" 2>/dev/null) || s=unstamped
    [ -n "$s" ] || s=unstamped
  fi
  printf 'D %s\n' "$d"
  printf 'S %s\n' "$s"
done
printf 'NOCX_CN %s END\n' "$nonce"
`

// scanCommandNamesScript is the expensive half: every executable name on the
// PATH.
//
// This is the work the whole enumeration exists to run once: a stat per
// candidate file across up to 32 directories. The directory bound matches the
// probe's exactly — a name found in a directory the probe never stamps could
// never be invalidated, so the two halves enumerate the same set of directories
// or the cache makes a promise it cannot keep.
const scanCommandNamesScript = `
nonce=$1
printf 'NOCX_CN %s BEGIN\n' "$nonce"
IFS=:
set -f
set -- $PATH
IFS=' '
set +f
n=0
for d in "$@"; do
  [ -n "$d" ] || d=.
  n=$((n+1))
  [ "$n" -le 32 ] || break
  [ -d "$d" ] || continue
  for f in "$d"/*; do
    [ -x "$f" ] || continue
    [ -d "$f" ] && continue
    printf 'N %s\n' "${f##*/}"
  done
done
printf 'NOCX_CN %s END\n' "$nonce"
`

// CommandNamesBegin and CommandNamesEnd spell the markers the two enumeration
// scripts frame their output with. They are here, beside the scripts, so the
// parser that looks for a marker and the script that prints it cannot disagree;
// the nonce makes the frame unique per invocation.
func CommandNamesBegin(nonce string) string { return "NOCX_CN " + nonce + " BEGIN" }
func CommandNamesEnd(nonce string) string   { return "NOCX_CN " + nonce + " END" }

// CommandNamesCommand composes one enumeration command. The nonce is the frame
// marker AND the delimiter's tail, so a delimiter cannot collide with the
// script's own text.
func CommandNamesCommand(phase CommandNamesPhase, nonce string) (string, bool) {
	var script string
	switch phase {
	case CommandNamesProbe:
		script = parseCommandNamesScript
	case CommandNamesScan:
		script = scanCommandNamesScript
	default:
		return "", false
	}
	// POSIX `sh`, never bash: it runs on whatever the far side has, and the one
	// bash-specific thing this package's own shell tier wanted — `compgen -c` —
	// is exactly the enumeration being replaced.
	return heredoc("sh -s "+nonce, "NOCXCN_"+nonce, script), true
}

//go:embed scripts/nocx_complete.bash
var completionScript string

// CompletionCommand composes the completion probe's command.
//
// cwd and line are the user's, and they are the reason this command is composed
// from a fixed script rather than trusted: both arrive as QUOTED ARGUMENTS to
// the script, which is a data question, and neither can become shell syntax.
// The heredoc delimiter carries the nonce for the same reason the enumeration's
// does — no temp file on the far side, no printf escaping, and a body delivered
// verbatim.
func CompletionCommand(cwd, line string, pos, limit int, nonce string) string {
	return heredoc(
		fmt.Sprintf("bash -s -- %s %s %d %d %s",
			ShellQuote(cwd), ShellQuote(line), pos, limit, ShellQuote(nonce)),
		"NOCXEOF_"+nonce,
		completionScript,
	)
}

// CompletionStart and CompletionEnd spell the markers the completion script
// frames its answer with, for the reason the enumeration's do.
func CompletionStart(nonce string) string { return "NONCE:" + nonce + ":START" }
func CompletionEnd(nonce string) string   { return "NONCE:" + nonce + ":END" }
