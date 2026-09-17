package commandnames

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/remoteprobe"
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

// The two shell programs this package runs live in internal/remoteprobe, and
// so does the framing they print: the helper composes and runs them now (D3 —
// no command crosses the helper wire), this package reads their answers, and
// the marker a parser looks for and the marker a script prints must be one
// declaration or they are the pair that drifts. What stays here is what the
// ANSWERS mean — the tag grammar, the stamp ladder's vocabulary and the rule
// that an unframed answer is no answer.

// unstampedToken is what the probe emits for a directory it could not stamp
// with any rung of the ladder — remoteprobe's own spelling, because the SCRIPT
// that emits it is there. Two of them compare equal forever, which is why the
// service refuses to call an entry built from them current.
const unstampedToken = remoteprobe.UnstampedToken

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
	begin := remoteprobe.CommandNamesBegin(nonce)
	end := remoteprobe.CommandNamesEnd(nonce)
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
