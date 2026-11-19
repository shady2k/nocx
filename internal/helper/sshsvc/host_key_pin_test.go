//go:build nocx_local_ssh

package sshsvc_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log/logtest"
	gossh "golang.org/x/crypto/ssh"
)

func openPinnedPane(s *stand, ctx context.Context, d proto.SSHDestination, pin string) (io.Closer, error) {
	return s.service.OpenPaneListeners(ctx, sshsvc.PaneSpec{
		Destination: d, HostKeyFingerprint: pin, Lifecycle: true,
	})
}

func openPinnedShell(s *stand, ctx context.Context, d proto.SSHDestination, pin string) (io.Closer, error) {
	return s.service.OpenShell(ctx, sshsvc.ShellSpec{
		Destination: d, HostKeyFingerprint: pin, Cols: 80, Rows: 24,
	})
}

func openPinnedLease(s *stand, ctx context.Context, d proto.SSHDestination, pin string) (io.Closer, error) {
	return s.client.AcquireProbeLease(ctx, proto.LeaseParams{
		Destination: d, HostKeyFingerprint: pin,
	})
}

func openPinnedChannel(s *stand, ctx context.Context, d proto.SSHDestination, pin string) (io.Closer, error) {
	return s.client.OpenChannel(ctx, proto.OpenChannelParams{
		Destination: d, HostKeyFingerprint: pin, Kind: proto.ChannelSFTP,
	})
}

func TestAWarmConnectionCannotBypassACallersHostKeyPin(t *testing.T) {
	for name, open := range map[string]func(*stand, context.Context, proto.SSHDestination, string) (io.Closer, error){
		"listeners": openPinnedPane,
		"shell":     openPinnedShell,
		"lease":     openPinnedLease,
		"channel":   openPinnedChannel,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "pw", newSigner(t))
			s := newStandLogging(t, &coordinator{
				password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
			}, logtest.Slog(t))
			holder := acquireLease(t, s, f)
			destination := leaseParams(t, f, false).Destination
			ctx := host.WithConnection(context.Background(), s.helper)
			wrongPin := gossh.FingerprintSHA256(newSigner(t).PublicKey())

			got, err := open(s, ctx, destination, wrongPin)
			if err == nil {
				if got != nil {
					_ = got.Close()
				}
				t.Fatal("a warm connection with another host key satisfied the caller's pin")
			}
			var refusal *proto.Refusal
			var wireRefusal *client.RefusalError
			code := ""
			switch {
			case errors.As(err, &refusal):
				code = refusal.Code
			case errors.As(err, &wireRefusal):
				code = wireRefusal.Code
			}
			if code != "host-key-pin-mismatch" {
				t.Fatalf("pin refusal = %v, want host-key-pin-mismatch", err)
			}
			if count := f.connections(); count != 1 {
				t.Fatalf("refusing a pin made %d connections, want the original connection only", count)
			}
			// A refused acquisition must not poison the transport another holder owns.
			valid, err := open(s, ctx, destination, f.hostKeyFingerprint())
			if err != nil {
				t.Fatalf("the original holder's transport no longer serves its real key: %v", err)
			}
			if err := valid.Close(); err != nil {
				t.Fatalf("close valid acquisition: %v", err)
			}
			if count := f.connections(); count != 1 {
				t.Fatalf("matching pin redialed: %d connections, want 1", count)
			}

			// With the original holder gone, no reference from the refused acquisition
			// may remain: the next lease must authenticate a fresh transport.
			if err := holder.Close(); err != nil {
				t.Fatalf("close original holder: %v", err)
			}
			next := acquireLease(t, s, f)
			defer func() { _ = next.Close() }()
			if count := f.connections(); count != 2 {
				t.Fatalf("after closing the original holder, %d connections, want 2 (refused acquisition leaked a reference)", count)
			}
		})
	}
}

func TestAMatchingPinReusesAWarmConnectionWithoutAHandshake(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	s := newStandLogging(t, &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, logtest.Slog(t))
	holder := acquireLease(t, s, f)
	defer func() { _ = holder.Close() }()
	destination := leaseParams(t, f, false).Destination
	ctx := host.WithConnection(context.Background(), s.helper)
	for _, open := range []func(*stand, context.Context, proto.SSHDestination, string) (io.Closer, error){openPinnedPane, openPinnedShell, openPinnedLease, openPinnedChannel} {
		got, err := open(s, ctx, destination, f.hostKeyFingerprint())
		if err != nil {
			t.Fatalf("open with the matching pin: %v", err)
		}
		defer func() { _ = got.Close() }()
	}
	if count := f.connections(); count != 1 {
		t.Fatalf("matching pin made %d connections, want 1", count)
	}
}

func TestAWarmLaneCannotBypassTheInstalledHelpersHostKeyPin(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	f.setExecHandler(func(string) (string, string, int, bool) { return "", "", 0, false })
	s := newStandLogging(t, &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, logtest.Slog(t))
	holder := acquireLease(t, s, f)
	defer func() { _ = holder.Close() }()
	params := laneParams(t, f)
	params.HostKeyFingerprint = gossh.FingerprintSHA256(newSigner(t).PublicKey())
	var opened proto.OpenChannelResult
	err := s.client.Call(context.Background(), proto.ServiceSSH, proto.OpLane, params, &opened)
	if err == nil {
		t.Fatal("a warm connection ran an installed helper despite the caller's mismatched pin")
	}
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != "host-key-pin-mismatch" {
		t.Fatalf("lane refusal = %v, want host-key-pin-mismatch", err)
	}
	if ran := f.ran(); len(ran) != 0 {
		t.Fatalf("a refused lane executed %v", ran)
	}
	params.HostKeyFingerprint = f.hostKeyFingerprint()
	valid, err := s.client.OpenLane(context.Background(), params)
	if err != nil {
		t.Fatalf("matching lane pin: %v", err)
	}
	if err := valid.Close(); err != nil {
		t.Fatalf("close matching lane: %v", err)
	}
	if count := f.connections(); count != 1 {
		t.Fatalf("lane pin check made %d connections, want 1", count)
	}
	if ran := f.ran(); len(ran) != 1 {
		t.Fatalf("matching lane ran %v, want one bridge", ran)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close original holder: %v", err)
	}
	next := acquireLease(t, s, f)
	defer func() { _ = next.Close() }()
	if count := f.connections(); count != 2 {
		t.Fatalf("after closing original holder, %d connections, want 2 (refused lane leaked a reference)", count)
	}
}
