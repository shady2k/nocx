//go:build nocx_local_ssh

package ssh

import (
	"testing"
	"time"
)

// TestArmKeepaliveHonoursTheFirstCallerThatActuallyWantsOne is
// nocx-y6fh7 item 6's own regression: AD-4 shares one pooled connection
// across every caller for a destination+identity, and dial order among them
// is NOT the same question as "which caller wants a prober". A helper-hosted
// pane's own shell channel races the shell-integration bundle's sftp
// publish for the SAME pooled connection, and the publish reliably wins
// (internal/app/helper_local.go's openSSH calls it BEFORE the spawn, by
// design — nocx-50w7p.21). Before armKeepalive existed, keepalive was
// started once, inside the dial closure, with whichever caller's spec
// happened to cause the cache miss — so an integrated pane's own
// KeepaliveInterval was silently discarded every time, which is exactly
// what ssh-reconnect.spec.ts:200 and :297 measured against a live helper.
//
// This proves the fix at the seam that owns it: a connection dialed by a
// caller with NO keepalive interest (interval 0, the shape a probe or a
// publish's acquirePooled call has) can still be armed AFTERWARDS by a
// caller that does want one, and the arming actually starts a prober.
func TestArmKeepaliveHonoursTheFirstCallerThatActuallyWantsOne(t *testing.T) {
	conn := &pooledSSHConn{client: &fakeClient{}}

	// The publish's own shape: it never asks for a prober at all.
	var neverCalled bool
	conn.armKeepalive(0, 0, func(Reachability) { neverCalled = true })

	// The pane's own shell channel arrives after, on the SAME connection
	// (this is exactly what a cache hit looks like from ArmKeepalive's
	// side — same *pooledSSHConn, a second, later call).
	reported := make(chan Reachability, 8)
	conn.armKeepalive(10*time.Millisecond, 2, func(r Reachability) {
		reported <- r
	})

	// fakeClient implements no SendRequest, so every probe fails immediately
	// with errNoGlobalRequests — an ordinary (not silent) failure, which
	// with countMax=2 reports "unresponsive" once before giving up.
	select {
	case r := <-reported:
		if r.Responsive {
			t.Fatalf("first report says responsive=true; want false against a client with no SendRequest")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("armKeepalive's prober never reported anything — the second caller's interval was not armed")
	}

	if neverCalled {
		t.Fatal("the FIRST caller's observer fired; it asked for no prober at all (interval 0) and must never be armed")
	}
}

// TestArmKeepaliveIsANoOpWithNoInterval proves the other half directly: a
// caller passing interval<=0 never claims the connection's one arming slot,
// which is what lets a later, real caller still win it.
func TestArmKeepaliveIsANoOpWithNoInterval(t *testing.T) {
	conn := &pooledSSHConn{client: &fakeClient{}}
	called := make(chan struct{}, 1)
	conn.armKeepalive(0, 0, func(Reachability) { called <- struct{}{} })
	select {
	case <-called:
		t.Fatal("armKeepalive with interval<=0 started a prober")
	case <-time.After(100 * time.Millisecond):
		// No report in a generous window: nothing was armed.
	}
}
