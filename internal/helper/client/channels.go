package client

// The coordinator's end of a PROXIED CHANNEL: a stream the helper opened on a
// pooled ssh connection, carried to this process as raw bytes keyed by a
// proto.ChannelID.
//
// # Why this is not an AttachedSession
//
// An AttachedSession is a READER of something the helper owns — a shell with a
// window, a cursor, an inventory entry and a replay ring — and every part of
// its shape exists for that. A proxied channel has none of it: the helper
// opened it at this process's request, exactly one coordinator reads it, and
// it ends when either end says so. Sharing one type would mean carrying a
// subscriber and a lease epoch for a stream that has neither, and the first
// thing that would go wrong is somebody attaching to one.
//
// What they DO share is the queue and its rule: pushing never blocks, because
// the connection has one read loop and a read loop that waits on a consumer is
// a client that wedges for every session on the helper (see stream's own doc).
// So this reuses that queue rather than writing a second one.
//
// # The two ways a channel ends, and why only one is a frame
//
// The LOCAL end ends by calling Close, which tells the helper (ssh.close) and
// unblocks every reader here. The REMOTE end ends with an ssh.channel-closed
// notification, which the helper sends once it knows — and because a
// notification rides the same wire as the data frames, every byte written
// before it has already been written. That ordering is what makes a
// notification sufficient where an end-of-stream marker would have to be
// invented inside the data plane, where a zero-length frame is a legitimate
// write of no bytes (proto.ChannelFrame says so).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ErrChannelClosed is returned by a channel stream whose end has gone: this
// side closed it, or the helper reported the remote end gone. It is not
// ErrLost — the connection may be perfectly healthy — and the distinction is
// what lets a caller tell "this stream ended" from "the helper is gone".
var ErrChannelClosed = errors.New("helper: the channel stream is closed")

// channelCloseTimeout bounds the ssh.close REQUEST.
//
// It exists because Close has a promise to keep that the request does not: the
// caller must be released even when the helper has gone quiet. A Call waits for
// an answer, and a helper that is alive enough to hold the socket but not alive
// enough to dispatch would park a Close for ever — in a deferred cleanup, which
// is where a Close usually runs. The bound is a courtesy's bound and not a
// retry budget: the notification is best-effort, the local release already
// happened, and what this stops is a shutdown that never finishes.
const channelCloseTimeout = 2 * time.Second

// ChannelStream is one proxied channel, as an io.ReadWriteCloser.
//
// Read returns io.EOF once the stream has ended AND everything already queued
// has been handed over — the rule AttachedSession.take applies, and the reason
// it is stated rather than inherited from a select: a select over the queue and
// a closed done picks uniformly, so with k payloads buffered the odds of losing
// the last one are 2^-k, and the last one is the sftp STATUS that says whether
// the write landed.
type ChannelStream struct {
	client *Client
	id     proto.ChannelID

	data *stream
	done chan struct{}

	mu        sync.Mutex
	lost      error
	closed    bool
	closeOnce sync.Once
}

// OpenChannel asks the helper for one proxied channel and returns its stream.
//
// The id is minted by the helper and this side only learns it from the answer,
// so there is a window in which bytes for that channel can arrive before
// anything here knows what they belong to. Two things close it and BOTH are
// needed:
//
//   - the helper does not write a byte of a channel until the open's response
//     is on the wire (host.ResponseObserver runs the reader pump only after the
//     write), so on the wire the answer always precedes the data;
//   - and this side PARKS a frame whose id nobody has registered yet
//     (channelData's pending queue), because "the answer precedes the data on
//     one wire" is not the same statement as "the caller has registered by the
//     time the reader loop reaches the next frame": the answer wakes the CALLER
//     goroutine and the reader loop goes straight on, so the two genuinely race
//     and a fix that relied on the first alone would drop the first bytes of a
//     stream — for an sftp handshake, a hang.
//
// On a refusal nothing is registered and nothing is returned.
func (c *Client) OpenChannel(ctx context.Context, params proto.OpenChannelParams) (*ChannelStream, error) {
	if params.Kind == "" {
		return nil, errors.New("helper: open channel: no kind")
	}
	var result proto.OpenChannelResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpOpen, params, &result); err != nil {
		return nil, err
	}
	if result.Channel.IsZero() {
		return nil, errors.New("helper: open channel: the helper answered no channel id")
	}
	s := &ChannelStream{
		client: c,
		id:     result.Channel,
		data:   newStream(),
		done:   make(chan struct{}),
	}
	c.mu.Lock()
	if _, exists := c.channels[result.Channel]; exists {
		c.mu.Unlock()
		// The helper minted an id it is already serving. Closing it is the
		// only honest answer: it is a stream nobody can address, and leaving
		// it open would be a channel the helper holds for a caller that has
		// forgotten it. The close is BOUNDED for the same reason every other
		// close here is: this branch runs while the caller is waiting for an
		// answer, and a helper that has gone quiet must not turn a protocol
		// mistake into a hang.
		ctx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		_ = c.tellHelperChannelClosed(ctx, result.Channel)
		return nil, fmt.Errorf("helper: the helper reused channel id %s", result.Channel)
	}
	c.channels[result.Channel] = s
	// Anything that arrived while this call was in flight goes in FIRST, in
	// the order it arrived: the queue is the stream's own, so the parked bytes
	// land ahead of whatever the helper sends next.
	if parked, ok := c.parkedChannels[result.Channel]; ok {
		delete(c.parkedChannels, result.Channel)
		for _, payload := range parked {
			s.data.push(inbound{payload: payload})
		}
	}
	c.mu.Unlock()
	return s, nil
}

