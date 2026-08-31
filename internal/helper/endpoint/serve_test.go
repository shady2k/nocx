package endpoint_test

// The accept loop, over the real endpoint, with the real session service
// behind it. What nocx-k6p18.3 proved over a pipe pair is proved here over the
// socket that is the actual boundary — and one thing it could not prove at all
// is proved with it: a SECOND connection reaching a session the first one
// opened, while the first is still there (D12, same-UID trust).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// helperDaemon is `nocx-helper serve` with everything but the binary: one
// process-scoped session service, one listener, and the accept loop from
// production putting a fresh protocol engine on every connection.
type helperDaemon struct {
	dir  string
	svc  *session.Service
	ln   net.Listener
	done chan struct{}
}

func startDaemon(t *testing.T, shell session.Shell) *helperDaemon {
	t.Helper()
	dir := runDir(t)
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	svc := session.New(session.Options{
		Generation: gen,
		Spawner:    session.NewLocalSpawner(discardLog(), shell),
		Inspector:  session.NewProcFS(),
		Log:        discardLog(),
		Limits:     session.DefaultLimits(),
	})
	d := &helperDaemon{dir: dir, svc: svc, ln: ln, done: make(chan struct{})}

	ctx, stop := context.WithCancel(context.Background())
	go func() {
		defer close(d.done)
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			h := host.New(conn, conn, string(gen), "instance", discardLog())
			h.Register(svc)
			release := svc.Bind(h)
			defer release()
			_ = h.Serve(ctx)
		})
	}()
	t.Cleanup(func() {
		stop()
		<-d.done
		svc.Close()
	})
	return d
}

// coordinator is one connection to the endpoint, speaking the frame protocol.
// It is the minimal wire client the reattach test uses, moved onto a socket:
// the point of the test is what crosses the boundary, so it records the RAW
// bytes it read as well as what it decoded out of them.
type coordinator struct {
	t    *testing.T
	conn io.ReadWriteCloser

	mu        sync.Mutex
	raw       []byte
	responses map[uint64]proto.Response
	stream    map[[16]byte][]byte
	arrived   chan struct{}
	seq       uint64
}

func connect(t *testing.T, d *helperDaemon) *coordinator {
	t.Helper()
	conn, err := endpoint.Dial(context.Background(), d.dir, gen)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return connectOver(t, conn)
}

// connectOver runs the handshake over any stream that reaches the endpoint.
// There are two — the socket a local coordinator dials, and the ssh exec lane
// a bridge sits on the far side of — and this function is the proof that
// nothing above the carrier can tell them apart: every test below drives one
// or the other through it, and not one of them says which it got.
func connectOver(t *testing.T, conn io.ReadWriteCloser) *coordinator {
	t.Helper()
	c := &coordinator{
		t: t, conn: conn,
		responses: make(map[uint64]proto.Response),
		stream:    make(map[[16]byte][]byte),
		arrived:   make(chan struct{}),
	}
	t.Cleanup(c.close)

	c.write(proto.TypeHello, mustJSON(t, proto.Hello{Version: proto.Version, Nonce: "n"}))

	// The sentinel line comes before any frame, exactly as it does over the
	// exec lane: the boundary changed, the protocol did not.
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if err != nil {
			t.Fatalf("sentinel: %v", err)
		}
		if n == 1 {
			c.mu.Lock()
			c.raw = append(c.raw, one[0])
			c.mu.Unlock()
			if one[0] == '\n' {
				break
			}
			line = append(line, one[0])
		}
	}
	if want := "nocx-helper " + proto.Version + " ready"; string(line) != want {
		t.Fatalf("sentinel = %q, want %q", line, want)
	}
	go c.read()
	return c
}

func (c *coordinator) read() {
	dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
		c.mu.Lock()
		switch ty {
		case proto.TypeResponse:
			var resp proto.Response
			if err := json.Unmarshal(payload, &resp); err == nil {
				c.responses[resp.ID] = resp
			}
		case proto.TypeSessionData:
			if f, err := proto.DecodeSessionFrame(payload); err == nil {
				c.stream[f.Subscriber] = append(c.stream[f.Subscriber], f.Payload...)
			}
		}
		close(c.arrived)
		c.arrived = make(chan struct{})
		c.mu.Unlock()
	}, nil)
	buf := make([]byte, 32*1024)
	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.raw = append(c.raw, buf[:n]...)
			c.mu.Unlock()
			_ = dec.Feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (c *coordinator) write(ty proto.FrameType, payload []byte) {
	if _, err := c.conn.Write(proto.EncodeFrame(ty, 0, 0, payload)); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

func (c *coordinator) waiter() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.arrived
}

