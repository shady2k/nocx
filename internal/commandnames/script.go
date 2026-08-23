package commandnames

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// The two shell programs this package runs, and the framing that makes their
// output trustworthy.
//
// Both are POSIX `sh`, not bash: they run on whatever the far side has, and
// the one bash-specific thing either of them wanted — `compgen -c` — is
// exactly the enumeration being replaced, because it merges the shared half
// with the session-local half and so cannot be cached for anyone.
//
// Both frame their output between a BEGIN and an END line carrying a
// per-invocation nonce, and the parser rejects any output missing either
// marker WHOLE. That is the same rule internal/completion applies to its own
// remote answers, and for the same reason: a login banner, an MOTD or a
// chatty rc file lands on the same stream, and half-parsing a polluted
// answer is how a banner line becomes a command name.

// probeScript is the cheap per-session invalidation probe: who the far side
// thinks we are, what shell family the session belongs to, the effective
// PATH, and one stamp per PATH directory.
//
// The stamp ladder exists because there is no portable stat, and it is
// SELECTED ONCE and then used for every directory — the same shape
// internal/discovery uses for its port probes, and for the same reason: five
// rungs tried per directory would turn the cheap half into 160 process
// spawns on the host where the first rung fails, which is exactly the host
// least able to afford them.
//
// The rungs, in order: GNU coreutils with nanosecond mtime (`stat -c %.9Y`),
// GNU with second mtime, BSD/macOS with fractional mtime (`stat -f %Fm`),
// BSD with second mtime, and finally an `ls -ld` line. Sub-second precision
// leads because a second-granular stamp cannot see two changes inside one
// second — which is not merely a test-fixture problem: an installer that
// writes a directory twice in the same second would leave the cache claiming
// to be current when it is not. Nothing parses a time out of any rung; only
// equality is ever asked, so any token that moves when the directory changes
// is a correct stamp, and the weakest rung (`ls`, minute-granular for recent
// entries) is what the age backstop covers.
//
// A host with none of the three answers `unstamped`, and that word is load
// bearing: two `unstamped` stamps compare equal, so a cache keyed on them
// would look valid forever. The service reads it and refuses to call such an
// entry current — it is served as `stale`, with its age, which is a thing a
// person can see rather than a degrade hiding in a log. `absent` is the
// different and honest case of a PATH entry that does not exist; it changes
// to a real stamp the moment the directory is created.
//
// The directory count is bounded here as well as in the service: 32 stats is
// the number §8 puts on this, and a PATH with 200 entries must not turn the
// cheap half into the expensive one.
const probeScript = `
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

// scanScript is the expensive half: every executable name on the PATH.
//
// This is the work the whole package exists to run once. It is a stat per
// candidate file across up to 32 directories — thousands on an ordinary host
// — which is why it runs under a supervisor that owns its process group, and
// why its result is shared rather than recomputed per tab.
//
// The directory bound matches the probe's exactly. It must: a name found in a
// directory the probe never stamps could never be invalidated, so the two
// halves enumerate the same set of directories or the cache makes a promise
// it cannot keep.
const scanScript = `
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

// unstampedToken is what the probe emits for a directory it could not stamp
// with any rung of the ladder. Two of them compare equal forever, which is
// why the service refuses to call an entry built from them current.
const unstampedToken = "unstamped"

// newNonce mints the per-invocation frame marker.
func newNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("commandnames: nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// errUnframed is what a polluted or truncated answer produces. It is not a
// deadline, so it surfaces as `failed` rather than `timed-out` — the two
// states exist to be told apart.
var errUnframed = errors.New("commandnames: answer was not framed by this invocation's markers")

// framed returns the lines strictly between this invocation's BEGIN and END
// markers, or errUnframed. Both markers are required: without the END, what
// we hold is a prefix, and a prefix of an enumeration is exactly the partial
// answer that may not be published.
func framed(out []byte, nonce string) ([]string, error) {
	begin := "NOCX_CN " + nonce + " BEGIN"
	end := "NOCX_CN " + nonce + " END"
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), MaxScanBytes)
	var lines []string
	inside, closed := false, false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == begin:
			inside, lines = true, nil // a second BEGIN restarts the frame
		case line == end && inside:
			closed = true
		default:
			if inside && !closed {
				lines = append(lines, line)
			}
		}
		if closed {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("commandnames: reading answer: %w", err)
	}
	if !closed {
		return nil, errUnframed
	}
	return lines, nil
}

// parseProbe reads the probe's framed answer.
func parseProbe(out []byte, nonce string) (Probe, error) {
	lines, err := framed(out, nonce)
	if err != nil {
		return Probe{}, err
	}
	var p Probe
	var pendingDir string
	var haveDir bool
	p.Stamped = true
	for _, line := range lines {
		tag, value, ok := strings.Cut(line, " ")
		if !ok {
			// A tag with no value is legal for none of the fields; skip it
			// rather than inventing an empty user or an empty PATH.
			continue
		}
		switch tag {
		case "V":
			if value != "1" {
				return Probe{}, fmt.Errorf("commandnames: probe protocol %q is not 1", value)
			}
		case "U":
			p.User = value
		case "F":
			p.ShellFamily = value
		case "P":
			p.Path = value
		case "D":
			pendingDir, haveDir = value, true
		case "S":
			if haveDir {
				if value == unstampedToken {
					p.Stamped = false
				}
				p.Stamps = append(p.Stamps, DirStamp{Dir: pendingDir, Stamp: value})
				haveDir = false
			}
		}
	}
	if p.Path == "" {
		return Probe{}, errors.New("commandnames: probe reported no PATH")
	}
	if len(p.Stamps) > MaxPathDirs {
		p.Stamps = p.Stamps[:MaxPathDirs]
	}
	return p, nil
}

// parseScan reads the scan's framed answer. Only `N `-tagged lines are
// names: an rc file that printed something inside the frame contributes
// nothing rather than contributing a command that does not exist.
func parseScan(out []byte, nonce string) (Scan, error) {
	lines, err := framed(out, nonce)
	if err != nil {
		return Scan{}, err
	}
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		if tag, value, ok := strings.Cut(line, " "); ok && tag == "N" && value != "" {
			names = append(names, value)
		}
	}
	if len(names) == 0 {
		// An empty enumeration is never published. "Every command is
		// unknown" is the same lie as "every command exists", pointing the
		// other way — the shell tier already refuses to emit one, and this
		// is the same rule on the shared half.
		return Scan{}, errors.New("commandnames: the scan found no executables on PATH")
	}
	return Scan{Names: names}, nil
}
