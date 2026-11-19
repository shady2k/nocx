//go:build nocx_local_ssh

package sshsvc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshconn"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestHostKeyPinHandshakeIsTerminal(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	server := &gossh.ServerConfig{NoClientAuth: true}
	server.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			session, _, _, handshakeErr := gossh.NewServerConn(conn, server)
			if handshakeErr == nil {
				_ = session.Close()
			}
			_ = conn.Close()
		}
	}()
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("close listener: %v", closeErr)
		}
		<-done
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	innerCalled := false
	config := &gossh.ClientConfig{User: "test", HostKeyCallback: pinnedHostKey(func(string, net.Addr, gossh.PublicKey) error { innerCalled = true; return nil }, "SHA256:approved")}
	_, logger := logtest.New(t)
	client, err := ssh.NewReal(logger)
	if err != nil {
		t.Fatal(err)
	}
	clock := hostconntest.New()
	carrier := sshconn.NewWithOptions(client, logger, sshconn.Options{Clock: clock})
	spec := ssh.PooledSpec{Host: host, Port: port, User: "test", Identity: "pin-test", Config: config}
	var events []hostconn.Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	link, err := carrier.Dial(ctx, sshconn.PlanFor(spec), func(event hostconn.Event) { events = append(events, event) })
	if link != nil || err == nil {
		t.Fatalf("mismatched pin returned link=%v error=%v", link, err)
	}
	if innerCalled {
		t.Fatal("mismatched pin reached ordinary host trust")
	}
	var mismatch *ssh.ErrHostKeyPinMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("handshake lost the typed pin mismatch: %v", err)
	}
	want := hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseHostKeyChanged}
	if len(events) == 0 || events[len(events)-1] != want {
		t.Fatalf("handshake events=%v, want terminal event %v; error=%v", events, want, err)
	}
	var refusal *proto.Refusal
	if !errors.As(classifyChannelError(err), &refusal) || refusal.Code != proto.ErrCodeHostKeyPinMismatch {
		t.Fatalf("channel classification=%v", classifyChannelError(err))
	}
	var evidence proto.HostKeyEvidence
	if err := json.Unmarshal(refusal.Details, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Fingerprint != gossh.FingerprintSHA256(signer.PublicKey()) || evidence.Expected != "SHA256:approved" || evidence.Addr != listener.Addr().String() {
		t.Fatalf("pin evidence=%+v", evidence)
	}

	for _, policy := range []string{"auto", "manual"} {
		t.Run(policy, func(t *testing.T) {
			heldSpec := spec
			heldSpec.Identity = policy
			var attempts atomic.Int32
			heldSpec.Prepare = func(context.Context) (*gossh.ClientConfig, []ssh.PooledHop, error) {
				attempts.Add(1)
				return config, nil, nil
			}
			release := carrier.RetainConnection(heldSpec.Key(), policy)
			defer release()
			states := make(chan hostconn.State, 32)
			stop := carrier.WatchConnection(heldSpec.Key(), func(f hostconn.Facts) { states <- f.State })
			defer stop()
			lease, acquireErr := carrier.Acquire(ctx, heldSpec)
			if lease != nil || !errors.As(acquireErr, &mismatch) {
				t.Fatalf("Acquire lease=%v error=%v", lease, acquireErr)
			}
			var state hostconn.State
			for state.Kind != hostconn.Failed {
				select {
				case state = <-states:
				case <-ctx.Done():
					t.Fatalf("terminal state not observed: %v", state)
				}
			}
			if state.Cause != hostconn.CauseHostKeyChanged {
				t.Fatalf("retained %s state=%v", policy, state)
			}
			clock.Advance(24 * time.Hour)
			if attempts.Load() != 1 {
				t.Fatalf("unexpected attempt count after terminal refusal: %d", attempts.Load())
			}
			select {
			case unexpected := <-states:
				t.Fatalf("terminal pin refusal transitioned to %v", unexpected)
			default:
			}
		})
	}
}

func TestMatchingPinStillRequiresOrdinaryHostTrust(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := signer.PublicKey()
	for _, verdict := range []error{nil, &ssh.ErrUnknownHostKey{Addr: "host:22"}, &ssh.ErrHostKeyMismatch{Addr: "host:22"}} {
		called := false
		callback := pinnedHostKey(func(string, net.Addr, gossh.PublicKey) error { called = true; return verdict }, gossh.FingerprintSHA256(key))
		got := callback("host:22", &net.TCPAddr{}, key)
		if !called || got != verdict {
			t.Fatalf("matching pin: ordinary verdict=%v, got=%v, called=%v", verdict, got, called)
		}
	}
}
