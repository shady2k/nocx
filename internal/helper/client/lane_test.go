package client_test

// The LANE carrier: an exec channel the HELPER opened, presented as the
// HelperConn Dial rides (nocx-50w7p.10).
//
// Two of its promises are about what the coordinator must NOT be able to say
// and about which of two nearly identical endings it is looking at, and both
// are driven here over a scripted peer rather than through a real helper —
// because a real helper cannot be asked to end its bridge with exit 43 on cue,
// and because that exit status is exactly the input under test.
//
// The exec itself — a session on a real ssh connection, running the command
// this repository installs — is internal/helper/sshsvc's lane test, against a
// real in-process ssh server. What is asserted HERE is the client's half: the
// command refusal, the exit status, and the transport it is not.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// laneTestHash is the generation the scripted helper claims in hello-ok. The
// client verifies it against what the caller expected (D21), so a lane whose
// handshake never completes still has to be a peer of the right build.
const laneTestHash = "testhash"

// laneLaneID is the channel id the scripted helper mints for the lane. One
// lane per peer, so a fixed value is a fact rather than a collision.
const laneLaneID = "0000000000000000000000000000000a"

// laneScript is what the scripted helper does with the lane it opens: the
// bytes it writes on it, and how the lane ENDS.
//
// The ending's TIMING is part of the script rather than a detail, and for a
// reason the first draft of this file learned: a lane ended the moment its open
// was answered has already ended by the time a caller dials it, so the caller's
// failure is a write to a closed stream and not the classification under test.
// A real bridge exits AFTER it is started and after the hello it is fed, which
// is what endOnHello reproduces.
type laneScript struct {
	greeting []byte
	// exit is the status the lane's process ends with, or nil for an end no
	// process reported — the transport-shaped ending.
	exit *int32
	// endOnHello delays that ending until the first frame arrives on the
	// channel, which is the ordering a Dial produces.
	endOnHello bool
	// notifyFirst sends the ending BEFORE the open's answer. No helper
	// produces that ordering; it exists so the WINDOW between the answer and
	// the caller's registration can be exercised deterministically, which the
	// real race inside it cannot be.
	notifyFirst bool
}

// lanePeer answers the hello and one ssh.lane, then runs the script.
func lanePeer(t *testing.T, script laneScript) (peer func(io.Reader, io.Writer) int, params *proto.LaneParams, ended chan struct{}) {
	t.Helper()
	params = &proto.LaneParams{}
	ended = make(chan struct{})
	return func(in io.Reader, out io.Writer) int {
		var sendEnd func()
		endOnce := func() {
			if sendEnd == nil {
				return
			}
			f := sendEnd
			sendEnd = nil
			f()
		}
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch ty {
			case proto.TypeHello:
				var h proto.Hello
				_ = json.Unmarshal(payload, &h)
				_, _ = io.WriteString(out, "nocx-helper "+proto.Version+" ready\n")
				ok, _ := json.Marshal(proto.HelloOK{
					Version: proto.Version, Nonce: h.Nonce,
					ContentHash: laneTestHash, InstanceID: "instance-1",
				})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, ok))
			case proto.TypeChannelData:
				// The lane's first bytes, which in a Dial is its hello: the
				// bridge is running and this is where a real one fails.
				endOnce()
			case proto.TypeRequest:
				var req proto.Request
				if err := json.Unmarshal(payload, &req); err != nil {
					return
				}
				if req.Op != proto.OpLane {
					// The close: answered so a caller's release does not wait
					// out its own bound, and then the peer is done.
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: json.RawMessage("{}")})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				if err := json.Unmarshal(req.Params, params); err != nil {
					t.Errorf("the lane params off the wire did not decode: %v", err)
					return
				}
				channel := mustChannelID(t, laneLaneID)
				result, _ := json.Marshal(proto.OpenChannelResult{Channel: channel})
				resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
				if script.notifyFirst {
					// The end first, then the greeting, then the answer: the
					// end has to arrive while the caller is still waiting for
					// its open, which is the window under test.
					_, _ = out.Write(proto.EncodeFrame(proto.TypeNotify, 0, 0, mustNotify(t, channel, script.exit)))
					if len(script.greeting) > 0 {
						_, _ = out.Write(proto.EncodeFrame(proto.TypeChannelData, 0, 0,
							proto.EncodeChannelFrame(proto.ChannelFrame{Channel: channel, Payload: script.greeting})))
					}
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				if len(script.greeting) > 0 {
					_, _ = out.Write(proto.EncodeFrame(proto.TypeChannelData, 0, 0,
						proto.EncodeChannelFrame(proto.ChannelFrame{Channel: channel, Payload: script.greeting})))
				}
				sendEnd = func() {
					_, _ = out.Write(proto.EncodeFrame(proto.TypeNotify, 0, 0, mustNotify(t, channel, script.exit)))
					close(ended)
				}
				if !script.endOnHello {
					endOnce()
				}
			}
		}, func(int) {})
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if ferr := dec.Feed(buf[:n]); ferr != nil {
					return 0
				}
			}
			if err != nil {
				return 0
			}
		}
	}, params, ended
}

