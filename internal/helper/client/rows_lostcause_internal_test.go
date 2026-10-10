package client

// The receiving end of the lost-rows cause (nocx-zg3k3.5.10): the resend
// names the gap it could not fill — coordinator-unavailable says the rows
// left the screen while nobody was attached and ghostty pruned them before
// the resend could read them back (nocx-zg3k3.5.3). The store counts that
// gap separately from the emulator's own losses, and it can only if the
// decoded OutputRows still carry the cause: a decode that drops it delivers
// an unclassified loss, and unavailableRows stays 0 however the helper
// spelled it. Paired with the ordinary batch, whose every field is present
// and whose cause is empty because there is nothing to name.

import (
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestALostCauseRidesTheDecodedRows(t *testing.T) {
	session, subscriber := [16]byte{1}, [16]byte{2}
	c := &Client{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		attachments: make(map[[16]byte]*AttachedSession),
	}
	a := &AttachedSession{client: c, session: session, subscriber: subscriber}
	c.attachments[subscriber] = a
	var got []OutputRows
	a.OnOutputRows(func(o OutputRows) { got = append(got, o) })

	// The resend's gap as the helper sends it: two rows pruned while the
	// coordinator was away, stated at the position the survivors start at.
	c.outputRows(rowsFrameFor(t, session, subscriber,
		`{"fromRow":4,"lostRows":2,"lostCause":"coordinator-unavailable","rows":[{"text":"two"}]}`))
	if len(got) != 1 {
		t.Fatalf("the gap was not delivered: %+v", got)
	}
	g := got[0]
	if g.FromRow != 4 || g.LostRows != 2 {
		t.Fatalf("the gap arrived as (%d, %d), want (4, 2)", g.FromRow, g.LostRows)
	}
	if g.LostCause != proto.LostCauseCoordinatorUnavailable {
		t.Fatalf("LostCause = %q, want %q — a cause the decode drops is a loss the store cannot classify",
			g.LostCause, proto.LostCauseCoordinatorUnavailable)
	}

	// The paired ordinary batch: no gap, no cause.
	c.outputRows(rowsFrameFor(t, session, subscriber,
		`{"fromRow":5,"lostRows":0,"rows":[{"text":"three"}],"incomplete":false}`))
	if len(got) != 2 {
		t.Fatalf("the ordinary batch was not delivered: %+v", got)
	}
	if o := got[1]; o.LostRows != 0 || o.LostCause != "" || o.Incomplete {
		t.Fatalf("the ordinary batch arrived as %+v, want no loss, no cause, no marker", o)
	}
}
