package client

import (
	"context"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// SetScrollback sets one running session's scrollback budget: how far the
// session's live terminal scrolls back, in physical lines
// (proto.OpSetScrollback). Zero keeps no history at all and erases what the
// session has retained.
//
// A helper of a generation that does not know the op refuses with the
// wire's unknown-op error; a caller applying the person's setting across
// every live session reads that as "this machine's helper is older than
// this app" and leaves the pane at the budget it was spawned with — it is
// the caller's tolerance, not this method's, because only the caller knows
// what a pane losing the live update means for it.
func (c *Client) SetScrollback(ctx context.Context, id HostSessionID, maxLines uint64) error {
	return c.Call(ctx, proto.ServiceSession, proto.OpSetScrollback, proto.SetScrollbackParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
		MaxLines: maxLines,
	}, nil)
}
