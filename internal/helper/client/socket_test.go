package client

// The socket carrier: the local half of D11. There is no "local mode" here to
// test — the carrier is one of two implementations of the one interface Dial
// takes — so what is tested is the three things a socket answers differently
// from a process, and the constant it shares with the endpoint.

import (
	"errors"
	"net"
	"testing"

	"github.com/shady2k/nocx/internal/helper/endpoint"
)

func TestTheBridgesExitCodeIsTheOneTheEndpointDefines(t *testing.T) {
	// The value is restated here rather than imported into the classification
	// path, for the reason the version-mismatch code beside it is: the backend
	// does not link the helper's serving half. Restating it is only safe while
	// something asserts the two agree — this.
	if exitNoEndpoint != endpoint.ExitNoEndpoint {
		t.Fatalf("the client reads exit %d as 'no helper serving', the bridge exits %d",
			exitNoEndpoint, endpoint.ExitNoEndpoint)
	}
	if exitVersionMismatch == exitNoEndpoint {
		t.Fatal("two different refusals share one exit code: the client cannot tell them apart")
	}
}

func TestASocketCarrierRefusesACommandToLaunch(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	c := NewSocketConn(a)

	// The two carriers differ in exactly this: the exec lane LAUNCHES the
	// helper and the command names which binary, while the socket is already
	// being served and the generation was decided by which socket was dialled.
	// A silent no-op would run the wrong generation's sessions and look like
	// it worked.
	if err := c.Start("/path/to/nocx-helper"); !errors.Is(err, ErrNoCommandOnASocket) {
		t.Fatalf("Start with a command = %v, want ErrNoCommandOnASocket", err)
	}
	if err := c.Start(""); err != nil {
		t.Fatalf("Start with nothing to launch = %v, want nil", err)
	}
}

func TestASocketCarrierCallsItLossOnlyWhenTheLossIsNotOurs(t *testing.T) {
	// Done is the client's transport-loss watcher, and it must fire on the
	// peer going away and NOT on this side hanging up: a coordinator closing
	// its own connection is one attachment ending (D2), and reporting it as
	// loss would make an ordinary detach look like a dead helper.
	a, b := net.Pipe()
	ours := NewSocketConn(a)
	if err := ours.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-ours.Done():
		t.Fatal("closing our own connection was reported as transport loss")
	default:
	}
	// Wait releases either way: nothing above it may block forever waiting for
	// an exit status a socket does not have.
	if code, err := ours.Wait(); code != 0 || err != nil {
		t.Fatalf("Wait after our own close = (%d, %v), want (0, nil)", code, err)
	}
	_ = b.Close()

	c, d := net.Pipe()
	theirs := NewSocketConn(c)
	_ = d.Close()
	// The peer is gone: the first read reports it, which is where the loss
	// becomes a fact rather than a guess.
	if _, err := theirs.Stdout().Read(make([]byte, 1)); err == nil {
		t.Fatal("reading from a closed peer succeeded")
	}
	<-theirs.Done()
	if theirs.LostErr() == nil {
		t.Fatal("the loss carries no reason")
	}
}
