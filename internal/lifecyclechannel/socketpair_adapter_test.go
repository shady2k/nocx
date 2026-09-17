package lifecyclechannel

// newSocketPairAdapter is what New used to be, kept as a TEST constructor.
//
// New existed for one caller: internal/app's local pty factory, which forked
// the shell inside the coordinator and handed it the child end of a socketpair
// as fd 3. nocx-ie23r.3 deleted that factory — this machine's panes are forked
// by the helper daemon now, and the coordinator interprets their lifecycle
// bytes over a STREAM the helper carries (NewStream). So the descriptor
// constructor had no production caller left, and a constructor nothing
// constructs is what the dead-code ratchet is for.
//
// What it does NOT get to keep is the tests. The adapter's own behaviour —
// the handshake bound, the loss causes, the codec's gap reports, the accept
// ordering — is the same whatever carries the bytes, and it is cheapest to
// drive over a socketpair, where the "shell" end is an *os.File a test can
// write to. That is exactly what this builds, out of the two pieces that are
// still production code: NewSocketPair, which the helper's spawner uses, and
// NewStream, which every coordinator uses.

import (
	"os"

	"github.com/shady2k/nocx/internal/log"
)

func newSocketPairAdapter(l log.Logger, k Kernel, opts ...Option) (*Adapter, *os.File, error) {
	parent, child, err := NewSocketPair()
	if err != nil {
		return nil, nil, err
	}
	a, err := NewStream(l, k, parent, opts...)
	if err != nil {
		// NewStream closes the carrier it was handed on failure; the child
		// end is this constructor's own and is closed here.
		_ = child.Close()
		return nil, nil, err
	}
	return a, child, nil
}