// await blocks until want() holds, waking on each frame. Never on a clock: a
// condition that never becomes true is reported by the test's own timeout.
func (c *coordinator) await(what string, want func() bool) {
	c.t.Helper()
	for {
		next := c.waiter()
		if want() {
			return
		}
		select {
		case <-next:
		case <-c.t.Context().Done():
			c.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (c *coordinator) request(op string, params, out any) {
	c.t.Helper()
	c.mu.Lock()
	c.seq++
	id := c.seq
	c.mu.Unlock()

	c.write(proto.TypeRequest, mustJSON(c.t, proto.Request{
		ID: id, Service: proto.ServiceSession, Op: op, Params: mustJSON(c.t, params),
	}))
	var resp proto.Response
	c.await("a response to "+op, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		r, ok := c.responses[id]
		resp = r
		return ok
	})
	if resp.Error != nil {
		c.t.Fatalf("%s refused: %s (%s)", op, resp.Error.Message, resp.Error.Code)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			c.t.Fatalf("%s result: %v", op, err)
		}
	}
}

func (c *coordinator) received(sub [16]byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.stream[sub]...)
}

func (c *coordinator) wire() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.raw...)
}

// close is this coordinator going away: the socket is closed, which is what a
// coordinator being killed looks like from the helper's side.
func (c *coordinator) close() { _ = c.conn.Close() }

func subscriberBytes(t *testing.T, id proto.SubscriberID) [16]byte {
	t.Helper()
	raw, err := proto.SessionBytes(string(id))
	if err != nil {
		t.Fatalf("subscriber %q: %v", id, err)
	}
	return raw
}

func TestTheSessionOutlivesTheConnectionWhenTheConnectionIsASocket(t *testing.T) {
	d := startDaemon(t, session.Shell{Path: "/bin/sh", Args: []string{"-i"}})
	sub := proto.SubscriberID("0123456789abcdef0123456789abcdef")
	subRaw := subscriberBytes(t, sub)

	first := connect(t, d)
	var spawned proto.SpawnResult
	first.request(proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &spawned)
	handle := spawned.Entry.Session

	var attached proto.AttachResult
	first.request(proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: handle, Offset: 0, Fresh: true, RequestWrite: true,
	}, &attached)
	if !attached.Write.Granted {
		t.Fatalf("no write capability: %+v", attached.Write)
	}
	first.write(proto.TypeSessionData, proto.EncodeSessionFrame(proto.SessionFrame{
		Session:    mustSession(t, handle.Session),
		Subscriber: subRaw,
		Epoch:      attached.Write.Epoch,
		Payload:    []byte("i=0; while :; do i=$((i+1)); echo MARK$i; sleep 1; done\n"),
	}))
	first.await("the marker loop to start", func() bool {
		return bytes.Count(first.received(subRaw), []byte("MARK")) >= 3
	})

	// The connection dies. Nothing else does: this is one attachment ending
	// (D2), and the interval the session lives across opens here.
	first.close()

	second := connect(t, d)
	var inv proto.SessionsResult
	second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
	if len(inv.Sessions) != 1 {
		t.Fatalf("the inventory holds %d sessions after the connection died, want 1", len(inv.Sessions))
	}
	survivor := inv.Sessions[0]
	if survivor.Session != handle {
		t.Fatalf("the handle changed across the connection: %+v want %+v", survivor.Session, handle)
	}
	if survivor.Exit != nil {
		t.Fatalf("the session died with its connection: %+v", survivor.Exit)
	}
	// The write capability the dead connection held is released, and it is
	// released BY the connection ending rather than by anybody asking. The
	// wait is on that fact arriving, never on a duration: the accept loop's
	// handler releases as it returns, which is concurrent with this
	// connection's requests by construction.
	for survivor.Writer != nil {
		if t.Context().Err() != nil {
			t.Fatalf("the dead connection still holds the write capability: %v", survivor.Writer)
		}
		second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
		survivor = inv.Sessions[0]
	}

	// And the process RAN across the gap: the window grew while no connection
	// existed at all. An observable state, not an elapsed time.
	was := survivor.Window.Written
	for {
		second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
		if inv.Sessions[0].Window.Written > was {
			break
		}
		if t.Context().Err() != nil {
			t.Fatalf("the stream never grew past %d after the connection died", was)
		}
	}
}

