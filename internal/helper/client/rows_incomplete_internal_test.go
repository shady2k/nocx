package client

// The receiving end of the incomplete marker (nocx-2v80t.3.38): the contract
// says an incomplete document carries no rows, and the receiver holds it to
// that — a document claiming both that recording stopped and a batch of rows
// is refused, not half-believed. Paired with the marker as the helper sends
// it, which is delivered.

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func rowsFrameFor(t *testing.T, session, subscriber [16]byte, doc string) []byte {
	t.Helper()
	raw, err := proto.EncodeOutputRowsFrame(proto.OutputRowsFrame{
		Session: session, Subscriber: subscriber, FromRow: 3, Payload: json.RawMessage(doc),
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}

func TestAnIncompleteMarkerCarryingRowsIsRefused(t *testing.T) {
	session, subscriber := [16]byte{1}, [16]byte{2}
	c := &Client{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		attachments: make(map[[16]byte]*AttachedSession),
	}
	a := &AttachedSession{client: c, session: session, subscriber: subscriber}
	c.attachments[subscriber] = a
	var got []OutputRows
	a.OnOutputRows(func(o OutputRows) { got = append(got, o) })

	c.outputRows(rowsFrameFor(t, session, subscriber,
		`{"fromRow":3,"lostRows":0,"rows":[{"text":"x"}],"incomplete":true}`))
	if len(got) != 0 {
		t.Fatalf("an incomplete marker carrying rows was delivered: %+v", got)
	}

	c.outputRows(rowsFrameFor(t, session, subscriber,
		`{"fromRow":3,"lostRows":0,"rows":[],"incomplete":true}`))
	if len(got) != 1 || !got[0].Incomplete || got[0].FromRow != 3 {
		t.Fatalf("the marker as the helper sends it arrived as %+v, want one incomplete marker at 3", got)
	}
}
