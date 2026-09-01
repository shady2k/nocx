package client

// Dial and the handshake: launch the helper over the pty-less exec lane,
// write one hello, wait for the sentinel line — a LINE, not a frame (D5) —
// then verify the hello-ok. Every failure is one of the sentinels in
// errors.go; the caller owns the lane and must close it when Dial fails.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// Config is everything Dial needs to bring one helper up on one host.
type Config struct {
	Exec        HelperConn // the pty-less exec lane (internal/ssh)
	Command     string     // absolute path to the installed helper
	ExpectHash  string     // the content hash the installer wrote (D21)
	SentinelTTL time.Duration
	Log         *slog.Logger
}

// DefaultSentinelTTL is the handshake budget when Config leaves SentinelTTL
// zero. The sentinel is the first bytes the helper writes after accepting
// the hello; a silent peer is refused after this budget, never waited on.
const DefaultSentinelTTL = 5 * time.Second

// Dial brings one helper up: it starts the lane, writes the hello with a
// freshly minted nonce, waits for the sentinel line within SentinelTTL, and
// verifies the hello-ok echo — the nonce it sent and the content hash it
// installed (D5, D21). On success the returned Client owns the connection
// and the read pump is running. On failure the lane is left for the caller
// to close (the app's provider closes it); Dial never leaves a goroutine
// that outlives the lane.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.SentinelTTL <= 0 {
		cfg.SentinelTTL = DefaultSentinelTTL
	}

	if err := cfg.Exec.Start(cfg.Command); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExecForbidden, err)
	}

	nonce, err := randomID()
	if err != nil {
		_ = cfg.Exec.Close()
		return nil, fmt.Errorf("helper: nonce: %w", err)
	}

	c := &Client{
		conn:        cfg.Exec,
		cfg:         cfg,
		log:         cfg.Log,
		nonce:       nonce,
		pending:     make(map[uint64]chan proto.Response),
		streams:     make(map[uint64]*chunkStream),
		attachments: make(map[[16]byte]*AttachedSession),
		done:        make(chan struct{}),
		hsCh:        make(chan error, 1),
	}

	hello := proto.Hello{Version: proto.Version, Nonce: nonce, Corr: randomCorr()}
	raw, err := json.Marshal(hello)
	if err != nil {
		_ = cfg.Exec.Close()
		return nil, fmt.Errorf("helper: hello: %w", err)
	}
	frame := proto.EncodeFrame(proto.TypeHello, 0, 0, raw)

	// The hello write sits inside the handshake budget: a peer that accepts
	// the exec but never reads stdin must time out, not hang Dial. The write
	// goroutine unblocks when the caller closes the lane on failure.
	writeCh := make(chan error, 1)
	go func() {
		_, err := c.conn.Stdin().Write(frame)
		writeCh <- err
	}()

	c.decoder = proto.NewDecoder(func(ty proto.FrameType, seq, ack uint32, payload []byte) {
		c.onFrame(ty, payload)
	}, func(n int) {
		c.log.Warn("decoder resync", "garbage", n)
	})

	// The exit-status watcher feeds the pump's classification of a peer
	// that ended before the sentinel (D5's exit 42 is the one pre-sentinel
	// exit our helper ever takes).
	c.waitCh = make(chan waitResult, 1)
	go func() {
		code, err := c.conn.Wait()
		c.waitCh <- waitResult{code: code, err: err}
	}()

	// The transport-loss watcher: the lane's connection died under us. This
	// is the deterministic answer when a handshake failure races the loss.
	go func() {
		<-c.conn.Done()
		c.lose(c.conn.LostErr())
	}()

	// The pump scans for the sentinel, then feeds the decoder. It delivers
	// the handshake outcome on hsCh exactly once, and otherwise runs until
	// the stream ends.
	go c.pump()

	deadline := time.NewTimer(cfg.SentinelTTL)
	defer deadline.Stop()
	select {
	case <-c.conn.Done():
		<-c.done
		return nil, fmt.Errorf("%w: %v", ErrLost, c.lostErr)
	case err := <-writeCh:
		if err != nil {
			return nil, fmt.Errorf("helper: write hello: %w", err)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-deadline.C:
		return nil, ErrSentinelTimeout
	}

	select {
	case <-c.conn.Done():
		<-c.done
		return nil, fmt.Errorf("%w: %v", ErrLost, c.lostErr)
	case err := <-c.hsCh:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-deadline.C:
		return nil, ErrSentinelTimeout
	}
	return c, nil
}