// CloseChannel ends a channel by id, for a caller that has the id and not the
// stream. It is the same act ChannelStream.Close performs, and it takes the
// caller's context because a caller that HAS the id is by definition not the
// stream's reader and can say how long it is willing to wait.
func (c *Client) CloseChannel(ctx context.Context, id proto.ChannelID) error {
	if id.IsZero() {
		return errors.New("helper: close channel: no channel id")
	}
	c.forgetChannel(id, ErrChannelClosed)
	return c.Call(ctx, proto.ServiceSSH, proto.OpClose, proto.CloseChannelParams{Channel: id}, nil)
}

// ID is the wire identity of this stream, for a log line or a test.
func (s *ChannelStream) ID() proto.ChannelID { return s.id }

// Read hands over queued bytes. It returns io.EOF when the stream has ended
// and the queue is drained, and the end's own error when there was one.
func (s *ChannelStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		wake := s.data.wait()
		if item, ok := s.data.pop(); ok {
			n := copy(p, item.payload)
			if n < len(item.payload) {
				s.data.unpop(inbound{payload: item.payload[n:]})
			}
			return n, nil
		}
		select {
		case <-wake:
		case <-s.done:
			// Drained first, closed second: see the type's own doc.
			item, ok := s.data.pop()
			if !ok {
				s.mu.Lock()
				lost := s.lost
				s.mu.Unlock()
				if lost != nil {
					return 0, lost
				}
				return 0, io.EOF
			}
			n := copy(p, item.payload)
			if n < len(item.payload) {
				s.data.unpop(inbound{payload: item.payload[n:]})
			}
			return n, nil
		case <-s.client.done:
			s.mu.Lock()
			lost := s.lost
			s.mu.Unlock()
			if lost == nil {
				lost = ErrLost
			}
			return 0, lost
		}
	}
}

// Write puts bytes on the channel. The frame is the whole of what crosses:
// the helper moves it and never reads it (AD-6).
func (s *ChannelStream) Write(p []byte) (int, error) {
	select {
	case <-s.done:
		return 0, ErrChannelClosed
	default:
	}
	if err := s.client.sendChannelData(s.id, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close ends this end of the channel: the helper is told, the registration is
// dropped, and every reader here is unblocked. It is idempotent, and the
// notification is best-effort — a Close whose helper never answers must still
// release the caller, whose next act is usually to exit.
func (s *ChannelStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		// The local half first, and unconditionally: forgetChannel unblocks
		// every reader here, so the stream is over before the helper is asked
		// anything.
		s.client.forgetChannel(s.id, ErrChannelClosed)
		ctx, cancel := context.WithTimeout(context.Background(), channelCloseTimeout)
		defer cancel()
		err = s.client.tellHelperChannelClosed(ctx, s.id)
	})
	return err
}

// tellHelperChannelClosed sends the ssh.close request, which the close path
// reaches through its own bounded context (channelCloseTimeout).
func (c *Client) tellHelperChannelClosed(ctx context.Context, id proto.ChannelID) error {
	return c.Call(ctx, proto.ServiceSSH, proto.OpClose, proto.CloseChannelParams{Channel: id}, nil)
}

