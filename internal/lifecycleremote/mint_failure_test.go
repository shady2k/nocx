package lifecycleremote

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy on this machine") }

// An adapter whose identifiers cannot be minted is not created (nocx-s16k8).
//
// Neither the transport id nor the lane id is an authenticator, and a zero
// value lets nobody in — which is why the read was tolerated (`_, _ =
// io.ReadFull(rand.Reader, b)`). What a zero value does instead is make every
// adapter carry the SAME transport id and the SAME lane id, and the kernel
// tells one transport's domains from another's by exactly that value: the
// binding check is an equality, and ErrWrongTransport is what a mismatch
// produces. Two remote sessions on one machine would share a lane and each
// would authenticate against the other's domains.
//
// New already returns an error, so there is a caller with an answer: no
// adapter, no remote lifecycle channel, a conventional session.
func TestNew_AFailedRandomReadCreatesNoAdapter(t *testing.T) {
	prev := randReader
	randReader = failingReader{}
	defer func() { randReader = prev }()

	tunnel := newFakeTunnel()
	a, _, err := New(log.NewSlogAdapter(nil), newTestKernel(), tunnel)
	if err == nil {
		_ = a.Close()
		t.Fatal("New returned an adapter whose identifiers could not be minted")
	}
	if !strings.Contains(err.Error(), "randomness") {
		t.Errorf("the error does not name the cause: %v", err)
	}
	if a != nil {
		t.Error("a non-nil adapter was returned alongside the error")
	}
	// The listener the constructor opened before the mint is closed again:
	// a refused construction leaves no remote forward behind it. Observed,
	// not timed — a dial to the address either connects or is refused.
	ln, lerr := tunnel.Listen("127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("fixture listener: %v", lerr)
	}
	c, derr := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if derr == nil {
		_ = c.Close()
		t.Error("the listener opened before the refused mint is still accepting; a refused construction leaves no forward behind")
	}
}
