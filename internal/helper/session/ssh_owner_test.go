//go:build nocx_local_ssh

package session_test

// The owner's SSH acceptance tests (nocx-6q1uh.3, spec §5.2, §5.7), over the
// real harness in ssh_spawn_harness_test.go: a real in-process ssh server, a
// real helper host with both services, a real coordinator answering the
// credential and host-key asks, and a real sshProcess/ShellChannel/ConnPool
// underneath. Written test-first against owner_ssh.go, spawn_ssh.go,
// internal/ssh's pool.go/ssh_pooled.go and sshsvc/shell.go, and not run by
// this worker (the coordinator runs every suite once on the merged tree —
// AGENTS.md, "Git authority"); go vet and go build are what checked these
// compile and type-check.
//
// Two of this task's four acceptance tests live here (a real spawn's
// sshProcess is what must answer no_read_barrier and be detachable); the
// other two (the pool's own Taint/CloseTainted contract) are
// internal/ssh/pool_taint_test.go, which needs no real network at all.
//
// session.intent has no wire op yet (nocx-6q1uh.4/.5 build it) and no
// production caller passes stop a deadline yet (owner.go's own doc), so both
// tests reach the owner through owner_ssh_export_test.go's white-box bridge
// (package session, TestGrantControl/TestIncarnation/TestSubmitIntent/
// TestForceStop) rather than inventing a wire feature this task does not
// own. Everything else — spawning, attaching and writing — is the real wire.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TestAnSSHSessionRefusesAnIntentAndStillServesASnapshot is this task's
// no_read_barrier acceptance criterion: an intent on a REAL ssh session is
// refused with zero bytes ever reaching the far side, and a snapshot of the
// same session still succeeds — reads and snapshots are unaffected by a
// session having no read barrier (spec §5.2).
func TestAnSSHSessionRefusesAnIntentAndStillServesASnapshot(t *testing.T) {
	f := newSSHFixture(t, "pw", "cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))

	id := proto.HostSessionID{
		Generation: proto.GenerationID(entry.HostSessionID.Generation),
		Session:    entry.HostSessionID.Session,
	}

	ctrl, err := stand.sessions.TestGrantControl(id, sessionruntime.Principal{Kind: sessionruntime.PrincipalAgent, ID: "test-agent"})
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}
	at, err := stand.sessions.TestIncarnation(id)
	if err != nil {
		t.Fatalf("incarnation: %v", err)
	}
	intent := sessionruntime.Intent{
		At: at, Under: ctrl.Epoch, By: ctrl.Holder,
		Kind: sessionruntime.IntentKindKey, Payload: []byte("Enter"),
	}

	state, err := stand.sessions.TestSubmitIntent(id, intent)
	if state != sessionruntime.IntentStateRefused {
		t.Fatalf("intent state = %v, want refused", state)
	}
	if !errors.Is(err, session.ErrNoReadBarrier) {
		t.Fatalf("intent err = %v, want session.ErrNoReadBarrier", err)
	}

	// The far side received nothing: a refusal writes zero bytes, and this
	// is the ONLY assertion that can prove it — the state and error above
	// prove the owner's own decision, not what did or did not cross the
	// wire to the real ssh server.
	if got := f.programInputSeen(); len(got) != 0 {
		t.Fatalf("the far side's program received %q; a refused intent must write nothing", got)
	}

	// A snapshot of the same session still succeeds.
	if _, err := stand.client.Screen(context.Background(), entry.HostSessionID); err != nil {
		t.Fatalf("screen (a snapshot) after a refused intent: %v", err)
	}
}

// TestAWriterBlockedOnAZeroWindowIsDetachedAndTheSessionCloses is this
// task's detach acceptance criterion: the fixture's far side never adjusts
// its window (neverReadShellNum, ssh_spawn_harness_test.go — the peer's own
// gossh.Channel.Read is what drives an outgoing window-adjust, and this
// fixture never calls it), so a write larger than the initial grant
// (channelWindowSize, 2 MiB) blocks in golang.org/x/crypto/ssh's
// remoteWin.reserve — exactly the case with no per-channel interrupt (spec
// §5.7). A forced stop must detach rather than hang, and a SIBLING channel
// already open on the same pooled connection (same destination, same
// identity — AD-4's key) must be unaffected: Taint removes the pool's map
// entry, never the connection itself, while any reference is still live.
func TestAWriterBlockedOnAZeroWindowIsDetachedAndTheSessionCloses(t *testing.T) {
	f := newSSHFixture(t, "pw", "cat")
	f.neverReadShellNum = 1 // the FIRST shell request only — the sibling's is the second.
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	stuckEntry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	stuckID := proto.HostSessionID{
		Generation: proto.GenerationID(stuckEntry.HostSessionID.Generation),
		Session:    stuckEntry.HostSessionID.Session,
	}
	stuckAttached := stand.mustAttach(t, stuckEntry)
	t.Cleanup(func() { _ = stuckAttached.Close() })

	// The sibling: a second session dialed with the identical destination
	// and identity, so AD-4's pool key is the same and it shares the FIRST
	// session's pooled connection rather than dialing its own.
	siblingEntry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	siblingAttached := stand.mustAttach(t, siblingEntry)
	t.Cleanup(func() { _ = siblingAttached.Close() })

	if f.connections() != 1 {
		t.Fatalf("the fixture accepted %d TCP connections, want 1: the sibling must share the stuck session's pooled connection", f.connections())
	}

	// A write larger than the far side's one-time initial window grant.
	// The fixture never reads it, so this blocks in the real ssh library —
	// on the CLIENT'S OWN write goroutine (this owner's writer), never on
	// this test's own goroutine, hence sent from one.
	stuck := make([]byte, 4<<20)
	go func() { _, _ = stuckAttached.Write(stuck) }()

	tailLost, writerDetached, err := stand.sessions.TestForceStop(stuckID, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatalf("force stop: %v", err)
	}
	_ = tailLost
	if !writerDetached {
		t.Fatal("writerDetached = false, want true: a write stuck behind a peer that never adjusts its window must be detached, not joined")
	}

	// The sibling, on the SAME pooled connection, still round-trips a
	// write: Taint removed the pool's map entry, not the connection itself,
	// and the sibling's own reference was never touched.
	if _, err := siblingAttached.Write([]byte("still-alive\n")); err != nil {
		t.Fatalf("the sibling's write after the stuck session was detached: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		if got := f.programInputSeen(); len(got) > 0 {
			break
		}
		select {
		case <-f.changed:
		case <-deadline:
			t.Fatal("the sibling's write never reached the far side after the stuck session was detached")
		}
	}
}
