package panedriver_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestSameConnectionRebindClearsSessionAndRefusesOlderAdoption(t *testing.T) {
	d, out := newDriver(t)
	apply(d, connected(1, 1))
	old := d.Bind("pane", "conn-a")
	d.AdoptSession("pane", "conn-a", "old", "instance", old)
	current := d.Bind("pane", "conn-a")
	if out.last().SessionID != "" {
		t.Fatal("rebind kept old session")
	}
	d.AdoptSession("pane", "conn-a", "new", "instance", current)
	d.AdoptSession("pane", "conn-a", "late", "instance", old)
	if out.last().SessionID != "new" {
		t.Fatal("older open overwrote current session")
	}
	d.Unbind("pane")
	reopened := d.Bind("pane", "conn-a")
	d.AdoptSession("pane", "conn-a", "late-after-close", "instance", old)
	if out.last().SessionID != "" || reopened == old {
		t.Fatal("closed lifetime's adoption was accepted")
	}
}

func TestReturnReplacesEachHeldShellOnceAndCloseCancelsAction(t *testing.T) {
	d, _ := newDriver(t)
	contexts := make(chan context.Context, 4)
	d.SetAction(func(ctx context.Context, _ string, _ uint64) error { contexts <- ctx; return nil })
	apply(d, connected(1, 2))
	for _, pane := range []string{"one", "two"} {
		g := d.Bind(pane, "conn-a")
		d.AdoptSession(pane, "conn-a", "sess-"+pane, "instance", g)
	}
	apply(d, reconnecting(2, 1, "lost-link"))
	apply(d, opening(3, "tcp"))
	apply(d, connected(4, 0))
	waittest.WaitFor(t, "both replacement actions", func() bool { return len(contexts) == 2 })
	a, b := <-contexts, <-contexts
	// The replacement's own bind must not cancel the context driving its open.
	d.Bind("one", "conn-a")
	if a.Err() != nil || b.Err() != nil {
		t.Fatal("replacement bind canceled its own open")
	}
	apply(d, connected(5, 2))
	d.Unbind("one")
	d.Unbind("two")
	if a.Err() == nil || b.Err() == nil {
		t.Fatal("close did not cancel replacement lifetimes")
	}
	if len(contexts) != 0 {
		t.Fatal("repeated connected observation replaced shells twice")
	}
}

func TestAnsweredStallAndClosedPaneDoNotReplaceShells(t *testing.T) {
	d, _ := newDriver(t)
	d.SetAction(func(context.Context, string, uint64) error { t.Error("unexpected shell replacement"); return nil })
	apply(d, connected(1, 1))
	g := d.Bind("pane", "conn-a")
	d.AdoptSession("pane", "conn-a", "sess", "instance", g)
	apply(d, stalled(2, 1))
	apply(d, connected(3, 1))
	apply(d, reconnecting(4, 1, "lost-link"))
	d.Unbind("pane")
	apply(d, connected(5, 0))
}

func TestCloseWhileReturnPublishesSuppressesReplacement(t *testing.T) {
	d, _ := newDriver(t)
	d.SetAction(func(context.Context, string, uint64) error { t.Error("closed pane got replacement"); return nil })
	apply(d, connected(1, 1))
	g := d.Bind("pane", "conn-a")
	d.AdoptSession("pane", "conn-a", "sess", "instance", g)
	apply(d, reconnecting(2, 1, "lost-link"))
	d.SetPublisher(func(s panedriver.Snapshot) {
		if s.Transport.State == proto.ConnectionConnected {
			d.Unbind("pane")
		}
	})
	apply(d, connected(3, 0))
}
