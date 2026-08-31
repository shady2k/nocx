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

// TestTheReservedSessionServiceNameHasOneOwner — the name the host refuses to
// register and the name the ABI freezes are the same constant, not two string
// literals that can drift apart while both look right.
func TestTheReservedSessionServiceNameHasOneOwner(t *testing.T) {
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want Register to panic for proto.ServiceSession")
		}
	}()
	h.Register(&fakeService{name: proto.ServiceSession})
}
