package panedriver

// THE HELPER'S LINK GENERATION IS NOT THE PANE'S, AND THE DRIVER IGNORES IT
// (nocx-oalo1.6.2).
//
// `proto.ConnectionStateEvent` gained `generation` — which of the helper
// supervisor's LINKS the observation is about — and this package reads the
// observation field by field. The new number is not one of the facts a pane is
// drawn from: a link replaced under a stall is still the same connection, and
// the pane keeps what it is drawing while the helper redials. The two
// `Generation` fields are different things and stay that way: Snapshot's counts
// the pane's own bindings, this one counts the connection's transports.

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
)

func TestALinkGenerationOnTheObservationChangesNothingForThePane(t *testing.T) {
	_, logger := logtest.New(t)

	// THE PAIR A COORDINATOR CANNOT TELL APART WITHOUT THE NUMBER, delivered in
	// the order a slow reader sees it: `reconnecting` was overtaken by
	// `connected`, so only the second observation arrives — the same bytes
	// whether the transport survived or was replaced.
	without := New("coordinator", logger)
	with := New("coordinator", logger)
	for _, d := range []*Driver{without, with} {
		d.Bind("pane", "connection")
		d.ApplyFrom("helper", proto.ConnectionStateEvent{
			ConnectionID: "connection", Seq: 1, State: proto.ConnectionConnected, Generation: 1,
		})
	}
	with.ApplyFrom("helper", proto.ConnectionStateEvent{
		ConnectionID: "connection", Seq: 2, State: proto.ConnectionConnected, Generation: 2,
	})

	// Same state, same quality, same offer: the pane is told the same thing,
	// and only the fact that is not the pane's to draw is different.
	a := without.conns["connection"]
	b := with.conns["connection"]
	if a.transport != b.transport {
		t.Fatalf("a link generation changed the pane's transport: %+v against %+v", a.transport, b.transport)
	}
	if (a.quality == nil) != (b.quality == nil) {
		t.Fatalf("a link generation changed the pane's measurement: %v against %v", a.quality, b.quality)
	}
	if without.panes["pane"].lost != with.panes["pane"].lost ||
		without.panes["pane"].replacing != with.panes["pane"].replacing {
		t.Fatalf("a link generation changed the pane's replacement state: lost %v/%v, replacing %v/%v",
			without.panes["pane"].lost, with.panes["pane"].lost,
			without.panes["pane"].replacing, with.panes["pane"].replacing)
	}
}
