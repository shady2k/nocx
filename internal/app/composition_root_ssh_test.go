package app

// THE COORDINATOR'S REGISTRY HOLDS NO SSH FACTORY (nocx-ygxjv.13, ADR-0057,
// nocx-50w7p.5).
//
// cmd/nocx-server/dependency_test.go proves the SOURCE half of this claim: no
// file the shipped binary links calls a dial primitive or holds a live
// connection. It says so itself that the BEHAVIOURAL half — an open that
// reaches the real composition root and finds no route to a far host — is
// owed and does not live there.
//
// This is that other half, asked the direct way rather than through the
// transport's own refusal (session_open.go, asserted at the wire by
// internal/transport's TestAnSSHOpenWithNoHelperOpenerIsANamedRefusal): build
// the app exactly as `New` builds it for this machine, then ask its
// session.Reg — the same *session.Reg the WebSocket handler opens panes on,
// App.Session — to open a KindRemote session. The registry's own nil check
// (session.go's `if r.ssh == nil`) is the last line of defence if a future
// change ever re-wires a factory into `sessionOpener` while forgetting the
// registry, or if a caller reaches the registry directly (a backend RPC, a
// test, a future capability) rather than through session_open.go. Both
// checks stand for the reason WithSSHFactory's own doc gives: the registry is
// a general one, kept as a seam for tests, and nothing shipped supplies it a
// factory.
//
// `WithSSHFactory` has no production caller — grep across the module finds it
// called only from _test.go files, all of them either exercising a stand
// under the nocx_local_ssh tag (a developer's own machine, never the shipped
// build) or standing in for the helper route inside internal/transport's own
// test suite. `deadcode -tags gtk3,nocx_local_ssh -whylive
// 'github.com/shady2k/nocx/internal/session.Reg.WithSSHFactory' ./...`
// answers "reachable only through reflection" — RTA's name for a method
// nothing calls except through an interface value, which is exactly the seam
// doc above describes and not a production call edge.
import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestCompositionRoot_SessionRegistryHasNoSSHFactory builds the app the way
// New actually builds it — no test seam substitutes anything about the
// registry — and asks that same registry to open a remote session. If a
// later change wires WithSSHFactory back into the composition root (the
// wiring nocx-50w7p.5 deleted, per app.go's own comment beside sshClient),
// this open would stop failing with the registry's own refusal and this test
// would go red.
func TestCompositionRoot_SessionRegistryHasNoSSHFactory(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t, WithLogFilePath(filepath.Join(t.TempDir(), "nocx.log")))
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	if a.Session == nil {
		t.Fatal("the composition root built no session registry at all")
	}

	_, err = a.Session.Open(context.Background(), session.Config{
		Kind:   session.KindRemote,
		Host:   "unreachable.invalid",
		Remote: &ssh.ConnectConfig{User: "test"},
	})
	if err == nil {
		t.Fatal("a KindRemote Open on the shipped composition root's registry " +
			"succeeded: this process dialed a far host, which is the Tier A " +
			"fallback ADR-0057 refuses by name")
	}
	const want = "SSH sessions not available (no SSH factory wired)"
	if err.Error() != want {
		t.Fatalf("Open error = %q, want %q — a different refusal here means "+
			"something now stands between the registry and a real dial, but not "+
			"the absence of a factory itself", err.Error(), want)
	}
}
