package client_test

// The probe lease and its named ops, driven over a scripted peer.
//
// Two of the promises here are about the WIRE rather than about a result, and a
// real helper cannot be asked to stop and show them: that no command crosses
// (the params of every probe op are a lease and typed arguments, and the command
// text exists only in the helper's build), and that a lease learns its transport
// died either from the coordinator's own connection dropping or from a probe
// that came back saying so.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/waittest"
)

// recorded is one request the peer saw: the op and its params, verbatim.
type recorded struct {
	op     string
	params string
}

// probePeer answers the lease and every named probe, and keeps what it was
// asked. failWith, when set, is the refusal code every probe op answers with —
// which is how a "the host refused exec" or "the lease is gone" answer is
// produced without a host.
type probePeer struct {
	mu       sync.Mutex
	seen     []recorded
	failWith string
	leaseHex string
	// hang stops the peer answering any probe op at all, which is the state a
	// caller's release has to survive.
	hang bool
}

func (p *probePeer) requests() []recorded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recorded(nil), p.seen...)
}

func (p *probePeer) peer(t *testing.T) func(io.Reader, io.Writer) int {
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
				p.mu.Lock()
				p.seen = append(p.seen, recorded{op: req.Op, params: string(req.Params)})
				failWith, hang := p.failWith, p.hang
				leaseHex := p.leaseHex
				p.mu.Unlock()
				if hang {
					return // withheld, deliberately
				}
				// The refusal applies to the PROBE ops: a lease that could not
				// be acquired would test a different path, and the case under
				// test is a probe that came back refused.
				if failWith != "" && req.Op != proto.OpLease && req.Op != proto.OpUnlease {
					errObj, _ := json.Marshal(proto.Response{ID: req.ID, Error: &proto.Error{Code: failWith, Message: "refused"}})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, errObj))
					return
				}
				switch req.Op {
				case proto.OpLease:
					result, _ := json.Marshal(proto.LeaseResult{
						Lease:              mustLeaseID(t, leaseHex),
						HostKeyFingerprint: "SHA256:recorded",
					})
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				case proto.OpUnlease:
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: json.RawMessage("{}")})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				default:
					result, _ := json.Marshal(proto.ProbeExecResult{Stdout: []byte("NOCX-PD/1\n"), ExitStatus: 0})
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				}
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

func mustLeaseID(t *testing.T, hexID string) proto.LeaseID {
	t.Helper()
	id, err := proto.ParseLeaseID(hexID)
	if err != nil {
		t.Fatalf("lease id %q: %v", hexID, err)
	}
	return id
}

const testLeaseHex = "0000000000000000000000000000000a"

func dialProbePeer(t *testing.T, p *probePeer) (*client.Client, *fakeConn) {
	t.Helper()
	if p.leaseHex == "" {
		p.leaseHex = testLeaseHex
	}
	conn := newFakeConn(p.peer(t))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: testHash, SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, conn
}

func acquireTestLease(t *testing.T, c *client.Client) *client.ProbeLease {
	t.Helper()
	lease, err := c.AcquireProbeLease(context.Background(), proto.LeaseParams{
		Destination: proto.SSHDestination{
			Host: "host.example.com", Port: 22, User: "deploy",
			Identity: proto.SSHIdentity{
				Credential: &proto.SSHCredential{Ref: "cred-1"},
				Auth:       proto.SSHAuthPassword,
			},
		},
		AcceptOnTrust: false,
	})
	if err != nil {
		t.Fatalf("AcquireProbeLease: %v", err)
	}
	return lease
}

