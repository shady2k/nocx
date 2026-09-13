package client

// The coordinator's end of a LANE: an exec channel the HELPER opened, running
// the remote helper's bridge, presented as the same HelperConn a coordinator's
// own exec lane was.
//
// # Why the carrier is a channel and not a second connection
//
// A lane is not a different kind of thing from an sftp channel: it is a stream
// on the pooled ssh connection, keyed by an id the helper minted, carried by the
// same frames, ended by the same close and announced by the same notification
// (internal/helper/sshsvc's lane op). Everything below this type is therefore
// the channel plane unchanged, which is what lets a lane ride the connection
// every other consumer of that destination rides (AD-4) instead of raising a
// second one.
//
// # The two things a lane adds
//
// Its far end is a PROCESS, so the closed event carries an exit status and this
// type reports it through Wait — which is not decoration: Dial reads it to tell
// "no helper is serving that generation" (the bridge's own exit 43) from "the
// host did not answer with our helper", and losing it would collapse two facts
// a person acts on differently into one.
//
// And the command is NOT the caller's. Start therefore refuses a non-empty
// command exactly as SocketConn refuses one, and for the same reason: the party
// that launches the helper is the helper (D3 — an argv is the capability this
// level exists not to hand out), and a caller passing one has confused a lane
// with the exec session it replaced. A silent no-op would launch the wrong
// generation's binary and look like it worked.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ErrNoCommandOnALane is a lane carrier handed a command to launch. See the
// file comment: the helper built the invocation from the machine and the
// generation the caller named, and there is nothing left for a caller to name.
var ErrNoCommandOnALane = errors.New("helper: this lane was started by the helper; there is no command to launch")

// OpenLane asks the helper for one exec lane to a remote helper's bridge and
// answers the carrier Dial rides.
//
// The id is the helper's and it is learned from the answer, so this is
// OpenChannel's race with OpenChannel's answer: a lane that writes a byte before
// its open returns would write about an id this side cannot address yet, which
// for a frame protocol is a hang rather than a slow start.
func (c *Client) OpenLane(ctx context.Context, params proto.LaneParams) (*LaneConn, error) {
	if params.Generation == "" {
		return nil, errors.New("helper: open lane: no generation")
	}
	var result proto.OpenChannelResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpLane, params, &result); err != nil {
		return nil, err
	}
	if result.Channel.IsZero() {
		return nil, errors.New("helper: open lane: the helper answered no channel id")
	}
	s, err := c.claimChannel(result.Channel)
	if err != nil {
		// A lane nobody can address is a bridge the far host is running for a
		// caller that has forgotten it, so it is closed rather than left — and
		// the close is bounded for the reason every other close here is: this
		// branch runs while the caller waits for an answer.
		closeCtx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		_ = c.tellHelperChannelClosed(closeCtx, result.Channel)
		return nil, fmt.Errorf("helper: the helper reused lane id %s", result.Channel)
	}
	return &LaneConn{ChannelStream: s}, nil
}

// LaneConn is one helper-opened exec lane, as client.HelperConn.
//
// It embeds the channel stream rather than wrapping it because the two
// directions of a lane ARE the channel's bytes: what the helper opened is one
// duplex stream, and stdin and stdout are the same stream read and written —
// which is exactly what SocketConn's two halves add up to as well.
type LaneConn struct {
	*ChannelStream
}

// Stdin is what the frame protocol is written to.
func (l *LaneConn) Stdin() io.WriteCloser { return l.ChannelStream }

// Stdout is the wire.
func (l *LaneConn) Stdout() io.Reader { return l.ChannelStream }

// Stderr is empty and stays empty, for the reason SocketConn's is: the helper
// drained the remote bridge's diagnostics where they arrived (its own log), and
// inventing a second stream for them here would be a stream nothing writes and
// a reader nothing feeds.
func (l *LaneConn) Stderr() io.Reader { return eofReader{} }

// Start refuses a command: see ErrNoCommandOnALane. An empty command is the
// no-op it should be — the helper has already started the bridge.
func (l *LaneConn) Start(command string) error {
	if command != "" {
		return ErrNoCommandOnALane
	}
	return nil
}

// Wait reports the exit status the remote bridge ended with, once it has.
//
// A lane whose stream ended WITHOUT a status — the transport died, or the far
// end was never a process to begin with — answers the error that ended it
// rather than a fabricated zero: exit 0 is an ordinary way for a process to
// end, and reporting one for a connection that died would be a lie the
// coordinator's own classification acts on.
func (l *LaneConn) Wait() (int, error) {
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hasExit {
		return l.exit, nil
	}
	if l.lost != nil {
		return 0, l.lost
	}
	return 0, io.EOF
}

// Done reports the lane's TRANSPORT, and the distinction is the whole of this
// method: it closes when the stream ended for a reason nobody here asked for
// AND no process reported an exit status — a helper connection or an ssh
// connection that died under the lane.
//
// It deliberately does not close when the remote bridge EXITS (that is an exit
// status, and Dial classifies it: exit 43 is "no helper is serving that
// generation", not a lost connection) nor when the caller closes the lane
// (HelperConn's own contract: Done is not the caller's own act).
func (l *LaneConn) Done() <-chan struct{} { return l.gone }

// LostErr reports why the transport went, once Done has closed; nil before it
// does, and nil when what ended was the remote process rather than the
// transport.
func (l *LaneConn) LostErr() error {
	select {
	case <-l.gone:
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.lost
	default:
		return nil
	}
}

// Close ends the lane: the helper is told, which closes the far session and so
// ends the bridge, and every reader here is unblocked.
func (l *LaneConn) Close() error { return l.ChannelStream.Close() }
