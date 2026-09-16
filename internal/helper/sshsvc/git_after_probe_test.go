//go:build nocx_local_ssh

package sshsvc_test

// nocx-xn63t.6.4: the bead's own traffic shape, reproduced at the ssh
// channel-proxying layer the bead named as the suspect (internal/helper/
// sshsvc, internal/ssh's pool) — and a RULING-OUT, not a reproduction. The
// bead measured, in the container: lease, uname, unlease, an sftp open/close
// for the install, then a git.open sent over the far helper's lane and never
// answered (git.open#51 sent at 09:58:02.864Z, nothing logged again until
// Playwright's 60s timeout tore the pane down at 09:59:01Z).
//
// This test drives exactly that sequence — a lease over the REAL pool
// (internal/ssh), released before an sftp open/close, then a real bridge
// process and a real git.open over the lane that follows — bounded by a
// channel select rather than a sleep (the wait ends on the Call returning or
// a generous timeout, never on a duration alone). IT PASSES: the sequence
// that hung in the container answers in well under a second here, which
// rules out the ssh channel-proxying layer and the pool's lease/release
// cycle as the cause — the same way internal/helper/client's own
// TestGitOpenOnALiveConnectionLeavesTheSessionsLifecycleChannelOpen ruled out
// host.Host/client.Client for the prior bead (nocx-xn63t.6.3). The actual
// reproduction — a git.open that is sent and never answered, bounded rather
// than hung — is internal/app's
// TestGitOpenNeverAnsweredIsBoundedRatherThanHungForever; this file is kept
// as evidence of what is NOT the cause, not as the fix's regression test.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git/hostsvc"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestGitOpenIsAnsweredAfterAProbeLeaseSharesTheConnection(t *testing.T) {
	bin, generation := builtHelperBinary(t)
	home := helperAccount(t)
	runHelperDaemon(t, bin, home, generation)

	f := newFixture(t, "pw", newSigner(t))
	// The lane's own exec (the bridge) runs for real, as does the probe's own
	// exec (uname): both ride the same fixture and the same pooled
	// connection, exactly as the bead's traffic did on the pane's local
	// helper.
	f.execRun = true
	f.execEnv = []string{"HOME=" + home}
	stand := newStand(t, &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The platform probe: lease the destination, run the named uname probe,
	// then release. leaseParams and laneParams both resolve the SAME
	// destination and identity (wantRef/"cred-1", password auth, "test"),
	// which is what makes this the same pool entry the lane below reuses —
	// the sequence the bead measured, not a coincidence of two unrelated
	// connections.
	lease, err := stand.client.AcquireProbeLease(ctx, leaseParams(t, f, true))
	if err != nil {
		t.Fatalf("acquire probe lease: %v", err)
	}
	if _, uerr := lease.Uname(ctx); uerr != nil {
		t.Fatalf("uname: %v", uerr)
	}
	if cerr := lease.Close(); cerr != nil {
		t.Fatalf("unlease: %v", cerr)
	}

	// The install's own channel: an sftp-kind ssh.open, immediately
	// ssh.close'd — the second half of the bead's traffic burst
	// ("lease, uname, unlease, open, close") before the far helper's lane
	// ever opens.
	install, err := stand.client.OpenChannel(ctx, sftpParams(t, f))
	if err != nil {
		t.Fatalf("open the install channel: %v", err)
	}
	if cerr := install.Close(); cerr != nil {
		t.Fatalf("close the install channel: %v", cerr)
	}

	// The far helper's own lane, over the same destination — the unlease
	// above released the pool's last reference, so this dials fresh onto the
	// entry the probe just gave up, exactly as openFarHelper does once the
	// probe and the install are done.
	params := laneParams(t, f)
	params.Machine.Dir = filepath.Dir(bin)
	params.Generation = generation

	lane, err := stand.client.OpenLane(ctx, params)
	if err != nil {
		t.Fatalf("open the lane: %v", err)
	}
	t.Cleanup(func() { _ = lane.Close() })

	helper, err := client.Dial(ctx, client.Config{
		Exec: lane, ExpectHash: string(generation), SentinelTTL: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial the far helper through the lane: %v", err)
	}
	t.Cleanup(func() { _ = helper.Close() })

	// git.open, over the far helper client — the exact call the bead's
	// git.open#51 was. The bound is on the CALL's own answer channel racing a
	// generous timeout: the defect this reproduces is "never answered", so a
	// hang here must fail by that timeout firing rather than by the test
	// itself hanging forever.
	type openResult struct {
		res hostsvc.OpenResult
		err error
	}
	done := make(chan openResult, 1)
	go func() {
		var res hostsvc.OpenResult
		callErr := helper.Call(ctx, "git", "open", hostsvc.OpenParams{Cwd: home}, &res)
		done <- openResult{res: res, err: callErr}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("git.open: %v", r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("git.open was never answered after the probe lease released the shared pooled connection")
	}
}