// sentinel is the line the helper writes to stdout only after accepting the
// hello (D5): "nocx-helper <version> ready". Its appearance is the claim
// "our helper started", and nothing else claims it. It is a line, not a
// frame, so the handshake scans bytes for it before any framing begins.
func sentinel() string {
	return "nocx-helper " + proto.Version + " ready\n"
}

// waitResult is the lane's exit status: the process's own code, or the
// error Wait returned when the process did not exit cleanly.
type waitResult struct {
	code int
	err  error
}

// pump reads the wire until the stream ends. Before the sentinel is seen it
// scans for the sentinel line and buffers everything (a peer that dies
// without it is classified by what it printed and its exit status); after
// the sentinel it hands every byte to the decoder, including the bytes that
// arrived in the same read as the sentinel — the leftover-bytes trap — so a
// hello-ok that arrived early is never lost.
func (c *Client) pump() {
	scan := &bytes.Buffer{}
	handshaken := false
	buf := make([]byte, 32*1024)
	for {
		n, err := c.conn.Stdout().Read(buf)
		if n > 0 {
			if !handshaken {
				scan.Write(buf[:n])
				idx := bytes.Index(scan.Bytes(), []byte(sentinel()))
				if idx >= 0 {
					handshaken = true
					leftover := append([]byte(nil), scan.Bytes()[idx+len(sentinel()):]...)
					scan.Reset()
					if ferr := c.decoder.Feed(leftover); ferr != nil {
						c.handshakeDone(fmt.Errorf("helper: handshake feed: %w", ferr))
						return
					}
				}
				continue
			}
			if ferr := c.decoder.Feed(buf[:n]); ferr != nil {
				c.lose(fmt.Errorf("helper: feed: %w", ferr))
				return
			}
		}
		if err != nil {
			if !handshaken {
				// The peer ended before the sentinel: classify by its exit
				// status. EOF implies the process ended, and the channel
				// close that ended our read is what unblocks Wait.
				r := <-c.waitCh
				if r.code == exitVersionMismatch {
					c.handshakeDone(ErrVersionMismatch)
					return
				}
				if r.code == exitNoEndpoint {
					// The bridge ran, reached the host and found no helper
					// serving that generation. It is a fact about the HOST,
					// not about the binary or the protocol, so it must not be
					// reported as "not our helper": the recovery is a helper
					// starting, and nothing about the install is wrong.
					c.handshakeDone(ErrHelperNotServing)
					return
				}
				msg := ""
				if seen := scan.Bytes(); len(seen) > 0 {
					msg = fmt.Sprintf(": saw %q", truncate(string(seen)))
				}
				c.handshakeDone(fmt.Errorf("%w%s", ErrNotOurHelper, msg))
				return
			}
			// After the handshake, a stream that ends is the helper going
			// away: everything in flight is lost, never refused.
			c.lose(fmt.Errorf("helper: stream ended"))
			return
		}
	}
}

// handshakeDone delivers the handshake outcome once; the pump and the
// hello-ok verifier both report through it, and neither may block on it.
func (c *Client) handshakeDone(err error) {
	select {
	case c.hsCh <- err:
	default:
	}
}

// verifyHelloOK checks the hello-ok echo: the nonce we sent (D5) and the
// content hash we installed (D21). The instance id is recorded and unused
// (D15 reserves it for a later reattach).
func (c *Client) verifyHelloOK(payload []byte) {
	var ok proto.HelloOK
	if err := json.Unmarshal(payload, &ok); err != nil {
		c.handshakeDone(fmt.Errorf("%w: malformed hello-ok: %v", ErrNotOurHelper, err))
		return
	}
	if ok.Nonce != c.nonce {
		c.handshakeDone(fmt.Errorf("%w: nonce mismatch", ErrNotOurHelper))
		return
	}
	if ok.Version != proto.Version {
		c.handshakeDone(ErrVersionMismatch)
		return
	}
	if ok.ContentHash != c.cfg.ExpectHash {
		c.handshakeDone(fmt.Errorf("%w: content hash %q, want %q", ErrHashMismatch, ok.ContentHash, c.cfg.ExpectHash))
		return
	}
	c.instanceID = ok.InstanceID
	c.handshakeDone(nil)
}

// truncate bounds a refusal's "what was seen" to something a log line can
// carry; the full prefix of what a hostile peer printed is not a document.
func truncate(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func randomCorr() string {
	id, err := randomID()
	if err != nil {
		return ""
	}
	return id
}
