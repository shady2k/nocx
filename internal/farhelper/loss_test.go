package farhelper

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/hostconn"
)

// AssertCoalescedClientLoss uses clients from the public hello fixture. The
// delayed old watch overwrites the current dead client's loss before drain.
func AssertCoalescedClientLoss(t *testing.T, current, old *client.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := New(Config{})
	defer r.Close()
	e := &entry{r: r, decisionState: decisionState{phase: phaseServing, client: current, conn: Connection{State: hostconn.Connected}}, ctx: ctx, ready: make(chan struct{}), wake: make(chan struct{}, 1)}
	e.clientLost(current)
	e.clientLost(old)
	if e.drainInbox() {
		t.Fatal("unexpected close")
	}
	if e.phase != phaseBroken || e.client != nil {
		t.Fatal("delayed old loss hid the current dead peer")
	}
}