func TestASecondCoordinatorReachesASessionTheFirstOneOpened(t *testing.T) {
	// D12, frozen deliberately: any nocx running under that Unix account may
	// connect to the helper. Both connections are open at once here — the
	// second is asserted to be SERVED, not refused.
	d := startDaemon(t, session.Shell{Path: "/bin/sh", Args: []string{"-i"}})

	first := connect(t, d)
	var spawned proto.SpawnResult
	first.request(proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &spawned)
	handle := spawned.Entry.Session

	second := connect(t, d)
	var inv proto.SessionsResult
	second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session != handle {
		t.Fatalf("the second connection does not see the first one's session: %+v", inv.Sessions)
	}

	// It attaches to it as its own subscriber, at its own cursor: the
	// identities the ABI froze are per subscriber, not per session (D8).
	other := proto.SubscriberID("fedcba9876543210fedcba9876543210")
	var attached proto.AttachResult
	second.request(proto.OpAttach, proto.AttachParams{
		Subscriber: other, Session: handle, Offset: 0, Fresh: true,
	}, &attached)

	// And the first is still there and still answering: a second coordinator
	// does not displace one.
	var still proto.SessionsResult
	first.request(proto.OpSessions, proto.SessionsParams{}, &still)
	if len(still.Sessions) != 1 || still.Sessions[0].Session != handle {
		t.Fatalf("the first connection lost its session to the second: %+v", still.Sessions)
	}
}

func TestTheBinaryPlaneIsNotRewrappedInJSONRPCOnTheSocket(t *testing.T) {
	// AD-1 is untouched and this is what says so out loud. The data plane
	// crossing the endpoint is RAW: the bytes appear on the wire contiguously,
	// exactly as the process produced them. JSON-RPC would have escaped the
	// quote and the ESC and base64 would have replaced the lot — either way
	// the literal run below could not be found in what crossed the socket.
	//
	// The "shell" is cat, so the bytes under test are the process's own output
	// and are not a shell's interpretation of anything.
	d := startDaemon(t, session.Shell{Path: lookPath(t, "cat")})
	sub := proto.SubscriberID("0123456789abcdef0123456789abcdef")
	subRaw := subscriberBytes(t, sub)

	c := connect(t, d)
	var spawned proto.SpawnResult
	c.request(proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &spawned)
	handle := spawned.Entry.Session

	var attached proto.AttachResult
	c.request(proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: handle, Offset: 0, Fresh: true, RequestWrite: true,
	}, &attached)
	if !attached.Write.Granted {
		t.Fatalf("no write capability: %+v", attached.Write)
	}

	// A quote and a backslash (JSON would escape them), an ESC (JSON would
	// turn it into ) and two bytes that are not valid UTF-8 at all
	// (JSON could not carry them in a string in any form).
	marker := []byte{'N', 'O', 'C', 'X', 0x1b, '"', '\\', 0xfe, 0xff, 'Z', 'Z'}
	c.write(proto.TypeSessionData, proto.EncodeSessionFrame(proto.SessionFrame{
		Session:    mustSession(t, handle.Session),
		Subscriber: subRaw,
		Epoch:      attached.Write.Epoch,
		Payload:    append(append([]byte(nil), marker...), '\n'),
	}))

	c.await("the marker to come back off the process", func() bool {
		return bytes.Contains(c.received(subRaw), marker)
	})
	if !bytes.Contains(c.wire(), marker) {
		t.Fatalf("the marker is not on the wire byte for byte: the binary plane was re-encoded on its way across the endpoint")
	}
}

// lookPath resolves a program rather than pinning /bin: this repo is
// developed on a machine whose /bin holds almost nothing, and the test is
// about the socket, not about where a distribution puts cat.
func lookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH: %v", name, err)
	}
	return p
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func mustSession(t *testing.T, hexID string) [16]byte {
	t.Helper()
	raw, err := proto.SessionBytes(hexID)
	if err != nil {
		t.Fatalf("session id %q: %v", hexID, err)
	}
	return raw
}

// discardLog is the logger these tests give the helper: the daemon's
// diagnostics are not what is under test, and stderr is not the wire here.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
