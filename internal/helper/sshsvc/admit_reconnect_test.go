//go:build nocx_local_ssh

package sshsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

func TestHostKeyPinRefusedWarmLeaseDoesNotReplaceRetainedPlan(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	s := newStandLogging(t, &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()}, logtest.Slog(t))
	d := destinationAt(t, proxy.Addr())
	bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx := host.WithConnection(bounded, s.helper)
	holder, err := openPinnedLease(s, ctx, d, f.hostKeyFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	key := ssh.PooledSpec{Host: d.Host, Port: d.Port, User: d.User, Identity: wantRef}.Key()
	release := s.carrier.RetainConnection(key, "auto")
	defer release()
	_, err = openPinnedLease(s, ctx, d, gossh.FingerprintSHA256(newSigner(t).PublicKey()))
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != proto.ErrCodeHostKeyPinMismatch {
		t.Fatalf("refusal = %v", err)
	}
	facts := make(chan hostconn.Facts, 256)
	stop := s.carrier.WatchConnection(key, func(f hostconn.Facts) { facts <- f })
	defer stop()
	// Consume the initial observation before cutting the original transport.
	<-facts
	proxy.Cut()
	waittest.WaitFor(t, "a fresh authenticated connection after the rejected lease", func() bool { return f.connections() >= 2 })
	for {
		select {
		case got := <-facts:
			if got.State.Kind == hostconn.Failed {
				t.Fatalf("retained holder's retry was refused: %v", got.State)
			}
			if got.State.Kind == hostconn.Connected && f.connections() == 2 {
				// A matching acquisition proves the second connection is fully admitted.
				next, err := openPinnedLease(s, ctx, d, f.hostKeyFingerprint())
				if err != nil {
					t.Fatal(err)
				}
				defer next.Close()
				if f.connections() != 2 {
					t.Fatalf("connections = %d, want 2", f.connections())
				}
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestHostKeyPinColdRefusalPreservesManualReconnectPlan(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	s := newStandLogging(t, &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()}, logtest.Slog(t))
	d := destinationAt(t, f.addr)
	ctx := host.WithConnection(context.Background(), s.helper)
	holder, err := openPinnedLease(s, ctx, d, f.hostKeyFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	key := ssh.PooledSpec{Host: d.Host, Port: d.Port, User: d.User, Identity: wantRef}.Key()
	release := s.carrier.RetainConnection(key, "manual")
	defer release()
	if !s.carrier.Disconnect(key) {
		t.Fatal("original transport was not disconnected")
	}
	_, err = openPinnedLease(s, ctx, d, gossh.FingerprintSHA256(newSigner(t).PublicKey()))
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != proto.ErrCodeHostKeyPinMismatch {
		t.Fatalf("cold refusal = %v", err)
	}
	facts := make(chan hostconn.Facts, 256)
	stop := s.carrier.WatchConnection(key, func(f hostconn.Facts) { facts <- f })
	defer stop()
	initial := <-facts
	if initial.State.Kind != hostconn.Failed || initial.State.Cause != hostconn.CauseHostKeyChanged {
		t.Fatalf("cold refusal state = %v", initial.State)
	}
	if err := s.carrier.RegisterReconnect(key, initial.Seq); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "manual reconnect with the admitted holder's pin", func() bool {
		select {
		case fact := <-facts:
			if fact.State.Kind == hostconn.Failed {
				t.Fatalf("manual retry used the refused caller's pin: %v", fact.State)
			}
			return fact.State.Kind == hostconn.Connected
		default:
			return false
		}
	})
	if f.connections() != 3 {
		t.Fatalf("connections = %d, want original + refused handshake + manual retry", f.connections())
	}
}