// mustNotify marshals one ssh.channel-closed notification.
func mustNotify(t *testing.T, channel proto.ChannelID, exit *int32) []byte {
	t.Helper()
	raw, _ := json.Marshal(proto.Notification{
		Service: proto.ServiceSSH,
		Event:   proto.EventChannelClosed,
		Params:  mustRaw(t, proto.ChannelClosedEvent{Channel: channel, Exit: exit}),
	})
	return raw
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// laneParams is what a caller names: which machine's install, and which build
// of it. There is no command field — see TestALaneRefusesTheCoordinatorsCommand
// for what happens when a caller brings one anyway.
func laneParams() proto.LaneParams {
	return proto.LaneParams{
		Destination: proto.SSHDestination{
			Host: "host.example.com", Port: 22, User: "deploy",
			Identity: proto.SSHIdentity{
				Credential: &proto.SSHCredential{Ref: "cred-1"},
				Auth:       proto.SSHAuthPassword,
			},
		},
		Machine:    proto.Machine{Dir: "/home/u/.nocx/helper/7-linux-amd64-" + strings.Repeat("a", 64)},
		Generation: proto.GenerationID(strings.Repeat("a", 64)),
	}
}

// openLaneOver brings a helper connection up over a scripted peer and asks it
// for one lane. It is the production chain in miniature: the helper connection
// is the carrier, and the lane is a channel on it.
func openLaneOver(t *testing.T, peer func(io.Reader, io.Writer) int) *client.LaneConn {
	t.Helper()
	helper := newFakeConn(peer)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: helper, ExpectHash: laneTestHash, SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial the helper: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	lane, err := c.OpenLane(context.Background(), laneParams())
	if err != nil {
		t.Fatalf("open lane: %v", err)
	}
	t.Cleanup(func() { _ = lane.Close() })
	return lane
}

// TestALaneRefusesTheCoordinatorsCommand is D3 at the one place a caller could
// otherwise still reach a command line on somebody else's machine.
//
// The helper builds `<dir>/nocx-helper bridge <generation>` from the two facts
// the params carry; a caller that arrives with a command of its own has
// confused a lane with the exec session it replaced, and a silent no-op would
// launch whatever this process happened to name — the wrong build, or something
// that is not a helper at all — and look like it worked.
func TestALaneRefusesTheCoordinatorsCommand(t *testing.T) {
	peer, _, _ := lanePeer(t, laneScript{exit: int32Ptr(0)})
	lane := openLaneOver(t, peer)

	// The empty command is the no-op it must be: it is what a caller that
	// names nothing sends (client.Dial's Config leaves Command empty for a
	// helper-opened lane), and refusing it would break every lane.
	if err := lane.Start(""); err != nil {
		t.Fatalf("Start(\"\") = %v, want the no-op a helper-started lane needs", err)
	}
	err := lane.Start("/home/u/.nocx/helper/7-linux-amd64-" + strings.Repeat("a", 64) + " bridge " + strings.Repeat("a", 64))
	if !errors.Is(err, client.ErrNoCommandOnALane) {
		t.Fatalf("Start(a command) = %v, want ErrNoCommandOnALane", err)
	}

	// And the same refusal reaches a caller that goes through Dial with a
	// command set — the shape of a coordinator that has not been migrated: it
	// launches nothing, and it is not reported as a lost connection either.
	if _, err := client.Dial(context.Background(), client.Config{
		Exec: lane, Command: "anything at all", ExpectHash: laneTestHash,
	}); !errors.Is(err, client.ErrExecForbidden) {
		t.Fatalf("Dial with a command over an open lane = %v, want ErrExecForbidden", err)
	}
}

// TestABridgeThatExitsIsClassifiedByItsStatusNotAsLoss is the exit status's
// whole reason for crossing the wire.
//
// A bridge that ends before the handshake's sentinel is how a host with no
// helper serving that generation reports itself, and the coordinator's own
// classification reads it from the exit code: 43 is ErrHelperNotServing, whose
// sentence is "no helper is running there" and whose recovery is different
// from every other pre-sentinel ending. Reported as a lost transport instead —
// which is what a lane that dropped the status would produce — it becomes a
// connection error, is retried, and tells a person nothing true.
func TestABridgeThatExitsIsClassifiedByItsStatusNotAsLoss(t *testing.T) {
	peer, _, ended := lanePeer(t, laneScript{exit: int32Ptr(exitNoEndpointCode()), endOnHello: true})
	lane := openLaneOver(t, peer)

	if _, err := client.Dial(context.Background(), client.Config{
		Exec: lane, ExpectHash: laneTestHash, SentinelTTL: 5 * time.Second,
	}); !errors.Is(err, client.ErrHelperNotServing) {
		t.Fatalf("Dial over a bridge that exited 43 = %v, want ErrHelperNotServing", err)
	}

	// The two facts the classification is built from, asserted directly: the
	// status is the process's, and the transport was never reported gone.
	<-ended
	code, err := lane.Wait()
	if err != nil || int32(code) != exitNoEndpointCode() { //nolint:gosec // an exit status is 0-255
		t.Fatalf("Wait() = (%d, %v), want (%d, nil)", code, err, exitNoEndpointCode())
	}
	select {
	case <-lane.Done():
		t.Fatal("Done closed for a lane whose PROCESS exited: a bridge that exits with a status is not a lost transport")
	default:
	}
}

// TestALaneWhoseTransportDiedIsNotAnExit is the other half of the same
// distinction, and it is the half that must still fail: a lane whose channel
// ended with NO exit status is a transport that went, which is the retryable
// class (ErrLost) and not a fact about the far host.
func TestALaneWhoseTransportDiedIsNotAnExit(t *testing.T) {
	// endOnHello, so the lane is REGISTERED before it ends: this is the shape
	// the product meets (the bridge is started, takes the hello, and then dies
	// with no status), and it is the one whose classification is under test
	// here. A lane that ended before its caller claimed it is a different case
	// and has its own test below — the parking one — because there the fact
	// itself has to survive the round trip.
	peer, _, ended := lanePeer(t, laneScript{endOnHello: true})
	lane := openLaneOver(t, peer)

	if _, err := client.Dial(context.Background(), client.Config{
		Exec: lane, ExpectHash: laneTestHash, SentinelTTL: 5 * time.Second,
	}); !errors.Is(err, client.ErrLost) {
		t.Fatalf("Dial over a lane that ended with no exit status = %v, want ErrLost", err)
	}
	<-ended
	select {
	case <-lane.Done():
	default:
		t.Fatal("Done stayed open for a lane whose end reported no process: the transport is gone and the client's dial watches exactly this")
	}
	if lane.LostErr() == nil {
		t.Fatal("LostErr is nil after the transport went, so a caller could not say why")
	}
}

// TestALaneCarriesItsBytesAndItsParams proves the plumbing the two tests above
// take for granted: the greeting the helper wrote on the channel arrives on the
// lane's stdout, and what crossed was the typed request — machine, generation
// and destination — with no command anywhere in it.
func TestALaneCarriesItsBytesAndItsParams(t *testing.T) {
	const greeting = "the first frame protocol bytes"
	peer, params, _ := lanePeer(t, laneScript{greeting: []byte(greeting), exit: int32Ptr(0)})
	lane := openLaneOver(t, peer)

	got := make([]byte, len(greeting))
	if _, err := io.ReadFull(lane.Stdout(), got); err != nil {
		t.Fatalf("read the lane: %v", err)
	}
	if string(got) != greeting {
		t.Fatalf("lane carried %q, want %q", got, greeting)
	}
	if lane.ID().IsZero() {
		t.Fatal("the lane has no channel id, so its bytes cannot be addressed")
	}
	want := laneParams()
	if !reflect.DeepEqual(*params, want) {
		t.Fatalf("the lane params off the wire = %+v, want %+v", *params, want)
	}
	// And there is no command among them, which the frozen schema states as
	// `additionalProperties: false` (contracts/helper/ssh.lane.params) rather
	// than as prose: a lane's whole surface is which machine and which build.
	raw, err := json.Marshal(*params)
	if err != nil {
		t.Fatalf("marshal the params: %v", err)
	}
	for _, forbidden := range []string{"command", "argv", "exec", "args"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Fatalf("the lane params carry %q: %s", forbidden, raw)
		}
	}
}

