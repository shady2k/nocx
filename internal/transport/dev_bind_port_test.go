package transport

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// freePort asks the OS for a port and gives it straight back, so the number is
// one nothing holds at the moment it is returned. That is a race in principle
// and the only way to name a concrete port in a test; the window is the two
// statements below.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probing for a free port: %v", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("probe listener is %T, want *net.TCPAddr", l.Addr())
	}
	port := addr.Port
	if err := l.Close(); err != nil {
		t.Fatalf("releasing the probe: %v", err)
	}
	return port
}

func startOn(t *testing.T, port int) *WSServer {
	t.Helper()
	t.Setenv(devBindPortEnv, strconv.Itoa(port))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws
}

// THE ONE THAT MATTERS IN A SHIPPED BUILD. Without the nocx_dev_bind tag the
// variable is not read at all, so a process of the user setting it moves
// nothing — which is the whole of design §6's objection to an address that
// comes from the environment.
func TestDevBindPortIsIgnoredWithoutTheTag(t *testing.T) {
	if devBindEnabled {
		t.Skip("this build declares nocx_dev_bind; the paired test asserts the other half")
	}
	want := freePort(t)
	ws := startOn(t, want)
	if ws.Port() == want {
		t.Fatalf("a build without nocx_dev_bind bound the port the environment named (%d); "+
			"the environment must not reach the listener at all", want)
	}
}

// AND THE DEV STAND'S HALF: under the tag the number is honoured, so the ssh
// forward `make dev-web` prints survives a restart.
func TestDevBindPortIsHonouredUnderTheTag(t *testing.T) {
	if !devBindEnabled {
		t.Skip("build with -tags nocx_dev_bind to exercise this half")
	}
	want := freePort(t)
	ws := startOn(t, want)
	if ws.Port() != want {
		t.Fatalf("bound %d, want the %d the environment named", ws.Port(), want)
	}
}

// The host is never an input, under either build. §6's objection is not to a
// port number — it is to a coordinator that can be told to bind off loopback,
// and this is the assertion that says it cannot.
func TestDevBindNeverLeavesLoopback(t *testing.T) {
	ws := startOn(t, freePort(t))
	host, _, err := net.SplitHostPort(ws.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", ws.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("listening on %q, want 127.0.0.1", host)
	}
}

// A build carrying the tag but asked for nothing behaves exactly like a shipped
// one. This is the assertion the ten transport tests that start a second
// WSServer were making by accident, and it is worth making on purpose: a
// default port in the tag would be a default for EVERY listener in the process.
func TestDevBindPortUnsetLeavesTheChoiceToTheOS(t *testing.T) {
	if !devBindEnabled {
		t.Skip("build with -tags nocx_dev_bind to exercise this half")
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start with %s unset: %v", devBindPortEnv, err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	second := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	if err := second.Start(ctx); err != nil {
		t.Fatalf("a second server in the same process: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop(ctx) })
	if ws.Port() == second.Port() {
		t.Fatalf("both servers took port %d; neither asked for one", ws.Port())
	}
}

// A value that is set and unusable stops the server rather than sliding to an
// OS-chosen port: sliding is exactly the churn this tag removes, and it would
// do it silently.
func TestDevBindPortRefusesAnUnusableValue(t *testing.T) {
	if !devBindEnabled {
		t.Skip("build with -tags nocx_dev_bind to exercise this half")
	}
	for _, raw := range []string{"no-such-port", "0", "-1", "65536"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(devBindPortEnv, raw)
			ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
				WithVaultLifecycle(newFakeVaultLifecycle()))
			ctx := context.Background()
			if err := ws.Start(ctx); err == nil {
				_ = ws.Stop(ctx)
				t.Fatalf("Start accepted %s=%q", devBindPortEnv, raw)
			}
		})
	}
}

// A port somebody holds is refused rather than silently swapped for another:
// a dev stand that quietly moved would print an ssh line that is already wrong.
func TestDevBindPortRefusesAPortInUse(t *testing.T) {
	if !devBindEnabled {
		t.Skip("build with -tags nocx_dev_bind to exercise this half")
	}
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer func() { _ = held.Close() }()
	heldAddr, ok := held.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("held listener is %T, want *net.TCPAddr", held.Addr())
	}

	t.Setenv(devBindPortEnv, strconv.Itoa(heldAddr.Port))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err == nil {
		_ = ws.Stop(ctx)
		t.Fatal("Start accepted a port another listener holds; it must fail instead")
	}
}
