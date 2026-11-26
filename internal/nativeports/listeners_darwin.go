//go:build darwin

package nativeports

// macOS: lsof — a documented fallback, not a kernel read. The native route
// is libproc (proc_listpids/proc_pidfdinfo), which needs cgo; the decision
// and its evidence are in .internal/reports/nocx-wzc4.8.md. lsof ships with
// every macOS base system (/usr/sbin/lsof) and its -nP -iTCP -sTCP:LISTEN
// dialect is stable across releases; it is parsed by internal/portsprobe's
// ParseLsof — the remote ladder's own lsof rung, one implementation — so the
// wire renders identically. When lsof is absent the read reports ErrToolMissing —
// a terminal degrade to the unavailable state, never a convincing empty
// list.
//
// The exec is bounded: a fixed absolute path (never shelled), a 5s timeout,
// output capped at 1 MiB.
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/portsprobe"
)

const probeName = "darwin-lsof"

const (
	lsofPath      = "/usr/sbin/lsof"
	lsofOutputCap = 1 << 20 // 1 MiB
	lsofTimeout   = 5 * time.Second
)

func listeners(ctx context.Context) ([]portsprobe.Listener, error) {
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()

	proc := exec.CommandContext(ctx, lsofPath, "-nP", "-iTCP", "-sTCP:LISTEN")
	stdout, err := proc.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// stderr is captured because exit status alone cannot tell "nothing is
	// listening" from "I could not look". lsof exits 1 for both, and writes
	// its reason — a refused /dev/kmem, an unreadable process, a warning it
	// then gives up on — only to stderr. Discarding it made those identical,
	// and the module's contract is explicit that they must never be: an empty
	// result means no listeners were observed, and everything else means
	// could-not-determine, never "no ports" (portsprobe.State).
	var stderr bytes.Buffer
	proc.Stderr = &stderr
	if err := proc.Start(); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrToolMissing
		}
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, lsofOutputCap+1))
	truncated := len(data) > lsofOutputCap
	if truncated {
		data = data[:lsofOutputCap]
	}
	waitErr := proc.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) && ee.ExitCode() == 1 {
			// Exit 1 with a silent stderr is the honest empty table: lsof
			// looked and matched nothing, exactly like the remote ladder's
			// lsof rung.
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("lsof could not read the listener table: %s", firstLines(msg, 3))
			}
			return []portsprobe.Listener{}, nil
		}
		return nil, waitErr
	}
	if truncated {
		// A table past the cap is unreadable, not "all of it": the sample
		// degrades like a remote table cut short.
		return nil, errors.New("lsof output exceeds the 1 MiB capture cap")
	}
	ls, ok := portsprobe.ParseLsof(data)
	if !ok {
		// The ladder's rung answers `false` with "advance"; here lsof is the
		// only tool, so the honest answer is could-not-determine. Never an
		// empty list — the module's contract is explicit that a table it
		// failed to read must not render as "this machine has no ports".
		return nil, errors.New("lsof output was not the expected -nP -iTCP -sTCP:LISTEN dialect")
	}
	return ls, nil
}

// firstLines keeps an error message short enough to log and to read in a CI
// failure, without dropping the part that names the cause: lsof puts its
// reason on the first line or two and can then repeat itself per process.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return strings.Join(lines, "; ")
	}
	return strings.Join(lines[:n], "; ") + fmt.Sprintf(" (+%d more lines)", len(lines)-n)
}
