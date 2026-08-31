package host_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// A generation is content-addressed and immutable and lingers for as long as
// it holds a session, so a coordinator NEWER than the helper it reached is the
// ordinary case rather than an edge one. Such a coordinator will send
// TypeSessionData at a generation whose session service does not exist yet
// (D15). What this generation must do with it is the same thing AD-1 already
// decided for its own reserved metadata msg-type: recognise the frame, log it,
// drop it — never spawn anything, never tear the connection down, and above
// all never treat it as garbage, because the decoder's resync walks forward
// one byte at a time and would then walk through a live PTY stream.
func TestAnUnservedSessionDataFrameIsDroppedAndTheConnectionSurvives(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", logger)
	h.Register(&fakeService{
		name: "stub",
		ops:  map[string]any{"ping": struct{}{}},
		callFn: func(context.Context, string, json.RawMessage) (any, error) {
			return "pong", nil
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	body := make([]byte, proto.SessionFrameHeaderLen+4)
	body[0] = 0xab                                     // a session id byte
	body[16] = 0xcd                                    // a subscriber id byte
	binary.BigEndian.PutUint64(body[32:40], 7)         // the writer's lease
	copy(body[proto.SessionFrameHeaderLen:], "ls\r\n") // raw PTY bytes
	writeFrame(t, inW, proto.TypeSessionData, body)

	// The connection is still serving: the request after the dropped frame is
	// answered. Waiting on the answer rather than on a duration is the point —
	// a host that tore the connection down produces no frame and the test's
	// own timeout reports it.
	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "stub", Op: "ping"}))
	resp := readResponse(t, outCh)
	if resp.ID != 1 || resp.Error != nil {
		t.Fatalf("want the request after the dropped data frame answered, got %+v", resp)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}

	// The drop is stated, not silent: a dropped PTY write is a fact somebody
	// debugging a mismatched generation needs.
	if !strings.Contains(logs.String(), "session data frame") {
		t.Fatalf("the dropped session data frame was not logged:\n%s", logs.String())
	}
	// And it was never resynced past. "decoder resync" is what the host logs
	// for garbage, and a data frame the type set does not know would produce
	// exactly that — through the PTY bytes themselves.
	if strings.Contains(logs.String(), "decoder resync") {
		t.Fatalf("a session data frame was scanned past as garbage:\n%s", logs.String())
	}
}

// TestASessionDataFrameReachesTheServiceThatOwnsTheName is the other side of
// the drop above, and it is the seam nocx-k6p18.3 cashed the reservation in
// for: with a session service registered, a data frame is ROUTED to it, whole
// — the same session, the same subscriber, the same lease epoch and the same
// bytes, unread on the way past (AD-6).
//
// It is asserted through the wire rather than by calling SessionData directly,
// because the thing that can be wrong is the routing: a host that decoded the
// frame and then dropped it anyway would pass a direct call and fail here.
func TestASessionDataFrameReachesTheServiceThatOwnsTheName(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())

	got := make(chan proto.SessionFrame, 1)
	connection := make(chan any, 1)
	h.Register(&dataPlaneService{
		fakeService: fakeService{name: proto.ServiceSession, ops: map[string]any{proto.OpSpawn: struct{}{}}},
		received:    got,
		connection:  connection,
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	want := proto.SessionFrame{
		Session:    [16]byte{0xab},
		Subscriber: [16]byte{0xcd},
		Epoch:      7,
		Payload:    []byte("ls\r\n"),
	}
	writeFrame(t, inW, proto.TypeSessionData, proto.EncodeSessionFrame(want))

	select {
	case f := <-got:
		if f.Session != want.Session || f.Subscriber != want.Subscriber || f.Epoch != want.Epoch {
			t.Fatalf("routed %+v, want %+v", f, want)
		}
		if string(f.Payload) != string(want.Payload) {
			t.Fatalf("payload = %q, want %q", f.Payload, want.Payload)
		}
	case <-t.Context().Done():
		t.Fatal("the data frame never reached the session service")
	}

	select {
	case conn := <-connection:
		if conn != h {
			t.Fatalf("data-plane context connection = %p, want this host %p", conn, h)
		}
	case <-t.Context().Done():
		t.Fatal("the data-plane frame carried no connection identity")
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestTheHelperCanWriteADataFrameBack closes the loop the other way: the
// helper's own output leaves as a TypeSessionData frame on the same wire, with
// the layout the ABI froze — which is what makes the encode half of the codec
// have a caller at all.
func TestTheHelperCanWriteADataFrameBack(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	if err := h.SendSessionData(proto.SessionFrame{
		Session: [16]byte{1}, Subscriber: [16]byte{2}, Payload: []byte("out"),
	}); err != nil {
		t.Fatalf("SendSessionData: %v", err)
	}
	f := readFrame(t, outCh)
	if f.ty != proto.TypeSessionData {
		t.Fatalf("frame type = %v, want TypeSessionData", f.ty)
	}
	decoded, err := proto.DecodeSessionFrame(f.payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Session != [16]byte{1} || decoded.Subscriber != [16]byte{2} || string(decoded.Payload) != "out" {
		t.Fatalf("decoded %+v", decoded)
	}
	// Helper→coordinator frames carry no lease: there is nothing to authorize
	// in that direction, and a non-zero epoch here would invite a reader to
	// check one.
	if decoded.Epoch != 0 {
		t.Errorf("epoch = %d on a helper→coordinator frame, want zero", decoded.Epoch)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestTheSessionServiceNameHasOneOwner — the name the ABI freezes, the name the
// host dispatches on and the name the service answers to are one constant, not
// three string literals that can drift apart while all three look right.
func TestTheSessionServiceNameHasOneOwner(t *testing.T) {
	if proto.ServiceSession != "session" {
		t.Fatalf("ServiceSession = %q: the frozen name changed", proto.ServiceSession)
	}
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	h.Register(&dataPlaneService{
		fakeService: fakeService{name: proto.ServiceSession, ops: map[string]any{proto.OpSpawn: struct{}{}}},
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("a second service claimed the session name")
		}
	}()
	h.Register(&dataPlaneService{
		fakeService: fakeService{name: proto.ServiceSession, ops: map[string]any{proto.OpSessions: struct{}{}}},
	})
}

// dataPlaneService is a fakeService that also implements host.DataPlane.
type dataPlaneService struct {
	fakeService
	received   chan proto.SessionFrame
	connection chan any
}

func (d *dataPlaneService) SessionData(ctx context.Context, f proto.SessionFrame) {
	if d.received != nil {
		select {
		case d.received <- f:
		default:
		}
	}
	if d.connection != nil {
		select {
		case d.connection <- host.ConnectionFrom(ctx):
		default:
		}
	}
}
