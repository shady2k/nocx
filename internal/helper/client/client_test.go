// Package client_test drives the helper client through a HelperConn fake
// backed by io.Pipe — no SSH anywhere in this file. The handshake matrix
// (design §4, plan Task 6) is the point: every Dial error is a distinct
// sentinel because the product renders them as distinct states (design §6).
// Where the protocol itself is under test, the fake's peer is the REAL
// internal/helper/host, so the client is exercised against the actual
// implementation rather than a reading of it.
package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// fakeConn is a HelperConn backed by io.Pipe with a scripted peer. The peer
// function runs the remote side of the wire — it reads what the client
// writes (stdin) and writes what the client reads (stdout) — and its return
// value is the remote process's exit status. The lane has no pty surface at
// all, which is the point: the pty-less contract (D19) is asserted on the
// real lane in internal/ssh, and this fake cannot express a pty request.
type fakeConn struct {
	started  chan string
	startErr error

	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader

	exited   chan struct{}
	exitCode int

	done    chan struct{}
	lostErr error
}

func newFakeConn(peer func(stdin io.Reader, stdout io.Writer) int) *fakeConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeConn{
		started: make(chan string, 1),
		stdin:   toPeerW,
		stdout:  fromPeerR,
		stderr:  bytes.NewReader(nil),
		exited:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	go func() {
		code := peer(toPeerR, fromPeerW)
		_ = fromPeerW.Close() // EOF: the process ended, so its stdout ends
		f.exitCode = code
		close(f.exited)
	}()
	return f
}

func (f *fakeConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeConn) Stderr() io.Reader     { return f.stderr }

func (f *fakeConn) Start(command string) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started <- command
	return nil
}

func (f *fakeConn) Wait() (int, error) {
	<-f.exited
	return f.exitCode, nil
}

func (f *fakeConn) Done() <-chan struct{} { return f.done }

func (f *fakeConn) LostErr() error {
	select {
	case <-f.done:
		return f.lostErr
	default:
		return nil
	}
}

// lose simulates transport loss: the lane's connection dies under the
// client, closing Done exactly as the real lane's loss watcher does.
func (f *fakeConn) lose(err error) {
	f.lostErr = err
	close(f.done)
}

func (f *fakeConn) Close() error {
	// Closing stdin ends the peer's read; a host-style peer then serves to
	// EOF and exits cleanly.
	return f.stdin.Close()
}

// hostPeer runs the REAL helper host over the fake's pipes. contentHash is
// what the host reports in hello-ok; a nil service is fine for
// handshake-only tests.
func hostPeer(contentHash string, svc host.Service) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1",
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		if svc != nil {
			h.Register(svc)
		}
		if err := h.Serve(context.Background()); err != nil {
			if errors.Is(err, host.ErrVersionMismatch) {
				return host.ExitVersionMismatch
			}
			return 1
		}
		return 0
	}
}

// helloOKPeer answers a hello with the sentinel and a hello-ok, then holds
// the connection open (ignoring everything after the hello) until stdin
// EOF — a faithful peer for every handshake test that does not need a real
// service. It echoes the nonce it received (that is the protocol's claim)
// unless nonceOverride is set, and reports the given content hash.
func helloOKPeer(hash, nonceOverride string) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		answered := false
		dec := proto.NewDecoder(func(ty proto.FrameType, seq, ack uint32, payload []byte) {
			if answered || ty != proto.TypeHello {
				return
			}
			answered = true
			var h proto.Hello
			_ = json.Unmarshal(payload, &h)
			echo := h.Nonce
			if nonceOverride != "" {
				echo = nonceOverride
			}
			_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
			ok := proto.HelloOK{Version: proto.Version, Nonce: echo, ContentHash: hash, InstanceID: "instance-1"}
			raw, _ := json.Marshal(ok)
			_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
		}, nil)
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				_ = dec.Feed(buf[:n])
			}
			if err != nil {
				return 0
			}
		}
	}
}

// stubService is a trivial host service for round-trip tests: op "ping"
// echoes its params back as a string, and op "nope" is deliberately absent
// so a Call to it exercises the unknown_op refusal.
type stubService struct{}

func (stubService) Name() string  { return "stub" }
func (stubService) Ops() []string { return []string{"ping"} }
func (stubService) ParamsSchema(op string) *host.Schema {
	if op != "ping" {
		return nil // unknown ops must reach the host's unknown_op refusal
	}
	return host.SchemaFor(pingParams{})
}

func (stubService) Call(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var p pingParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return p.Msg, nil
}

type pingParams struct {
	Msg string `json:"msg"`
}

func TestDialRefusesAPeerThatIsNotOurHelper(t *testing.T) {
	// A launched helper that is not ours still lets the exec channel buffer
	// our hello; the pipe lane needs the drain to model that.
	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		go func() { _, _ = io.Copy(io.Discard, in) }()
		_, _ = io.WriteString(out, "bash: nocx-helper: No such file or directory\n")
		return 1
	})
	_, err := client.Dial(context.Background(), client.Config{Exec: conn, SentinelTTL: time.Second})
	if !errors.Is(err, client.ErrNotOurHelper) {
		t.Fatalf("want ErrNotOurHelper, got %v", err)
	}
	if !strings.Contains(err.Error(), "No such file") {
		t.Fatalf("the error must carry what was seen: %v", err)
	}
}

