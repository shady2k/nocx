package client

// The coordinator's end of a PANE'S TOOL SOCKET: a unix socket on a far host
// whose connections the helper pipes into this machine's tool endpoint, with
// the pane record written first (nocx-e2bws).
//
// # Why this is not a *Forward
//
// A Forward is a stream FACTORY whose accepted connections arrive here as
// proxied channels. Nothing arrives on a tool socket: the helper writes the
// pane record into the endpoint itself and pumps, because the endpoint cannot
// admit a connection it cannot attribute to a pane and this process is the
// wrong party to ask — it does not hold the listener. So there is no accept
// queue, no announcement, no data plane here. What the coordinator holds is the
// LISTENER, and its whole lifecycle is one id and one call.
//
// That is also why the id is a plain ForwardID and not a type of its own: the
// helper holds one namespace of listeners and ends any of them with the same
// `unforward`, and a second type would be a second way to say "a listener this
// helper holds" (AD-8).
//
// # Who owns it, and when it ends
//
// The COORDINATOR that opened it, and it ends when the session it was opened
// for ends: `OpenToolSocket` answers the id and `CloseListener` ends it, both
// on the same connection. A caller that keeps only the id cannot end it by
// accident from another process (the helper holds listeners per connection —
// `host.ConnectionFrom`), and a caller that forgets ends it with the connection
// the request arrived on, which is the helper's own teardown. That is the same
// rule the pane's own listeners follow one process over ("the session is the
// thing it holds now, and it ends at exactly the same moment").

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// OpenToolSocket asks the helper for one pane's far-side tool socket.
//
// The path is the FAR host's, and this is the party that knows it: the helper
// cannot read somebody else's filesystem, so a path it invented would be a
// guess (proto.ToolSocketParams says so at length). A refusal here is the SERVER
// refusing the streamlocal bind — a directory that does not exist, a name
// already taken — or this helper's own refusal of an incomplete request, and
// both arrive as the helper classified them.
func (c *Client) OpenToolSocket(ctx context.Context, params proto.ToolSocketParams) (proto.ToolSocketResult, error) {
	if params.Path == "" || params.Target == "" || params.Session == "" {
		// Refused here as well as at the helper, and not as a courtesy: a
		// request this process knows is incomplete must not cost the far host a
		// connection, and the caller's own mistake is better named locally than
		// as a refusal that has travelled.
		return proto.ToolSocketResult{}, errors.New("helper: open tool socket: path, target and session are all required")
	}
	var result proto.ToolSocketResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpToolSocket, params, &result); err != nil {
		return proto.ToolSocketResult{}, err
	}
	if result.Forward.IsZero() {
		return proto.ToolSocketResult{}, errors.New("helper: open tool socket: the helper answered no forward id")
	}
	return result, nil
}

// CloseListener ends one listener this helper holds, whatever opened it: a
// remote forward's TCP listener or a pane's tool socket. Both answer the same
// ForwardID and both are ended by `ssh.unforward`, and this method exists so a
// caller that opened a tool socket has one call to end it by rather than a
// hand-rolled request (the idempotence is the helper's: an id it no longer
// holds is answered as done).
func (c *Client) CloseListener(ctx context.Context, id proto.ForwardID) error {
	if id.IsZero() {
		return errors.New("helper: close listener: no forward id")
	}
	return c.Call(ctx, proto.ServiceSSH, proto.OpUnforward, proto.UnforwardParams{Forward: id}, nil)
}
