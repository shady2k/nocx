package client

// The connection half of the ssh service's client: the questions a coordinator
// asks about a pane's connection, and the one it asks to end it (design §5.3,
// §5.6; nocx-oalo1.5.3 and .5.4).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ConnectionID asks the helper which connection a destination's shell will
// ride. It dials nothing and asks no person: the answer is the supervisor key
// derived from the resolved destination, which is why it can be asked BEFORE
// the shell is opened and why the pane can be bound that early.
//
// A helper that does not answer the op is older than this app; the error is the
// ordinary `unknown_op` refusal, and the caller is expected to read it that way.
func (c *Client) ConnectionID(ctx context.Context, destination proto.SSHDestination) (string, error) {
	var result proto.ConnectionResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpConnection, proto.ConnectionParams{
		Destination: destination,
	}, &result); err != nil {
		return "", err
	}
	if result.ConnectionID == "" {
		return "", fmt.Errorf("helper: the connection op answered no connection id")
	}
	return result.ConnectionID, nil
}

// Disconnect closes the connection a destination rides, if the helper holds one,
// and reports whether it did. It is the reconnect's first move (design §5.3).
func (c *Client) Disconnect(ctx context.Context, destination proto.SSHDestination) (bool, error) {
	var result proto.DisconnectResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpDisconnect, proto.DisconnectParams{
		Destination: destination,
	}, &result); err != nil {
		return false, err
	}
	return result.Closed, nil
}

// connectionState routes one `ssh.connection-state` notification to the
// configured observer. A caller with no observer pays no unmarshal: the frame
// is dropped by its own shape, not by a state.
func (c *Client) connectionState(raw json.RawMessage) {
	if c.cfg.OnConnectionEvent == nil {
		return
	}
	var ev proto.ConnectionStateEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.log.Warn("malformed connection-state notification", "err", err)
		return
	}
	c.cfg.OnConnectionEvent(c.instanceID, ev)
}

// BindConnection registers a pane's logical hold before its first dial.
func (c *Client) BindConnection(ctx context.Context, destination proto.SSHDestination, paneID, policy string) (string, error) {
	var res proto.ConnectionResult
	err := c.Call(ctx, proto.ServiceSSH, proto.OpConnection, proto.ConnectionParams{Destination: destination, PaneID: paneID, ReconnectPolicy: policy}, &res)
	return res.ConnectionID, err
}

// RegisterReconnect closes the socket first and answers before the new dial.
func (c *Client) RegisterReconnect(ctx context.Context, destination proto.SSHDestination, expectedSeq uint64) error {
	var res proto.ReconnectResult
	return c.Call(ctx, proto.ServiceSSH, proto.OpReconnect, proto.ReconnectParams{Destination: destination, ExpectedSeq: expectedSeq}, &res)
}

// ReleaseConnection drops the logical pane hold when its layout pane closes.
func (c *Client) ReleaseConnection(ctx context.Context, paneID string) error {
	var res proto.ReleaseConnectionResult
	return c.Call(ctx, proto.ServiceSSH, proto.OpReleaseConnection, proto.ReleaseConnectionParams{PaneID: paneID}, &res)
}