// TestTheProbeOpsCarryNoCommandAndNoFreeFormArgument is the wire half of D3:
// every named probe's params are a lease id and TYPED arguments, and nothing in
// them is shell text.
//
// The completion probe is the case worth naming: it carries the user's line,
// and the assertion is that the line arrives as DATA — a JSON field — while the
// script, the heredoc and the quoting exist only in the helper's build. A
// reviewer reading this can see the difference between the two, which is the
// difference between a parameter and a command.
func TestTheProbeOpsCarryNoCommandAndNoFreeFormArgument(t *testing.T) {
	peer := &probePeer{}
	c, _ := dialProbePeer(t, peer)
	lease := acquireTestLease(t, c)
	ctx := context.Background()

	if _, err := lease.Uname(ctx); err != nil {
		t.Fatalf("uname: %v", err)
	}
	if _, err := lease.SamplePorts(ctx, remoteprobe.PortSS); err != nil {
		t.Fatalf("sample-ports: %v", err)
	}
	if _, err := lease.Completion(ctx, "/etc", "git commit -m 'a line'", 6, 20, "n1"); err != nil {
		t.Fatalf("completion: %v", err)
	}
	if _, err := lease.CommandNames(ctx, remoteprobe.CommandNamesProbe, "n2"); err != nil {
		t.Fatalf("command-names: %v", err)
	}

	seen := peer.requests()
	wantOps := []string{proto.OpLease, proto.OpUname, proto.OpSamplePorts, proto.OpCompletion, proto.OpCommandNames}
	if len(seen) != len(wantOps) {
		t.Fatalf("ops on the wire = %v, want %v", seen, wantOps)
	}
	for i, op := range wantOps {
		if seen[i].op != op {
			t.Fatalf("op %d = %q, want %q", i, seen[i].op, op)
		}
	}

	// Every params payload, character for character, for the two ops whose
	// shape is the whole claim: a lease and a member of a closed set, and a
	// lease plus typed completion arguments.
	if got, want := seen[1].params, `{"lease":"`+testLeaseHex+`"}`; got != want {
		t.Errorf("uname params = %s, want %s", got, want)
	}
	if got, want := seen[2].params, `{"lease":"`+testLeaseHex+`","probe":"ss"}`; got != want {
		t.Errorf("sample-ports params = %s, want %s", got, want)
	}
	// The completion line crosses as a FIELD. Nothing in the payload is the
	// script, the heredoc delimiter or a shell metacharacter standing alone.
	var completion struct {
		Lease string `json:"lease"`
		Cwd   string `json:"cwd"`
		Line  string `json:"line"`
		Pos   int    `json:"pos"`
		Limit int    `json:"limit"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(seen[3].params), &completion); err != nil {
		t.Fatalf("completion params: %v", err)
	}
	if completion.Line != "git commit -m 'a line'" || completion.Cwd != "/etc" || completion.Pos != 6 || completion.Limit != 20 {
		t.Errorf("completion params lost an argument: %+v", completion)
	}
	if strings.Contains(seen[3].params, "NOCXEOF_") || strings.Contains(seen[3].params, "bash -s") {
		t.Errorf("the completion payload carries shell text: %s", seen[3].params)
	}
	// And no payload anywhere names a probe COMMAND: the fixed words live in the
	// helper's build.
	for _, r := range seen {
		for _, word := range []string{"uname -s", "echo $HOME", "ss -H", "netstat -l", "lsof -nP", "stat -c", "sh -s", "bash -s", "NOCX-PD", "NOCX_CN", "NOCXEOF"} {
			if strings.Contains(r.params, word) {
				t.Errorf("op %s carries probe command text (%q): %s", r.op, word, r.params)
			}
		}
	}
}

// TestAProbeRefusalArrivesAsTheKindTheConsumersSwitchOn: the code on the wire
// is converted in ONE place, and the three consumers read its kinds.
func TestAProbeRefusalArrivesAsTheKindTheConsumersSwitchOn(t *testing.T) {
	for _, tc := range []struct {
		code string
		want remoteprobe.Kind
	}{
		{proto.ErrCodeExecSessionRefused, remoteprobe.KindSessionRefused},
		{proto.ErrCodeExecProhibited, remoteprobe.KindExecProhibited},
		{proto.ErrCodeExecTooLong, remoteprobe.KindCommandTooLong},
		{proto.ErrCodeExecLost, remoteprobe.KindConnectionLost},
	} {
		peer := &probePeer{failWith: tc.code}
		c, _ := dialProbePeer(t, peer)
		lease := acquireTestLease(t, c)

		_, err := lease.Uname(context.Background())
		var probeErr *remoteprobe.Error
		if !errors.As(err, &probeErr) {
			t.Fatalf("%s: err = %v (%T), want a remoteprobe.Error", tc.code, err, err)
		}
		if probeErr.Kind != tc.want {
			t.Errorf("%s mapped to %v, want %v", tc.code, probeErr.Kind, tc.want)
		}
	}
}

// TestALostLeaseClosesDoneAndSaysWhy: a consumer watching for a dead transport
// learns it here, and the cause names the transport rather than the host.
func TestALostLeaseClosesDoneAndSaysWhy(t *testing.T) {
	peer := &probePeer{failWith: proto.ErrCodeExecLost}
	c, _ := dialProbePeer(t, peer)
	lease := acquireTestLease(t, c)

	select {
	case <-lease.Done():
		t.Fatal("Done was already closed before the loss")
	default:
	}

	if _, err := lease.Uname(context.Background()); err == nil {
		t.Fatal("a lost lease answered a probe")
	}
	waittest.WaitFor(t, "Done to close after a lost probe", func() bool {
		select {
		case <-lease.Done():
			return true
		default:
			return false
		}
	})
	if lease.LostErr() == nil {
		t.Fatal("Done closed with no cause reported")
	}
}

// TestTheHelpersDeathEndsEveryLease: the coordinator's own connection dying is
// the other way a lease ends, and a consumer must not have to wait for its next
// probe (minutes away in discovery's cadence) to be told.
func TestTheHelpersDeathEndsEveryLease(t *testing.T) {
	peer := &probePeer{}
	c, conn := dialProbePeer(t, peer)
	lease := acquireTestLease(t, c)

	// The lane dying under the client, driven the way the read loop reports it.
	conn.lose(errors.New("the helper went away"))
	waittest.WaitFor(t, "every lease to end with the helper", func() bool {
		select {
		case <-lease.Done():
			return true
		default:
			return false
		}
	})
	if lease.LostErr() == nil {
		t.Fatal("the lease ended with no cause")
	}
}

// TestAReleasedLeaseRefusesProbesLocally: the caller's own act is not a fact
// about the host, and a probe after it never reaches the wire.
func TestAReleasedLeaseRefusesProbesLocally(t *testing.T) {
	peer := &probePeer{}
	c, _ := dialProbePeer(t, peer)
	lease := acquireTestLease(t, c)

	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := len(peer.requests())

	_, err := lease.Uname(context.Background())
	var probeErr *remoteprobe.Error
	if !errors.As(err, &probeErr) || probeErr.Kind != remoteprobe.KindLeaseClosed {
		t.Fatalf("probe after release = %v, want the lease-closed kind", err)
	}
	if got := len(peer.requests()); got != before {
		t.Fatalf("a probe on a released lease reached the helper (%d requests, want %d)", got, before)
	}

	// And the release itself went out, exactly once, with the right id.
	seen := peer.requests()
	last := seen[len(seen)-1]
	if last.op != proto.OpUnlease || last.params != `{"lease":"`+testLeaseHex+`"}` {
		t.Fatalf("last request = %s %s, want the unlease", last.op, last.params)
	}
}

// TestCloseDoesNotWaitForAHelperThatStoppedAnswering: Close runs in deferred
// cleanup, so a helper alive enough to hold the socket and not alive enough to
// dispatch must not turn a shutdown into a hang.
func TestCloseDoesNotWaitForAHelperThatStoppedAnswering(t *testing.T) {
	peer := &probePeer{}
	c, _ := dialProbePeer(t, peer)
	lease := acquireTestLease(t, c)

	peer.mu.Lock()
	peer.hang = true
	peer.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- lease.Close() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited for a helper that stopped answering")
	}
}
