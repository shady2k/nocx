package session_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

const (
	helperExitSessionID  = "00112233445566778899aabbccddeeff"
	helperExitSubscriber = "ffeeddccbbaa99887766554433221100"
)

// helperExitConn is a pty-less helper lane whose peer speaks the real client
// framing. It sends the session.exit notification after the attach response,
// just as the helper does after recording the process status.
type helperExitConn struct {
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan struct{}
	peer   chan struct{}
	once   sync.Once
}

func newHelperExitConn(code int) *helperExitConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	c := &helperExitConn{
		stdin: toPeerW, stdout: fromPeerR,
		done: make(chan struct{}), peer: make(chan struct{}),
	}
	go func() {
		defer close(c.peer)
		defer fromPeerW.Close()
		answered := false
		decoder := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch {
			case ty == proto.TypeHello && !answered:
				answered = true
				var hello proto.Hello
				if json.Unmarshal(payload, &hello) != nil {
					return
				}
				_, _ = io.WriteString(fromPeerW, "nocx-helper "+proto.Version+" ready\n")
				raw, _ := json.Marshal(proto.HelloOK{
					Version: proto.Version, Nonce: hello.Nonce,
					ContentHash: "testhash", InstanceID: "instance-1",
				})
				_, _ = fromPeerW.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
			case ty == proto.TypeRequest:
				var req proto.Request
				if json.Unmarshal(payload, &req) != nil {
					return
				}
				var params proto.AttachParams
				if req.Op == proto.OpAttach {
					if json.Unmarshal(req.Params, &params) != nil {
						return
					}
					result, _ := json.Marshal(proto.AttachResult{
						Attachment:      "attachment-1",
						Resume:          proto.Resume{Resumed: true, From: params.Offset},
						LifecycleResume: proto.Resume{Resumed: true, From: params.LifecycleOffset},
					})
					response, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
					_, _ = fromPeerW.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, response))
					notification, _ := json.Marshal(proto.Notification{
						Service: proto.ServiceSession, Event: proto.EventSessionExit,
						Params: proto.SessionExit{
							Session: params.Session,
							Status:  proto.SessionExitStatus{Code: code, At: "2026-09-02T12:00:00.000000000Z"},
						},
					})
					_, _ = fromPeerW.Write(proto.EncodeFrame(proto.TypeNotify, 0, 0, notification))
				} else if req.Op == proto.OpDetach {
					response, _ := json.Marshal(proto.Response{ID: req.ID, Result: json.RawMessage(`{}`)})
					_, _ = fromPeerW.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, response))
				}
			}
		}, nil)
		buf := make([]byte, 32*1024)
		for {
			n, err := toPeerR.Read(buf)
			if n > 0 {
				_ = decoder.Feed(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *helperExitConn) Stdin() io.WriteCloser { return c.stdin }
func (c *helperExitConn) Stdout() io.Reader     { return c.stdout }
func (c *helperExitConn) Stderr() io.Reader     { return nil }
func (c *helperExitConn) Start(string) error    { return nil }
func (c *helperExitConn) Wait() (int, error) {
	<-c.peer
	return 0, nil
}
func (c *helperExitConn) Done() <-chan struct{} { return c.done }
func (c *helperExitConn) LostErr() error        { return nil }
func (c *helperExitConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		close(c.done)
	})
	return nil
}

// TestHelperExitOutcomeCarriesStatusThroughTheProductSeam drives a real
// AttachedSession through the same WaitErr seam session.ExitOutcome uses. The
// status must remain available after the attachment is closed; it is not a
// delivery-only side effect.
func TestHelperExitOutcomeCarriesStatusThroughTheProductSeam(t *testing.T) {
	conn := newHelperExitConn(7)
	defer conn.Close()
	attached, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash",
		SentinelTTL: time.Second, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer attached.Close()

	channel, err := attached.Attach(context.Background(), proto.AttachParams{
		Subscriber: helperExitSubscriber,
		Session:    proto.HostSessionID{Generation: "generation-1", Session: helperExitSessionID},
		Fresh:      true,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer channel.Close()

	select {
	case <-channel.Done():
	case <-time.After(time.Second):
		t.Fatal("helper exit notification did not close the attachment")
	}

	reg := session.New(log.NewSlogAdapter(nil), nil)
	adopted, err := reg.Adopt(session.Config{Kind: session.KindRemote, Cwd: "/"}, session.ID(helperExitSessionID), channel)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	cause, status := adopted.ExitOutcome()
	if cause != session.ExitExited || status != 7 {
		t.Fatalf("before close: outcome = (%q, %d), want (%q, 7)", cause, status, session.ExitExited)
	}

	if err := channel.Close(); err != nil {
		t.Fatalf("close attached session: %v", err)
	}
	cause, status = adopted.ExitOutcome()
	if cause != session.ExitExited || status != 7 {
		t.Fatalf("after close: outcome = (%q, %d), want (%q, 7)", cause, status, session.ExitExited)
	}
}