func TestDialSentinelTimeoutWhenThePeerNeverWrites(t *testing.T) {
	conn := newFakeConn(func(in io.Reader, _ io.Writer) int {
		_, _ = io.Copy(io.Discard, in) // accept the hello, never answer
		return 0
	})
	_, err := client.Dial(context.Background(), client.Config{Exec: conn, SentinelTTL: 100 * time.Millisecond})
	if !errors.Is(err, client.ErrSentinelTimeout) {
		t.Fatalf("want ErrSentinelTimeout, got %v", err)
	}
}

func TestDialRejectsAMismatchedNonce(t *testing.T) {
	conn := newFakeConn(helloOKPeer("testhash", "wrong-nonce"))
	_, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if !errors.Is(err, client.ErrNotOurHelper) {
		t.Fatalf("want ErrNotOurHelper, got %v", err)
	}
}

func TestDialRejectsAMismatchedContentHash(t *testing.T) {
	conn := newFakeConn(helloOKPeer("other-hash", ""))
	_, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if !errors.Is(err, client.ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
}

// TestDialRejectsAVersionMismatchExit: the helper's one pre-sentinel exit is
// code 42, written after refusing a wrong protocol version and writing
// nothing else (D5). The client must map it to its own sentinel, not to
// "something else answered".
func TestDialRejectsAVersionMismatchExit(t *testing.T) {
	conn := newFakeConn(func(in io.Reader, _ io.Writer) int {
		go func() { _, _ = io.Copy(io.Discard, in) }()
		return 42
	})
	_, err := client.Dial(context.Background(), client.Config{Exec: conn, SentinelTTL: time.Second})
	if !errors.Is(err, client.ErrVersionMismatch) {
		t.Fatalf("want ErrVersionMismatch, got %v", err)
	}
}

// TestDialExecForbidden: a server that refuses the exec surfaces as the
// execForbidden state, not as a timeout.
func TestDialExecForbidden(t *testing.T) {
	conn := newFakeConn(func(_ io.Reader, _ io.Writer) int { return 1 })
	conn.startErr = errors.New("ssh: rejected: administratively prohibited")
	_, err := client.Dial(context.Background(), client.Config{Exec: conn, SentinelTTL: time.Second})
	if !errors.Is(err, client.ErrExecForbidden) {
		t.Fatalf("want ErrExecForbidden, got %v", err)
	}
}

// TestDialAndCallRoundTrip drives the client against the REAL host over
// io.Pipe: hello, sentinel, hello-ok, then a request/response — the actual
// protocol implementation, both halves.
func TestDialAndCallRoundTrip(t *testing.T) {
	conn := newFakeConn(hostPeer("testhash", stubService{}))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if got := c.InstanceID(); got != "instance-1" {
		t.Fatalf("InstanceID = %q, want %q", got, "instance-1")
	}

	var got string
	if err := c.Call(context.Background(), "stub", "ping", pingParams{Msg: "hello helper"}, &got); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got != "hello helper" {
		t.Fatalf("Call result = %q, want %q", got, "hello helper")
	}
}

// TestCallRefusalSurfacesTheHelpersError: a service refusal is a wire fact
// with a code and a message; the caller must receive both, and must be able
// to tell a refusal from a transport loss.
func TestCallRefusalSurfacesTheHelpersError(t *testing.T) {
	conn := newFakeConn(hostPeer("testhash", stubService{}))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	var out string
	err = c.Call(context.Background(), "stub", "nope", pingParams{}, &out)
	if err == nil {
		t.Fatal("want a refusal error, got nil")
	}
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *RefusalError, got %T: %v", err, err)
	}
	if refusal.Code != proto.ErrCodeUnknownOp {
		t.Fatalf("Code = %q, want %q", refusal.Code, proto.ErrCodeUnknownOp)
	}
	if errors.Is(err, client.ErrLost) {
		t.Fatal("a refusal must not read as transport loss")
	}
}

// TestCallFailsOnTransportLoss: a request in flight when the transport dies
// fails with a loss error the caller can distinguish from a refusal, and the
// client's Done closes. The peer answers the hello and then withholds the
// response, so the Call is genuinely waiting when the transport dies under
// it.
func TestCallFailsOnTransportLoss(t *testing.T) {
	conn := newFakeConn(helloOKPeer("testhash", ""))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	errCh := make(chan error, 1)
	go func() {
		var out string
		errCh <- c.Call(context.Background(), "stub", "ping", pingParams{Msg: "x"}, &out)
	}()

	conn.lose(errors.New("connection reset by peer"))
	select {
	case err := <-errCh:
		if !errors.Is(err, client.ErrLost) {
			t.Fatalf("in-flight Call: want ErrLost, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Call did not fail on transport loss")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close on transport loss")
	}
}
