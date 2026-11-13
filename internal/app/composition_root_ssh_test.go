package app

// THE COORDINATOR'S REGISTRY CANNOT OPEN AN SSH SESSION AT ALL
// (nocx-50w7p.5, ADR-0057, nocx-oalo1.3.2).
//
// cmd/nocx-server/dependency_test.go proves the SOURCE half of this claim: no
// file the shipped binary links calls a dial primitive or holds a live
// connection. It says so itself that the BEHAVIOURAL half — an open that
// reaches the real composition root and finds no route to a far host — is owed
// and does not live there.
//
// This is that other half, and since nocx-oalo1.3.2 it is stronger than an
// absence: the registry's remote arm and its SSH factory seam are DELETED, so
// `Reg.Open` with KindRemote is a refusal by name — "opened by this machine's
// helper" — rather than a check against a nil field that a future wiring could
// satisfy. The transport already refuses an ssh destination no helper claimed
// (asserted at the wire by internal/transport's
// TestAnSSHOpenWithNoHelperOpenerIsANamedRefusal); this asks the registry
// directly, which is the door a backend RPC or a future capability would use.
import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestCompositionRoot_SessionRegistryOpensNoSSHSession builds the app the way
// New actually builds it — no test seam substitutes anything about the
// registry — and asks that same registry to open a remote session. If the
// deleted dial ever comes back, this open starts succeeding and the test goes
// red; if a different refusal replaces the named one, the second assertion says
// which.
func TestCompositionRoot_SessionRegistryOpensNoSSHSession(t *testing.T) {
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
	if !strings.Contains(err.Error(), "helper") {
		t.Fatalf("Open error = %q, want the refusal that names this machine's helper route", err.Error())
	}
	if got := len(a.Session.List()); got != 0 {
		t.Fatalf("a refused remote open registered %d session(s), want 0", got)
	}
}
