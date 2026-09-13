//go:build nocx_local_ssh

package sshsvc

// THE PANE ENDS THE FORWARDS IT IS CARRYING (nocx-50w7p.16).
//
// This is a test of the MECHANISM, and it is here rather than only at the far
// end of the ssh fixture because the fixture cannot see the difference: its own
// `cancel-streamlocal-forward` closes the connections already accepted on the
// listener, so an end-to-end test passes whether or not the pane ends them
// itself. Measured, not assumed — removing the cancellation below leaves the
// end-to-end test green and this one red.
//
// What the fixture does not model is the case that matters: a real sshd's
// cancellation removes the LISTENER, and the channels already open on it stay
// open until the connection or the channel ends. So a pane whose session is over
// must end its own forwards, or a far agent keeps a pipe into a coordinator that
// has forgotten the pane.

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"
)

func testPaneListeners() *PaneListeners {
	return &PaneListeners{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		session: "0f9b4d7159d38afee9648a843654516f",
	}
}

// TestClosingAPaneEndsTheForwardsItIsCarrying — the paired assertion: the
// connection is live before the pane closes and ended by it, with nobody on the
// far side closing anything.
func TestClosingAPaneEndsTheForwardsItIsCarrying(t *testing.T) {
	pane := testPaneListeners()
	far, local := net.Pipe()
	defer func() { _ = local.Close() }()

	if !pane.trackForward(far) {
		t.Fatal("an open pane refused to carry a connection")
	}

	// PAIRED SUCCESS FIRST: the connection works before the pane ends.
	go func() { _, _ = local.Write([]byte("still here\n")) }()
	buf := make([]byte, len("still here\n"))
	if err := far.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := io.ReadFull(far, buf); err != nil {
		t.Fatalf("the connection was not carried before the pane closed: %v", err)
	}

	// The deadline is set BEFORE the close and not after it: a closed
	// connection refuses SetReadDeadline, and the point of a deadline here is
	// to bound what follows — a read that was NOT ended by the close would
	// otherwise park the test until the package's own alarm.
	if err := far.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := pane.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	switch _, err := far.Read(buf); {
	case err == nil:
		t.Fatal("a forward outlived the pane that was carrying it")
	case errors.Is(err, os.ErrDeadlineExceeded):
		t.Fatal("the connection was still open after its pane closed: the read waited out its deadline instead of ending")
	}
}

// TestAPaneThatHasClosedRefusesFurtherForwards — the OTHER half of the same
// decision, and the one a listener alone cannot make: between the Accept that
// returned and the registration that follows it, the pane can close, and a
// connection registered after that is one nobody will ever end. It is refused
// instead, so the accept loop closes it.
func TestAPaneThatHasClosedRefusesFurtherForwards(t *testing.T) {
	pane := testPaneListeners()
	if err := pane.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	far, local := net.Pipe()
	defer func() { _ = far.Close() }()
	defer func() { _ = local.Close() }()

	if pane.trackForward(far) {
		t.Fatal("a closed pane accepted a connection it will never end")
	}
}
