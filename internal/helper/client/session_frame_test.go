package client_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The mirror of the host's case, and it is needed for the same reason read the
// other way round: generations coexist for months, so a helper NEWER than the
// coordinator that reached it will send TypeSessionData to a client that has
// nowhere to route it yet. The client must recognise the frame and drop it.
// If it did not, the decoder would call the type byte garbage and scan forward
// one byte at a time — through the PTY bytes, and through the head of whatever
// frame followed them.
//
// The response arriving after the dropped frame is what proves the wire
// survived: an observable state change, not a duration.
func TestASessionDataFrameIsDroppedWithoutDisturbingTheWire(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// A peer that answers the handshake, then answers every request by
	// sending a session data frame FIRST and the real response after it.
	peer := func(in io.Reader, out io.Writer) int {
		answered := false
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch {
			case !answered && ty == proto.TypeHello:
				answered = true
				var h proto.Hello
				_ = json.Unmarshal(payload, &h)
				_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
				ok := proto.HelloOK{Version: proto.Version, Nonce: h.Nonce, ContentHash: "testhash", InstanceID: "instance-1"}
				raw, _ := json.Marshal(ok)
				_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
			case ty == proto.TypeRequest:
				var req proto.Request
				_ = json.Unmarshal(payload, &req)
				body := make([]byte, proto.SessionFrameHeaderLen+5)
				body[0] = 0x77
				body[16] = 0x88
				binary.BigEndian.PutUint64(body[32:40], 3)
				copy(body[proto.SessionFrameHeaderLen:], "\x1b[0m ")
				_, _ = out.Write(proto.EncodeFrame(proto.TypeSessionData, 0, 0, body))
				result, _ := json.Marshal("pong")
				resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
			}
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

	conn := newFakeConn(peer)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash",
		SentinelTTL: time.Second, Log: logger,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	var got string
	if err := c.Call(context.Background(), "stub", "ping", pingParams{Msg: "hi"}, &got); err != nil {
		t.Fatalf("Call after a session data frame: %v", err)
	}
	if got != "pong" {
		t.Fatalf("result = %q, want %q", got, "pong")
	}
	if !strings.Contains(logs.String(), "session data frame") {
		t.Fatalf("the dropped session data frame was not logged:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "decoder resync") {
		t.Fatalf("a session data frame was scanned past as garbage:\n%s", logs.String())
	}
}