// sendChannelData writes one TypeChannelData frame. It takes the client's
// write mutex for the reason every other producer does: the wire has one
// writer, and two frames interleaved mid-header is a decoder resync.
func (c *Client) sendChannelData(id proto.ChannelID, payload []byte) error {
	c.mu.Lock()
	lost := c.lost
	lostErr := c.lostErr
	c.mu.Unlock()
	if lost {
		// Both fields are read UNDER the lock, and the error with them: lose
		// sets lostErr and the flag from another goroutine, so a snapshot
		// taken half-way reports a loss with no cause while claiming the
		// reason is known.
		return fmt.Errorf("%w: %v", ErrLost, lostErr)
	}
	frame := proto.EncodeChannelFrame(proto.ChannelFrame{Channel: id, Payload: payload})
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.conn.Stdin().Write(proto.EncodeFrame(proto.TypeChannelData, 0, 0, frame)); err != nil {
		return err
	}
	return nil
}

// channelData routes one inbound frame to its stream.
//
// An id nobody has registered is PARKED rather than dropped, because the one
// case that produces it is the one that must not lose bytes: the open's call is
// still in flight (see OpenChannel). The park is bounded, and past the bound
// the frames are dropped with a log line — a caller that never comes back for
// an id must not be able to grow this client's memory from the far end.
func (c *Client) channelData(payload []byte) {
	f, err := proto.DecodeChannelFrame(payload)
	if err != nil {
		c.log.Warn("malformed channel data frame", "err", err, "bytes", len(payload))
		return
	}
	if f.Channel.IsZero() {
		// A zero id names no channel, and it is refused rather than parked or
		// looked up: ChannelID's own contract is that the zero value is
		// refused at every boundary, because a frame whose identity was never
		// filled in must not resolve to "some channel" — and a park is a
		// lookup that has not happened yet.
		c.log.Warn("channel data dropped: the frame names no channel", "bytes", len(f.Payload))
		return
	}
	owned := append([]byte(nil), f.Payload...)
	c.mu.Lock()
	if s := c.channels[f.Channel]; s != nil {
		c.mu.Unlock()
		s.data.push(inbound{payload: owned})
		return
	}
	parked := c.parkedChannels[f.Channel]
	if len(parked) >= parkedChannelFrames || parkedBytes(c.parkedChannels)+len(owned) > parkedChannelBytes {
		c.mu.Unlock()
		c.log.Warn("channel data dropped: nothing has claimed this channel and the park is full",
			"channel", f.Channel.String(), "bytes", len(owned))
		return
	}
	c.parkedChannels[f.Channel] = append(parked, owned)
	c.mu.Unlock()
	c.log.Debug("channel data parked until its open returns", "channel", f.Channel.String(), "bytes", len(owned))
}

// parkedChannelFrames and parkedChannelBytes bound the park. They are small
// on purpose: the window they cover is one round trip, and a bound that could
// hold a stream is a bound that hides a caller who never asked.
const (
	parkedChannelFrames = 64
	parkedChannelBytes  = 1 << 20
)

func parkedBytes(parked map[proto.ChannelID][][]byte) int {
	total := 0
	for _, frames := range parked {
		for _, f := range frames {
			total += len(f)
		}
	}
	return total
}

// channelNotify handles the ssh service's notifications. Only one exists: a
// channel's remote end is gone.
func (c *Client) channelNotify(raw json.RawMessage) {
	var ev proto.ChannelClosedEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		c.log.Warn("malformed channel-closed notification", "err", err)
		return
	}
	var cause error = io.EOF
	if ev.Error != "" {
		cause = fmt.Errorf("helper: channel closed: %s", ev.Error)
	}
	c.forgetChannel(ev.Channel, cause)
}

// forgetChannel ends a stream from this side: it drops the registration and
// closes the stream, which unblocks its readers. The queue is NOT cleared —
// Read drains what was already written before it reports the end.
func (c *Client) forgetChannel(id proto.ChannelID, cause error) {
	c.mu.Lock()
	s := c.channels[id]
	delete(c.channels, id)
	// A channel that has ended is not going to be claimed: bytes parked for it
	// are bytes nobody will ever read, and holding them would be a leak with a
	// caller's name on it.
	delete(c.parkedChannels, id)
	c.mu.Unlock()
	if s == nil {
		return
	}
	s.finish(cause)
}

// finish closes the stream once, recording why.
func (s *ChannelStream) finish(cause error) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.lost = cause
		close(s.done)
	}
	s.mu.Unlock()
}
