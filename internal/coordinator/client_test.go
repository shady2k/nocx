package coordinator_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
)

// notifyStop is the child daemon's stop signal, named here so the child at
// the top of launcher_test.go reads as the daemon it stands in for.
func notifyStop(c chan<- os.Signal) {
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
}

// newRealClient is the production client pointed at a test's directory: the
// real dialer, the real socket, the real newline-delimited exchange.
func newRealClient(t *testing.T, dir string) *coordinator.Client {
	t.Helper()
	c, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: filepath.Join(dir, "srv.sock"),
		Self:   selfIdentity(),
		Dialer: coordinator.SystemDialer{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// --- the client, against a real daemon and against nothing ---------------

func TestClientReadsTheHelloOffARealSocket(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	got, err := newRealClient(t, dir).Hello(context.Background())
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if got.Hello.Build.Version != testVersion || got.Hello.Build.Commit != testCommit {
		t.Errorf("build = %+v, want %s/%s", got.Hello.Build, testVersion, testCommit)
	}
	if got.Hello.Protocol != coordinator.ProtocolVersion {
		t.Errorf("protocol = %d, want %d", got.Hello.Protocol, coordinator.ProtocolVersion)
	}
	if got.Hello.WSAddress != testAddr {
		t.Errorf("ws address = %q, want %q", got.Hello.WSAddress, testAddr)
	}
	if got.Hello.WSToken != testToken {
		t.Errorf("ws token = %q, want %q", got.Hello.WSToken, testToken)
	}
	// The pid the KERNEL reports for the process on the other end — this
	// process, since the server is in this test binary. It is what the
	// stopper aims at, so a client that could not read it could not refuse
	// an incompatible coordinator either.
	if got.PID != os.Getpid() {
		t.Errorf("peer pid = %d, want this process %d", got.PID, os.Getpid())
	}
	_ = s
}

func TestClientReportsNoCoordinatorWhenNothingIsServing(t *testing.T) {
	dir := shortDir(t)
	_, err := newRealClient(t, dir).Hello(context.Background())
	if !errors.Is(err, coordinator.ErrNoCoordinator) {
		t.Fatalf("Hello on an empty directory = %v, want ErrNoCoordinator", err)
	}
}

func TestClientReportsNoCoordinatorForASocketNobodyIsBehind(t *testing.T) {
	dir := shortDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A socket file left by a daemon that died: it exists, and connect(2)
	// refuses it. That is "no coordinator", not "a broken one".
	sock := filepath.Join(dir, "srv.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l.SetUnlinkOnClose(false)
	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	_, err = newRealClient(t, dir).Hello(context.Background())
	if !errors.Is(err, coordinator.ErrNoCoordinator) {
		t.Fatalf("Hello on a stale socket = %v, want ErrNoCoordinator", err)
	}
}

// failingDialer is the seam for "the dial itself failed for a reason that is
// not absence" — an EMFILE, a permission refusal on the parent directory.
type failingDialer struct{ err error }

func (d failingDialer) Dial(context.Context, string) (net.Conn, error) { return nil, d.err }

func TestClientReportsADialFailureThatIsNotAbsence(t *testing.T) {
	c, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: "/tmp/nocx-does-not-matter.sock",
		Self:   selfIdentity(),
		Dialer: failingDialer{err: syscall.EMFILE},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Hello(context.Background())
	if err == nil {
		t.Fatal("Hello succeeded although the dial failed")
	}
	if errors.Is(err, coordinator.ErrNoCoordinator) {
		t.Errorf("an EMFILE was reported as an absent coordinator: %v", err)
	}
	if !errors.Is(err, syscall.EMFILE) {
		t.Errorf("the failure does not carry the cause: %v", err)
	}
}

// scriptedConn is a connection whose answer a test writes.
type scriptedConn struct {
	net.Conn
	response    string
	writeErr    error
	deadlineErr error
	mu          sync.Mutex
	requested   []byte
	read        int
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	if c.read >= len(c.response) {
		return 0, io.EOF
	}
	n := copy(p, c.response[c.read:])
	c.read += n
	return n, nil
}

func (c *scriptedConn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.mu.Lock()
	c.requested = append(c.requested, p...)
	c.mu.Unlock()
	return len(p), nil
}

func (c *scriptedConn) Close() error { return nil }

func (c *scriptedConn) SetDeadline(time.Time) error { return c.deadlineErr }
func (c *scriptedConn) LocalAddr() net.Addr         { return nil }
func (c *scriptedConn) RemoteAddr() net.Addr        { return nil }

type scriptedDialer struct{ conn *scriptedConn }

func (d scriptedDialer) Dial(context.Context, string) (net.Conn, error) { return d.conn, nil }

func newScriptedClient(t *testing.T, conn *scriptedConn) *coordinator.Client {
	t.Helper()
	c, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: "/tmp/nocx-scripted.sock",
		Self:   selfIdentity(),
		Dialer: scriptedDialer{conn: conn},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestClientReportsARefusalFromTheDaemon(t *testing.T) {
	conn := &scriptedConn{response: `{"error":"peer uid 501 is not permitted"}` + "\n"}
	_, err := newScriptedClient(t, conn).Hello(context.Background())
	if err == nil {
		t.Fatal("Hello succeeded although the daemon refused it")
	}
	if !errors.Is(err, coordinator.ErrRefused) {
		t.Errorf("refusal = %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("the failure does not carry the daemon's reason: %v", err)
	}
}

func TestClientReportsAnUnreadableAnswer(t *testing.T) {
	conn := &scriptedConn{response: "this is not json\n"}
	_, err := newScriptedClient(t, conn).Hello(context.Background())
	if err == nil {
		t.Fatal("Hello accepted an answer that is not JSON")
	}
	if errors.Is(err, coordinator.ErrNoCoordinator) {
		t.Errorf("a garbled answer was reported as an absent coordinator: %v", err)
	}
}

func TestClientReportsAnEmptyAnswer(t *testing.T) {
	// A daemon that accepted the connection and then hung up: the response
	// carries neither a hello nor a reason, and reading it as an empty
	// hello would hand the renderer an empty address and an empty token.
	conn := &scriptedConn{response: "{}\n"}
	_, err := newScriptedClient(t, conn).Hello(context.Background())
	if err == nil {
		t.Fatal("Hello accepted a response with no payload and no error")
	}
}

func TestClientReportsAWriteFailure(t *testing.T) {
	conn := &scriptedConn{writeErr: syscall.EPIPE}
	_, err := newScriptedClient(t, conn).Hello(context.Background())
	if err == nil {
		t.Fatal("Hello succeeded although the request could not be written")
	}
	if !errors.Is(err, syscall.EPIPE) {
		t.Errorf("the failure does not carry the cause: %v", err)
	}
}

func TestClientStatesItsOwnIdentity(t *testing.T) {
	conn := &scriptedConn{response: `{"hello":{"build":{"version":"9.9.9","commit":"cafebabe"},` +
		`"protocol":1,"wsAddress":"127.0.0.1:1","wsToken":"t"}}` + "\n"}
	if _, err := newScriptedClient(t, conn).Hello(context.Background()); err != nil {
		t.Fatalf("Hello: %v", err)
	}
	conn.mu.Lock()
	sent := string(conn.requested)
	conn.mu.Unlock()
	// The handshake is symmetric by design: a launcher that could not state
	// its own version could not be told it is mismatched.
	for _, want := range []string{`"type":"hello"`, testVersion, testCommit} {
		if !strings.Contains(sent, want) {
			t.Errorf("the request does not carry %q: %s", want, sent)
		}
	}
	if !strings.HasSuffix(sent, "\n") {
		t.Errorf("the request is not newline-terminated: %q", sent)
	}
}

func TestNewClientRefusesAnIncompleteConfiguration(t *testing.T) {
	base := func() coordinator.ClientConfig {
		return coordinator.ClientConfig{
			Socket: "/tmp/nocx.sock",
			Self:   selfIdentity(),
			Dialer: coordinator.SystemDialer{},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}
	cases := map[string]func(*coordinator.ClientConfig){
		"no socket": func(c *coordinator.ClientConfig) { c.Socket = "" },
		"no dialer": func(c *coordinator.ClientConfig) { c.Dialer = nil },
		"no logger": func(c *coordinator.ClientConfig) { c.Logger = nil },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base()
			break_(&cfg)
			if _, err := coordinator.NewClient(cfg); err == nil {
				t.Fatalf("NewClient accepted a configuration with %s", name)
			}
		})
	}
}

// --- the spawner ----------------------------------------------------------

func TestExecSpawnerRefusesAPathThatIsNotThere(t *testing.T) {
	sp := coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{
		Path:   filepath.Join(t.TempDir(), "nocx-server"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := sp.Spawn(context.Background()); err == nil {
		t.Fatal("Spawn succeeded for a binary that does not exist")
	}
}

func TestNewExecSpawnerRefusesAnIncompleteConfiguration(t *testing.T) {
	if _, err := coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{}).Spawn(context.Background()); err == nil {
		t.Fatal("Spawn succeeded with no path at all")
	}
}

func TestTheSpawnEnvironmentDropsTheOverridesAndKeepsTheRest(t *testing.T) {
	got := coordinator.SpawnEnvironment([]string{
		"HOME=/home/someone",
		"PATH=/usr/bin",
		"NOCX_WS_ADDR=0.0.0.0:1",
		"NOCX_NO_SYSTEM_KEYSTORE=1",
		"NOCX_TEST_APPDIR=/tmp/profile",
		"NOCX_LOG_LEVEL=debug",
	})
	joined := strings.Join(got, "\n")
	for _, gone := range []string{"NOCX_WS_ADDR", "NOCX_NO_SYSTEM_KEYSTORE"} {
		if strings.Contains(joined, gone) {
			t.Errorf("%s survived the clean: %v", gone, got)
		}
	}
	// The profile override is deliberately KEPT: the daemon must resolve
	// the same profile the launcher watched a socket in, and under `go
	// test` that variable is what says which one that is.
	for _, kept := range []string{"HOME=/home/someone", "PATH=/usr/bin", "NOCX_TEST_APPDIR=/tmp/profile", "NOCX_LOG_LEVEL=debug"} {
		if !strings.Contains(joined, kept) {
			t.Errorf("%s did not survive the clean: %v", kept, got)
		}
	}
}

// --- the stopper ----------------------------------------------------------

func TestSignalStopperEndsARunningProcess(t *testing.T) {
	// A real child, stopped for real: the "and on an ordinary machine it
	// succeeds" half of the failure tests below.
	dir := shortDir(t)
	spawner := realSpawner(t, dir)
	sp, err := spawner.Spawn(context.Background())
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	client := newRealClient(t, dir)
	deadline := time.Now().Add(10 * time.Second)
	var sighting coordinator.Sighting
	for {
		sighting, err = client.Hello(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never answered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sighting.PID != sp.PID {
		t.Errorf("the socket reports pid %d, the spawn reported %d", sighting.PID, sp.PID)
	}

	stopper := coordinator.NewSignalStopper(coordinator.SignalStopperConfig{
		Grace:  5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := stopper.Stop(context.Background(), sighting); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := client.Hello(context.Background()); !errors.Is(err, coordinator.ErrNoCoordinator) {
		t.Errorf("the stopped daemon is still answering: %v", err)
	}
}

func TestSignalStopperRefusesASightingWithNoPID(t *testing.T) {
	stopper := coordinator.NewSignalStopper(coordinator.SignalStopperConfig{
		Grace:  time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	err := stopper.Stop(context.Background(), coordinator.Sighting{})
	if err == nil {
		t.Fatal("Stop claimed to have stopped a coordinator it cannot identify")
	}
	if !strings.Contains(err.Error(), "identify") {
		t.Errorf("the failure does not say why: %v", err)
	}
}

func TestSignalStopperReportsAProcessItCannotSignal(t *testing.T) {
	stopper := coordinator.NewSignalStopper(coordinator.SignalStopperConfig{
		Grace:  200 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// pid 1 is init: it exists, and an unprivileged process may not signal
	// it. (If this ever runs as root in a container where pid 1 IS ours,
	// the stop would succeed and the test would fail loudly rather than
	// silently passing.)
	if os.Getuid() == 0 {
		t.Skip("running as root: pid 1 is signallable here")
	}
	err := stopper.Stop(context.Background(), coordinator.Sighting{PID: 1})
	if err == nil {
		t.Fatal("Stop reported success for a process it may not signal")
	}
}

func TestClientReportsADeadlineItCannotSet(t *testing.T) {
	// The bound on one exchange is what stops a daemon that accepted a
	// connection and then went quiet from holding a window's startup open.
	// A connection that will not take one is therefore not usable, and
	// proceeding without it would reintroduce exactly that hang.
	conn := &scriptedConn{deadlineErr: syscall.EINVAL, response: "{}\n"}
	_, err := newScriptedClient(t, conn).Hello(context.Background())
	if err == nil {
		t.Fatal("Hello proceeded on a connection that takes no deadline")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("the failure does not carry the cause: %v", err)
	}
}

func TestASightingOverSomethingThatIsNotAUnixSocketCarriesNoPID(t *testing.T) {
	// The pid comes from the kernel's record on a unix socket and from
	// nowhere else. Where there is none, the sighting says 0 rather than
	// guessing — and the stopper refuses a 0 rather than signalling
	// something arbitrary (TestSignalStopperRefusesASightingWithNoPID).
	conn := &scriptedConn{response: `{"hello":{"build":{"version":"9.9.9","commit":"cafebabe"},` +
		`"protocol":1,"wsAddress":"127.0.0.1:1","wsToken":"t"}}` + "\n"}
	got, err := newScriptedClient(t, conn).Hello(context.Background())
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if got.PID != 0 {
		t.Errorf("pid = %d over a connection the kernel stamped nothing on", got.PID)
	}
}
