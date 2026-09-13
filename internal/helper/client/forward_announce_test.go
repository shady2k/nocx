package client_test

// The listener's own ordering problem, which is the data plane's one op over.
//
// A channel's id is learned from the open's RESPONSE; a listener's is learned
// from `ssh.forward`'s response too, and the connection that arrives on it is
// announced right after. The helper orders its own writes (the accept loop
// starts in ResponseObserver, once the answer has gone out), and that is not
// enough on its own for exactly the reason channels_test.go gives: it
// guarantees the frame ORDER on the wire, not that the caller has registered
// the listener by the time the reader loop reaches the next frame.
//
// So the peer below writes the forward's answer, the announcement and the
// connection's first bytes back to back, and the assertion is that the
// connection is delivered with its bytes — because losing it costs the FIRST
// connection somebody made to a forward that is working perfectly, which for
// the remote lifecycle channel is the shell's one attempt to connect.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// announcedPeer answers the hello and `ssh.forward`, and on the forward writes
// the answer, the `forwarded-tcpip` announcement and the accepted connection's
// first bytes with nothing in between.
func announcedPeer(t *testing.T) func(io.Reader, io.Writer) int {
	t.Helper()
	return func(in io.Reader, out io.Writer) int {
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch ty {
			case proto.TypeHello:
				var h proto.Hello
				_ = json.Unmarshal(payload, &h)
				_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
				ok := proto.HelloOK{Version: proto.Version, Nonce: h.Nonce, ContentHash: testHash, InstanceID: "instance-1"}
				raw, _ := json.Marshal(ok)
				_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
			case proto.TypeRequest:
				var req proto.Request
				if err := json.Unmarshal(payload, &req); err != nil {
					return
				}
				if req.Service != proto.ServiceSSH {
					return
				}
				if req.Op != proto.OpForward {
					// Answered so a release is not left waiting on the bounded
					// close timeout: this test is about the announcement, and a
					// two-second cleanup would be paid by every run.
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: json.RawMessage("{}")})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				forward := mustForwardID(t, fmt.Sprintf("%032x", 1))
				channel := mustChannelID(t, fmt.Sprintf("%032x", 2))
				result, _ := json.Marshal(proto.ForwardResult{
					Forward: forward,
					Bind:    proto.ChannelTarget{Host: "127.0.0.1", Port: 41234},
				})
				resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				// Back to back, and deliberately: this is the race the client
				// has to survive, not an ordering it may assume away.
				event, _ := json.Marshal(proto.Notification{
					Service: proto.ServiceSSH,
					Event:   proto.EventForwardedTCPIP,
					Params: proto.ForwardedTCPIPEvent{
						Forward: forward,
						Channel: channel,
						Peer:    "127.0.0.1:52344",
					},
				})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeNotify, 0, 0, event))
				_, _ = out.Write(proto.EncodeFrame(proto.TypeChannelData, 0, 0,
					proto.EncodeChannelFrame(proto.ChannelFrame{
						Channel: channel,
						Payload: []byte(testGreeting),
					})))
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
}

func mustForwardID(t *testing.T, hexID string) proto.ForwardID {
	t.Helper()
	id, err := proto.ParseForwardID(hexID)
	if err != nil {
		t.Fatalf("forward id %q: %v", hexID, err)
	}
	return id
}

// TestAConnectionAnnouncedWithTheForwardsAnswerIsNotLost is the park's reason
// to exist on the listener side.
func TestAConnectionAnnouncedWithTheForwardsAnswerIsNotLost(t *testing.T) {
	conn := newFakeConn(announcedPeer(t))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: testHash, SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	fwd, err := c.OpenForward(context.Background(), proto.ForwardParams{
		Destination: proto.SSHDestination{
			Host: "host.example.com", Port: 22, User: "deploy",
			Identity: proto.SSHIdentity{
				Credential: &proto.SSHCredential{Ref: "cred-1"},
				Auth:       proto.SSHAuthPassword,
			},
		},
		Bind: proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
	})
	if err != nil {
		t.Fatalf("OpenForward: %v", err)
	}
	if got := fwd.Bind(); got.Host != "127.0.0.1" || got.Port != 41234 {
		t.Fatalf("Bind = %+v, want the address the server bound", got)
	}

	// The connection the peer announced BEFORE this caller could register the
	// listener must still be delivered, with its bytes.
	stream, err := acceptWithin(t, fwd, 10*time.Second)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer func() { _ = stream.Close() }()
	got := make([]byte, len(testGreeting))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("read the announced connection's first bytes: %v", err)
	}
	if string(got) != testGreeting {
		t.Fatalf("first bytes = %q, want %q", got, testGreeting)
	}
	if peer := fwd.Peer(stream); peer != "127.0.0.1:52344" {
		t.Fatalf("Peer = %q, want the address the helper announced", peer)
	}
}

// acceptWithin bounds a Forward.Accept: the announced connection either arrives
// or the test fails, and a hang would name no assertion.
func acceptWithin(t *testing.T, f *client.Forward, d time.Duration) (*client.ChannelStream, error) {
	t.Helper()
	type result struct {
		s   *client.ChannelStream
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := f.Accept()
		ch <- result{s: s, err: err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("no connection arrived within %s", d)
	}
}