func int32Ptr(v int32) *int32 { return &v }

// exitNoEndpointCode is endpoint.ExitNoEndpoint as an exit status: 43 is the
// bridge's own code for "no helper is serving that generation", and the client
// is the party that reads it. Restated here rather than imported because this
// package may not reach internal/helper/endpoint (the constant's own guard
// lives in socket_test.go) — and a drift would be loud: the classification
// below asserts the specific code.
func exitNoEndpointCode() int32 { return 43 }

// TestAnEndThatArrivesBeforeTheClaimStillEndsTheStream is the parked end.
//
// A channel's end is announced by a NOTIFICATION, and the window between the
// helper's answer to the open and this side registering the id is the same
// window the data frames have: the answer wakes the CALLER goroutine, and the
// reader loop goes straight on to the next frame, so the two genuinely race.
// The bytes are parked for exactly that reason (channelData). An end that was
// dropped instead left a stream NOTHING would ever end — a lane's reader parked
// for ever and its handshake reporting a sentinel timeout for a fact that was
// already on the wire. That is not hypothetical: it is how
// TestALaneWhoseTransportDiedIsNotAnExit failed, once, under a loaded machine
// (nocx-50w7p.10).
//
// The ordering below is not one a helper produces — the helper writes nothing
// about a channel before its open is answered — and it is here because it is
// the same WINDOW, made deterministic: an end for an id nobody has registered.
func TestAnEndThatArrivesBeforeTheClaimStillEndsTheStream(t *testing.T) {
	const greeting = "the first bytes of a stream"
	peer, _, _ := lanePeer(t, laneScript{greeting: []byte(greeting), exit: int32Ptr(0), notifyFirst: true})
	lane := openLaneOver(t, peer)

	// The end is asserted FIRST, and under a bound of its own: a stream nothing
	// ends would park the read below until the test binary's timeout, and a
	// hang is a worse report than a sentence naming the defect.
	waited := make(chan int, 1)
	go func() {
		code, _ := lane.Wait()
		waited <- code
	}()
	select {
	case code := <-waited:
		if code != 0 {
			t.Fatalf("the parked end reported exit %d, want the 0 its process ended with", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the end never arrived: the notification was dropped for an id nobody had claimed, " +
			"so this stream will never end and its reader parks for ever")
	}

	// The greeting that was parked ahead of the end is still readable: the end
	// closes the stream, and Read drains what was already queued before it
	// reports it.
	got := make([]byte, len(greeting))
	if _, err := io.ReadFull(lane.Stdout(), got); err != nil {
		t.Fatalf("read the parked greeting: %v", err)
	}
	if string(got) != greeting {
		t.Fatalf("lane carried %q, want the parked %q", got, greeting)
	}
}
