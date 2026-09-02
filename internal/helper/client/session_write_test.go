package client_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The write half of the data plane, and the half no test had ever called.
//
// Every other test that exercises a TypeSessionData frame builds the outer
// envelope by hand, so all of them encode the author's model of the wire
// rather than the code's. AttachedSession.Write wrote the INNER session frame
// straight onto the lane with no envelope at all, and the helper's decoder met
// a keystroke beginning with the session id's first random byte: valid as a
// type byte about ten times in 256, and then a length read out of the middle
// of the id. The keystroke was discarded a byte at a time and, when the prefix
// happened to be plausible, so was the next genuine frame behind it.
//
// The invariant this pins is a span, not a moment: from the first Write until
// the attachment is closed, every byte this method puts on the lane is inside
// exactly one TypeSessionData envelope, and nothing else is on the lane
// between them. The decoder is what asserts it — a frame that is not framed is
// not a frame the peer's decoder can see at all, so counting what the peer
// decoded counts exactly the bytes that arrived as frames, and the resync
// counter counts the bytes that did not.
func TestAttachedSessionWriteRidesInsideASessionDataEnvelope(t *testing.T) {
	const (
		sessionHex    = "0123456789abcdef0123456789abcdef"
		subscriberHex = "fedcba9876543210fedcba9876543210"
		grantedEpoch  = proto.LeaseEpoch(7)
	)

	var mu sync.Mutex
	var dataFrames []proto.SessionFrame
	resynced := 0

	peer := func(in io.Reader, out io.Writer) int {
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch ty {
			case proto.TypeHello:
				var h proto.Hello
				_ = json.Unmarshal(payload, &h)
				_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
				raw, _ := json.Marshal(proto.HelloOK{
					Version: proto.Version, Nonce: h.Nonce,
					ContentHash: "testhash", InstanceID: "instance-1",
				})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
			case proto.TypeRequest:
				var req proto.Request
				_ = json.Unmarshal(payload, &req)
				result, _ := json.Marshal(proto.AttachResult{
					Attachment:      "attachment-1",
					Resume:          proto.Resume{Resumed: true},
					LifecycleResume: proto.Resume{Resumed: true},
					Write:           proto.WriteGrant{Granted: true, Epoch: grantedEpoch},
				})
				resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
			case proto.TypeSessionData:
				f, err := proto.DecodeSessionFrame(payload)
				if err != nil {
					return
				}
				mu.Lock()
				dataFrames = append(dataFrames, f)
				mu.Unlock()
			}
		}, func(n int) {
			mu.Lock()
			resynced += n
			mu.Unlock()
		})
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
		SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber:   proto.SubscriberID(subscriberHex),
		Session:      proto.HostSessionID{Generation: "testhash", Session: sessionHex},
		Fresh:        true,
		RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	typed := []byte("echo hello\r")
	n, err := attached.Write(typed)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(typed) {
		t.Fatalf("Write reported %d bytes, want %d", n, len(typed))
	}

	// An observable state change, not a duration: a request sent AFTER the
	// keystroke is answered only if the lane in front of it was frames. With
	// the envelope missing, the decoder is mid-resync when this request
	// arrives and eats into it, so the answer never comes.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if resizeErr := attached.Resize(ctx, 100, 30, 0, 0); resizeErr != nil {
		t.Fatalf("a request sent after the keystroke was not answered: %v", resizeErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if resynced != 0 {
		t.Fatalf("the peer's decoder resynced past %d unframed bytes", resynced)
	}
	if len(dataFrames) != 1 {
		t.Fatalf("the peer decoded %d session data frames, want exactly 1", len(dataFrames))
	}
	f := dataFrames[0]
	wantSession, err := proto.SessionBytes(sessionHex)
	if err != nil {
		t.Fatalf("session bytes: %v", err)
	}
	if f.Session != wantSession {
		t.Errorf("frame session = %x, want %x", f.Session, wantSession)
	}
	subscriberRaw, err := hex.DecodeString(subscriberHex)
	if err != nil {
		t.Fatalf("subscriber bytes: %v", err)
	}
	var wantSubscriber [16]byte
	copy(wantSubscriber[:], subscriberRaw)
	if f.Subscriber != wantSubscriber {
		t.Errorf("frame subscriber = %x, want %x", f.Subscriber, wantSubscriber)
	}
	if f.Epoch != grantedEpoch {
		t.Errorf("frame epoch = %d, want the granted write epoch %d", f.Epoch, grantedEpoch)
	}
	if string(f.Payload) != string(typed) {
		t.Errorf("frame payload = %q, want %q", f.Payload, typed)
	}
}

// The same defect at the seam a person actually reaches: the bead's own
// reproduction is "open a tab on a host where the helper is installed and type
// anything", and its symptom is that output renders and the keystroke does
// nothing. This drives the REAL helper session service over the REAL socket
// with a REAL shell behind it, so what it asserts is that the shell received
// what was typed — not that two encoders agree.
func TestWhatAUserTypesReachesTheHelperHostedShell(t *testing.T) {
	c := hostedSessions(t)

	entry, err := c.Spawn(context.Background(), proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "0123456789abcdef0123456789abcdef",
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: true, RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = attached.Close() }()

	if _, err := attached.Write([]byte("printf 'nocx-typed-this\\n'\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait on an observable state change — the shell's own output — with a
	// deadline that exists only to bound the failing direction.
	found := make(chan struct{})
	go func() {
		var seen []byte
		buf := make([]byte, 4096)
		for {
			n, readErr := attached.Read(buf)
			if n > 0 {
				seen = append(seen, buf[:n]...)
				if bytes.Contains(seen, []byte("nocx-typed-this")) {
					close(found)
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(20 * time.Second):
		t.Fatal("the shell never ran what was typed: the keystroke did not reach the helper-hosted PTY")
	}
}
