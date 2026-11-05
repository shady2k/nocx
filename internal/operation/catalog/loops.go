package catalog

import (
	"fmt"
	"strings"
	"time"
)

// Loop is a lifetime loop: work that runs as long as its owner, which a total
// deadline would kill while it is healthy. ADR-8eej3e66 section 2 says such a
// loop is NOT an operation. It is registered here with the owner it dies with
// and the finite stop a shutdown may wait for; each iteration is an operation
// from the table (Declarations), with its own lifetime.
type Loop struct {
	ID        ID       `json:"id"`
	Owner     Owner    `json:"owner"`
	Iteration ID       `json:"iteration"`
	Stop      Deadline `json:"stop"`
}

var loopTable = []Loop{
	{
		ID: "loop.session.readPump", Owner: OwnerSession, Iteration: "internal.session.recorder",
		Stop: d(10000, BasisRead, "session teardown joins the read pump within the transport's control drain bound (session close path)"),
	},
	{
		ID: "loop.files.watchPoll", Owner: OwnerBinding, Iteration: "internal.files.watchPoll",
		Stop: d(5000, BasisRead, "files.close stops the poll loop before it releases the binding (internal/transport/ws_files.go)"),
	},
	{
		ID: "loop.pane.observerSweep", Owner: OwnerServer, Iteration: "internal.pane.observer",
		Stop: d(10000, BasisRead, "server Stop cancels the observer sweep and waits within the control drain bound"),
	},
	{
		ID: "loop.signal.ticker", Owner: OwnerSession, Iteration: "internal.session.endHold",
		Stop: d(5000, BasisRead, "the signal ticker exits with its held-stop owner"),
	},
	{
		ID: "loop.typedLine.ownershipPoll", Owner: OwnerSession, Iteration: "internal.typedLine.ownership",
		Stop: d(5000, BasisRead, "the typed-line poll exits with its session"),
	},
	{
		ID: "loop.worker.deliveryPoll", Owner: OwnerSession, Iteration: "internal.agent.run",
		Stop: d(10000, BasisRead, "worker delivery stops with the app's workers"),
	},
	{
		ID: "loop.worker.checkoutSweep", Owner: OwnerServer, Iteration: "internal.worker.checkoutSweep",
		Stop: d(10000, BasisRead, "the sweep stops with the app's workers"),
	},
	{
		ID: "loop.ssh.keepalive", Owner: OwnerServer, Iteration: "internal.ssh.keepalive",
		Stop: d(10000, BasisRead, "keepalive exits when its connection closes"),
	},
	{
		ID: "loop.vault.autoSeal", Owner: OwnerServer, Iteration: "internal.vault.autoSeal",
		Stop: d(10000, BasisRead, "auto-seal stops with the app"),
	},
}

// Loops returns the registered loops.
func Loops() []Loop {
	out := make([]Loop, len(loopTable))
	copy(out, loopTable)
	return out
}

// ValidateLoops checks a loop table against the operation table: every loop
// names an internal operation, and its owner and stop are finite and explicit.
func ValidateLoops(loops []Loop, decls []Declaration) []error {
	var errs []error
	byID := map[ID]Declaration{}
	for _, d := range decls {
		byID[d.ID] = d
	}
	for _, l := range loops {
		if strings.TrimSpace(string(l.ID)) == "" {
			errs = append(errs, fmt.Errorf("loop with empty id"))
			continue
		}
		d, ok := byID[l.Iteration]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: iteration %q has no declaration", l.ID, l.Iteration))
		} else if d.Kind != KindInternal {
			errs = append(errs, fmt.Errorf("%s: iteration %q is not an internal operation", l.ID, l.Iteration))
		} else if d.Owner != l.Owner {
			errs = append(errs, fmt.Errorf("%s: owner %q disagrees with iteration owner %q", l.ID, l.Owner, d.Owner))
		}
		if l.Stop.Millis <= 0 || l.Stop.Basis == "" || strings.TrimSpace(l.Stop.Note) == "" {
			errs = append(errs, fmt.Errorf("%s: stop is not a finite, sourced deadline", l.ID))
		}
	}
	return errs
}

func init() {
	if errs := ValidateLoops(loopTable, declarationTable); len(errs) != 0 {
		panic(fmt.Sprintf("operation/catalog: %d invalid loop(s): %v", len(errs), errs[0]))
	}
}

var _ = time.Millisecond
