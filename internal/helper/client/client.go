package client

// The Client: one helper instance on one host, over one exec channel. After
// Dial the read pump runs for the connection's lifetime; Call sends one
// named operation and waits for its response, Cancel sends a cancel frame,
// and transport loss fails every request in flight with ErrLost — never a
// refusal — and closes Done.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// HelperConn is the pty-less exec lane the client rides (design D19): one
// long-lived exec session opened WITHOUT a pty-req — a pty applies line
// discipline and would corrupt the binary frames — with pipes, on a pooled
// reference the lane owns itself (ADR-0020, the DiscoveryConn shape).
// internal/ssh provides the real implementation; the interface exists so
// the client is testable over io.Pipe with no SSH at all.
type HelperConn interface {
	// Stdin returns the lane's stdin — the client's frame output.
	Stdin() io.WriteCloser
	// Stdout returns the lane's stdout — the wire.
	Stdout() io.Reader
	// Stderr returns the lane's stderr — diagnostics only (D22).
	Stderr() io.Reader
	// Start launches command over the already-open session. The channel was
	// opened without a pty-req (D19); a server refusal of the exec surfaces
	// here.
	Start(command string) error
	// Wait returns the remote exit status once the command has exited.
	// Call it once.
	Wait() (int, error)
	// Done closes when the underlying connection shuts down: connection
	// loss, server close, keepalive failure. It does NOT close on Close.
	Done() <-chan struct{}
	// LostErr reports why the connection shut down. Meaningful once Done
	// has closed; nil when the connection closed cleanly.
	LostErr() error
	// Close ends the lane: the session is closed and the lease's pooled
	// reference is released.
	Close() error
}

// Client is one helper instance on one host, over one exec channel.
type Client struct {
	conn HelperConn
	cfg  Config
	log  *slog.Logger

	nonce      string
	instanceID string

	decoder *proto.Decoder

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint64]chan proto.Response
	nextID  uint64
	lost    bool

	done      chan struct{}
	hsCh      chan error
	waitCh    chan waitResult
	doneOnce  sync.Once
	closeOnce sync.Once

	lostErr error
}

// InstanceID is the helper's self-issued instance id from the hello-ok
// (D15): recorded and unused, reserved for a later reattach.
func (c *Client) InstanceID() string { return c.instanceID }

// Done closes when the transport is lost: connection loss, server close,
// keepalive failure. It does not close on Close.
func (c *Client) Done() <-chan struct{} { return c.done }

// Call sends one named operation and waits for its response. A refusal
// (proto.Error) returns *RefusalError; a dead transport returns an error
// wrapping ErrLost; ctx cancellation returns ctx.Err(). out, when non-nil,
// receives the decoded result.
func (c *Client) Call(ctx context.Context, service, op string, params, out any) error {
	id := c.mintID()

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("helper: params: %w", err)
		}
		raw = b
	}
	req := proto.Request{ID: id, Service: service, Op: op, Params: raw, Corr: randomCorr()}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("helper: request: %w", err)
	}

	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.lost {
		c.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrLost, c.lostErr)
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	c.writeMu.Lock()
	_, err = c.conn.Stdin().Write(proto.EncodeFrame(proto.TypeRequest, 0, 0, payload))
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("helper: write request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return &RefusalError{Code: resp.Error.Code, Message: resp.Error.Message}
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("helper: result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("%w: %v", ErrLost, c.lostErr)
	}
}

// Cancel sends a TypeCancel frame naming the request. Whether the helper
// honours it is the operation's decision; mutations are never cancelled
// (D11) — the git service refuses a cancel naming one.
func (c *Client) Cancel(id uint64) {
	payload, err := json.Marshal(struct {
		ID uint64 `json:"id"`
	}{ID: id})
	if err != nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, _ = c.conn.Stdin().Write(proto.EncodeFrame(proto.TypeCancel, 0, 0, payload))
}

// Close ends the connection: the lane is closed — the session dies, the
// helper's stdin reaches EOF and the helper exits — and the lane releases
// its pooled reference. Requests still in flight fail with ErrLost; the
// registry drains its use-guards before closing, so no caller sees that.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		_ = c.conn.Close()
	})
	return nil
}

func (c *Client) mintID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

// lose marks the transport lost: every request in flight fails with ErrLost
// (their select on c.done), Done closes, and later Calls are refused with
// ErrLost rather than sent into the void.
func (c *Client) lose(reason error) {
	c.doneOnce.Do(func() {
		c.lostErr = reason
		c.mu.Lock()
		c.lost = true
		c.mu.Unlock()
		close(c.done)
	})
}

// onFrame routes one decoded frame. The hello-ok belongs to the handshake;
// responses go to their waiting Call by id; chunk frames cannot arrive
// until the service chunks (D14 lands with nocx-w3i1), and keepalives have
// no answer.
func (c *Client) onFrame(ty proto.FrameType, payload []byte) {
	switch ty {
	case proto.TypeHelloOK:
		c.verifyHelloOK(payload)
	case proto.TypeResponse:
		c.deliverResponse(payload)
	case proto.TypeChunk:
		c.log.Warn("chunk frame before chunking lands", "bytes", len(payload))
	case proto.TypeKeepAlive:
		// nothing to answer; keepalives keep the transport warm
	case proto.TypeNotify:
		c.log.Warn("notify frame", "bytes", len(payload))
	default:
		c.log.Warn("unexpected frame", "type", ty)
	}
}

func (c *Client) deliverResponse(payload []byte) {
	var resp proto.Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		c.log.Warn("malformed response", "err", err)
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	if ok {
		delete(c.pending, resp.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}
