package coordinator_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
)

// --- the child daemon --------------------------------------------------
//
// The spawn tests need a REAL detached process that really serves the
// discovery socket, and building cmd/nocx-server from a test would make the
// unit suite depend on a compiler run. So the daemon these tests raise is
// this test binary itself, re-executed with a marker in its environment —
// the shape coordinator_test.go already uses for the two-process lock test.
//
// The marker is read in TestMain rather than selected with -test.run,
// because the launcher spawns with NO arguments and that is the property
// under test: a spawn that needed an argument to say what to be could not
// have proved it.
const (
	childMarkerEnv = "NOCX_COORD_CHILD"
	childDirEnv    = "NOCX_COORD_CHILD_DIR"
	// childReportEnv names a file the child writes what it saw in its own
	// environment into, which is how the launcher's environment cleaning is
	// asserted from the outside rather than from the command it built.
	childReportEnv = "NOCX_COORD_CHILD_REPORT"
	// childRefuseEnv makes the child exit immediately with a status, so the
	// "spawned and then died" path has something real to observe.
	childRefuseEnv = "NOCX_COORD_CHILD_EXIT"
	// childVersionEnv overrides the build version the child answers with,
	// which is what makes an incompatible coordinator reproducible.
	childVersionEnv = "NOCX_COORD_CHILD_VERSION"
)

func TestMain(m *testing.M) {
	if os.Getenv(childMarkerEnv) != "1" {
		os.Exit(m.Run())
	}
	os.Exit(runChildDaemon())
}

// runChildDaemon is the spawned process: it records what reached its
// environment, serves the discovery socket for the directory it was told,
// and waits to be stopped.
func runChildDaemon() int {
	if report := os.Getenv(childReportEnv); report != "" {
		seen := fmt.Sprintf("NOCX_WS_ADDR=%q\nNOCX_NO_SYSTEM_KEYSTORE=%q\nARGS=%q\nPGID=%d\n",
			os.Getenv("NOCX_WS_ADDR"), os.Getenv("NOCX_NO_SYSTEM_KEYSTORE"),
			strings.Join(os.Args[1:], " "), syscall.Getpgrp())
		if err := os.WriteFile(report, []byte(seen), 0o600); err != nil {
			return 9
		}
	}
	if code := os.Getenv(childRefuseEnv); code != "" {
		// A daemon that refuses to start — the shape `nocx-server` has when
		// another one already holds the directory (exit 3).
		if code == "3" {
			return 3
		}
		return 1
	}
	version := testVersion
	if v := os.Getenv(childVersionEnv); v != "" {
		version = v
	}
	s, err := coordinator.NewServer(coordinator.Config{
		Dir:     os.Getenv(childDirEnv),
		Build:   coordinator.Build{Version: version, Commit: testCommit},
		Backend: fakeBackend{addr: testAddr, token: testToken},
		Peers:   coordinator.SystemPeerCredentials{},
		Owner:   coordinator.SystemPathOwner{},
		SelfUID: coordinator.SelfUID(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return 4
	}
	if err := s.Start(); err != nil {
		if errors.Is(err, coordinator.ErrAlreadyRunning) {
			return 3
		}
		return 5
	}
	defer func() { _ = s.Close() }()
	sig := make(chan os.Signal, 1)
	notifyStop(sig)
	<-sig
	return 0
}

// --- doubles -------------------------------------------------------------

// scriptedDiscoverer answers a queue of results, so a test can say "absent,
// then absent, then here" without waiting on a real process.
type scriptedDiscoverer struct {
	mu      sync.Mutex
	replies []reply
	calls   int
}

type reply struct {
	sighting coordinator.Sighting
	err      error
}

func (d *scriptedDiscoverer) Hello(context.Context) (coordinator.Sighting, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if len(d.replies) == 0 {
		return coordinator.Sighting{}, coordinator.ErrNoCoordinator
	}
	r := d.replies[0]
	if len(d.replies) > 1 {
		d.replies = d.replies[1:]
	}
	return r.sighting, r.err
}

// countingSpawner records how many daemons were raised. The body is
// supplied by the test, so one double covers "spawn works", "spawn fails"
// and "spawn does nothing at all".
type countingSpawner struct {
	mu    sync.Mutex
	count int
	body  func() (coordinator.Spawned, error)
}

func (s *countingSpawner) Spawn(context.Context) (coordinator.Spawned, error) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	if s.body == nil {
		return coordinator.Spawned{PID: 4321, Command: "double"}, nil
	}
	return s.body()
}

func (s *countingSpawner) spawns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// recordingStopper is the seam the version refusal kills through.
type recordingStopper struct {
	mu      sync.Mutex
	stopped []coordinator.Sighting
	err     error
}

func (s *recordingStopper) Stop(_ context.Context, sighting coordinator.Sighting) error {
	s.mu.Lock()
	s.stopped = append(s.stopped, sighting)
	s.mu.Unlock()
	return s.err
}

func (s *recordingStopper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stopped)
}

// recordingAnnouncer is the surfacing seam: what a person is told, as
// opposed to what the log recorded.
type recordingAnnouncer struct {
	mu      sync.Mutex
	notices []coordinator.Notice
}

func (a *recordingAnnouncer) Announce(n coordinator.Notice) {
	a.mu.Lock()
	a.notices = append(a.notices, n)
	a.mu.Unlock()
}

func (a *recordingAnnouncer) all() []coordinator.Notice {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]coordinator.Notice(nil), a.notices...)
}

// --- helpers -------------------------------------------------------------

func selfIdentity() coordinator.ClientIdentity {
	return coordinator.ClientIdentity{
		Version:  testVersion,
		Commit:   testCommit,
		Protocol: coordinator.ProtocolVersion,
	}
}

func newLauncher(t *testing.T, cfg coordinator.LauncherConfig) *coordinator.Launcher {
	t.Helper()
	if cfg.Self == (coordinator.ClientIdentity{}) {
		cfg.Self = selfIdentity()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Announcer == nil {
		cfg.Announcer = &recordingAnnouncer{}
	}
	l, err := coordinator.NewLauncher(cfg)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	return l
}

// realSpawner raises the child daemon described at the top of this file.
func realSpawner(t *testing.T, dir string) *trackingSpawner {
	t.Helper()
	t.Setenv(childMarkerEnv, "1")
	t.Setenv(childDirEnv, dir)
	sp := coordinator.NewExecSpawner(coordinator.ExecSpawnerConfig{
		Path:   os.Args[0],
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &trackingSpawner{t: t, inner: sp}
}

// trackingSpawner stops every daemon a test raised, so no test leaves a
// process behind holding a lock on a directory that is about to be removed.
type trackingSpawner struct {
	t     *testing.T
	inner coordinator.Spawner
	mu    sync.Mutex
	count int
}

func (s *trackingSpawner) Spawn(ctx context.Context) (coordinator.Spawned, error) {
	sp, err := s.inner.Spawn(ctx)
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	if err == nil && sp.PID > 0 {
		pid := sp.PID
		s.t.Cleanup(func() {
			if p, e := os.FindProcess(pid); e == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		})
	}
	return sp, err
}

func (s *trackingSpawner) spawns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// --- 1. a cold start raises exactly one daemon and reaches it ------------

func TestColdStartRaisesOneDaemonAndReachesIt(t *testing.T) {
	dir := shortDir(t)
	spawner := realSpawner(t, dir)
	client := newRealClient(t, dir)

	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:       dir,
		Client:    client,
		Spawner:   spawner,
		Stopper:   &recordingStopper{},
		Announcer: &recordingAnnouncer{},
	})

	got, err := l.Launch(context.Background())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !got.Spawned {
		t.Error("Launch did not report that it raised the daemon")
	}
	if got.Hello.WSAddress != testAddr {
		t.Errorf("ws address = %q, want %q", got.Hello.WSAddress, testAddr)
	}
	if got.Hello.WSToken != testToken {
		t.Errorf("ws token = %q, want the minted %q", got.Hello.WSToken, testToken)
	}
	if n := spawner.spawns(); n != 1 {
		t.Errorf("spawned %d daemons, want exactly 1", n)
	}

	// And a second launcher against the same directory raises none: the
	// first one is already answering.
	second := newLauncher(t, coordinator.LauncherConfig{
		Dir:     dir,
		Client:  newRealClient(t, dir),
		Spawner: &countingSpawner{},
		Stopper: &recordingStopper{},
	})
	again, err := second.Launch(context.Background())
	if err != nil {
		t.Fatalf("second Launch: %v", err)
	}
	if again.Spawned {
		t.Error("the second launcher raised a daemon over a live one")
	}
}

// --- 2. two launchers racing raise exactly one daemon --------------------

func TestTwoLaunchersRacingRaiseExactlyOneDaemon(t *testing.T) {
	dir := shortDir(t)
	spawner := realSpawner(t, dir)

	const launchers = 4
	results := make([]coordinator.Launch, launchers)
	errs := make([]error, launchers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range launchers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := newLauncher(t, coordinator.LauncherConfig{
				Dir:     dir,
				Client:  newRealClient(t, dir),
				Spawner: spawner,
				Stopper: &recordingStopper{},
			})
			<-start
			results[i], errs[i] = l.Launch(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("launcher %d: %v", i, err)
		}
		if results[i].Hello.WSToken != testToken {
			t.Errorf("launcher %d got token %q, want %q", i, results[i].Hello.WSToken, testToken)
		}
	}
	if n := spawner.spawns(); n != 1 {
		t.Errorf("%d daemons were raised, want exactly 1", n)
	}
}

// --- 3. a spawn that never becomes ready is reported, and does not hang --

func TestASpawnThatNeverBecomesReadyIsReported(t *testing.T) {
	dir := shortDir(t)
	spawner := &countingSpawner{body: func() (coordinator.Spawned, error) {
		// Started, and never serves anything.
		return coordinator.Spawned{PID: 12345, Command: "/opt/nocx/nocx-server"}, nil
	}}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       &scriptedDiscoverer{},
		Spawner:      spawner,
		Stopper:      &recordingStopper{},
		ReadyTimeout: 150 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
	})

	done := make(chan error, 1)
	go func() {
		_, err := l.Launch(context.Background())
		done <- err
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Launch hung on a daemon that never became ready")
	}
	if err == nil {
		t.Fatal("Launch reported success for a daemon that never answered")
	}
	for _, want := range []string{"/opt/nocx/nocx-server", filepath.Join(dir, "srv.sock"), "12345"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q: %v", want, err)
		}
	}
}

func TestASpawnThatDiesBeforeAnsweringReportsItsExit(t *testing.T) {
	dir := shortDir(t)
	t.Setenv(childRefuseEnv, "3")
	spawner := realSpawner(t, dir)
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       newRealClient(t, dir),
		Spawner:      spawner,
		Stopper:      &recordingStopper{},
		ReadyTimeout: 10 * time.Second,
		PollInterval: 10 * time.Millisecond,
	})

	_, err := l.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch reported success for a daemon that exited immediately")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("the failure does not say the daemon exited: %v", err)
	}
}

// --- 4 & 5. an incompatible coordinator is refused, replaced, and said ---

func TestABuildVersionMismatchIsRefusedReplacedAndSurfaced(t *testing.T) {
	dir := shortDir(t)
	old := coordinator.Sighting{
		PID: 777,
		Hello: coordinator.Hello{
			Build:     coordinator.Build{Version: "0.0.1", Commit: "0ldc0de"},
			Protocol:  coordinator.ProtocolVersion,
			WSAddress: "127.0.0.1:1",
			WSToken:   "old-token",
		},
	}
	fresh := coordinator.Sighting{
		PID: 888,
		Hello: coordinator.Hello{
			Build:     coordinator.Build{Version: testVersion, Commit: testCommit},
			Protocol:  coordinator.ProtocolVersion,
			WSAddress: testAddr,
			WSToken:   testToken,
		},
	}
	client := &scriptedDiscoverer{replies: []reply{
		{sighting: old},
		{err: coordinator.ErrNoCoordinator},
		{sighting: fresh},
	}}
	stopper := &recordingStopper{}
	announcer := &recordingAnnouncer{}
	spawner := &countingSpawner{}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       client,
		Spawner:      spawner,
		Stopper:      stopper,
		Announcer:    announcer,
		ReadyTimeout: 2 * time.Second,
		PollInterval: 5 * time.Millisecond,
	})

	got, err := l.Launch(context.Background())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !got.Replaced {
		t.Error("Launch did not report that it replaced the coordinator")
	}
	if got.Hello.WSToken != testToken {
		t.Errorf("the launcher kept the old token %q", got.Hello.WSToken)
	}
	if stopper.count() != 1 {
		t.Errorf("the old coordinator was stopped %d times, want 1", stopper.count())
	}
	if spawner.spawns() != 1 {
		t.Errorf("%d daemons raised, want 1", spawner.spawns())
	}

	notices := announcer.all()
	if len(notices) != 1 {
		t.Fatalf("%d notices surfaced, want 1: %+v", len(notices), notices)
	}
	n := notices[0]
	if n.Kind != coordinator.NoticeSessionsLost {
		t.Errorf("notice kind = %q, want %q", n.Kind, coordinator.NoticeSessionsLost)
	}
	// The loss must be stated in words a person reads, not inferred from a
	// version pair (D4: killing is correct here, being honest about it is
	// not optional).
	if !strings.Contains(strings.ToLower(n.Message), "session") {
		t.Errorf("the notice does not mention the sessions: %q", n.Message)
	}
	if !strings.Contains(n.Message, "0.0.1") || !strings.Contains(n.Message, testVersion) {
		t.Errorf("the notice does not name both versions: %q", n.Message)
	}
	if n.Running.Version != "0.0.1" || n.Expected.Version != testVersion {
		t.Errorf("notice carries running %+v expected %+v", n.Running, n.Expected)
	}
}

func TestAProtocolVersionMismatchIsRefusedReplacedAndSurfaced(t *testing.T) {
	dir := shortDir(t)
	old := coordinator.Sighting{
		PID: 777,
		Hello: coordinator.Hello{
			// The SAME build, so only the protocol can explain the refusal.
			Build:     coordinator.Build{Version: testVersion, Commit: testCommit},
			Protocol:  coordinator.ProtocolVersion + 1,
			WSAddress: "127.0.0.1:1",
			WSToken:   "old-token",
		},
	}
	fresh := coordinator.Sighting{Hello: coordinator.Hello{
		Build:     coordinator.Build{Version: testVersion, Commit: testCommit},
		Protocol:  coordinator.ProtocolVersion,
		WSAddress: testAddr,
		WSToken:   testToken,
	}}
	client := &scriptedDiscoverer{replies: []reply{
		{sighting: old},
		{err: coordinator.ErrNoCoordinator},
		{sighting: fresh},
	}}
	stopper := &recordingStopper{}
	announcer := &recordingAnnouncer{}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       client,
		Spawner:      &countingSpawner{},
		Stopper:      stopper,
		Announcer:    announcer,
		ReadyTimeout: 2 * time.Second,
		PollInterval: 5 * time.Millisecond,
	})

	got, err := l.Launch(context.Background())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !got.Replaced {
		t.Error("Launch did not report that it replaced the coordinator")
	}
	if stopper.count() != 1 {
		t.Errorf("the old coordinator was stopped %d times, want 1", stopper.count())
	}
	notices := announcer.all()
	if len(notices) != 1 {
		t.Fatalf("%d notices surfaced, want 1", len(notices))
	}
	if !strings.Contains(notices[0].Message, "protocol") {
		t.Errorf("the notice does not name the protocol: %q", notices[0].Message)
	}
	if notices[0].RunningProtocol != coordinator.ProtocolVersion+1 {
		t.Errorf("notice running protocol = %d", notices[0].RunningProtocol)
	}
}

func TestAStopThatFailsIsReportedAndRaisesNoSecondDaemon(t *testing.T) {
	dir := shortDir(t)
	old := coordinator.Sighting{PID: 777, Hello: coordinator.Hello{
		Build:    coordinator.Build{Version: "0.0.1"},
		Protocol: coordinator.ProtocolVersion,
	}}
	spawner := &countingSpawner{}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       &scriptedDiscoverer{replies: []reply{{sighting: old}}},
		Spawner:      spawner,
		Stopper:      &recordingStopper{err: errors.New("no such process")},
		ReadyTimeout: time.Second,
		PollInterval: 5 * time.Millisecond,
	})

	_, err := l.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch succeeded although the old coordinator could not be stopped")
	}
	if !strings.Contains(err.Error(), "no such process") {
		t.Errorf("the failure does not carry the cause: %v", err)
	}
	if spawner.spawns() != 0 {
		t.Errorf("%d daemons raised beside a coordinator that is still running", spawner.spawns())
	}
}

func TestAReplacementThatComesUpIncompatibleIsRefused(t *testing.T) {
	dir := shortDir(t)
	mismatched := coordinator.Sighting{Hello: coordinator.Hello{
		Build:    coordinator.Build{Version: "0.0.1"},
		Protocol: coordinator.ProtocolVersion,
	}}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir: dir,
		// Every answer is the wrong version: the replacement must be
		// refused rather than replaced again, forever.
		Client:       &scriptedDiscoverer{replies: []reply{{sighting: mismatched}}},
		Spawner:      &countingSpawner{},
		Stopper:      &recordingStopper{},
		ReadyTimeout: time.Second,
		PollInterval: 5 * time.Millisecond,
	})

	_, err := l.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch accepted a replacement that is still incompatible")
	}
}

// --- 6. the spawned daemon inherits no launch override -------------------

func TestTheSpawnedDaemonDoesNotInheritTheWSAddressOverride(t *testing.T) {
	dir := shortDir(t)
	report := filepath.Join(t.TempDir(), "seen.txt")
	t.Setenv("NOCX_WS_ADDR", "0.0.0.0:9999")
	t.Setenv("NOCX_NO_SYSTEM_KEYSTORE", "1")
	t.Setenv(childReportEnv, report)
	spawner := realSpawner(t, dir)

	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:     dir,
		Client:  newRealClient(t, dir),
		Spawner: spawner,
		Stopper: &recordingStopper{},
	})
	if _, err := l.Launch(context.Background()); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	seen, err := os.ReadFile(report) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("the spawned daemon wrote no report: %v", err)
	}
	if !strings.Contains(string(seen), `NOCX_WS_ADDR=""`) {
		t.Errorf("NOCX_WS_ADDR reached the daemon:\n%s", seen)
	}
	if !strings.Contains(string(seen), `NOCX_NO_SYSTEM_KEYSTORE=""`) {
		t.Errorf("NOCX_NO_SYSTEM_KEYSTORE reached the daemon:\n%s", seen)
	}
	// And nothing reached its argv either: a token must never be able to.
	if !strings.Contains(string(seen), `ARGS=""`) {
		t.Errorf("the daemon was spawned with arguments:\n%s", seen)
	}
	// Detached: setsid puts it in a session of its own, so its process
	// group is not ours and the window closing cannot take it down.
	if strings.Contains(string(seen), fmt.Sprintf("PGID=%d\n", syscall.Getpgrp())) {
		t.Errorf("the daemon shares this process group, so it is not detached:\n%s", seen)
	}
}

// --- the launcher never writes the token anywhere ------------------------

func TestTheLauncherLogsNoToken(t *testing.T) {
	dir := shortDir(t)
	handler := &capturingHandler{}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir: dir,
		Client: &scriptedDiscoverer{replies: []reply{{sighting: coordinator.Sighting{
			Hello: coordinator.Hello{
				Build:     coordinator.Build{Version: testVersion, Commit: testCommit},
				Protocol:  coordinator.ProtocolVersion,
				WSAddress: testAddr,
				WSToken:   testToken,
			},
		}}}},
		Spawner: &countingSpawner{},
		Stopper: &recordingStopper{},
		Logger:  slog.New(handler),
	})
	if _, err := l.Launch(context.Background()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	for _, line := range handler.all() {
		if strings.Contains(line, testToken) {
			t.Errorf("a log line carries the token: %s", line)
		}
	}
}

// --- configuration --------------------------------------------------------

func TestNewLauncherRefusesAnIncompleteConfiguration(t *testing.T) {
	base := func() coordinator.LauncherConfig {
		return coordinator.LauncherConfig{
			Dir:       "/tmp/nocx-run",
			Self:      selfIdentity(),
			Client:    &scriptedDiscoverer{},
			Spawner:   &countingSpawner{},
			Stopper:   &recordingStopper{},
			Announcer: &recordingAnnouncer{},
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}
	cases := map[string]func(*coordinator.LauncherConfig){
		"no directory":        func(c *coordinator.LauncherConfig) { c.Dir = "" },
		"relative directory":  func(c *coordinator.LauncherConfig) { c.Dir = "run" },
		"no client":           func(c *coordinator.LauncherConfig) { c.Client = nil },
		"no spawner":          func(c *coordinator.LauncherConfig) { c.Spawner = nil },
		"no stopper":          func(c *coordinator.LauncherConfig) { c.Stopper = nil },
		"no announcer":        func(c *coordinator.LauncherConfig) { c.Announcer = nil },
		"no logger":           func(c *coordinator.LauncherConfig) { c.Logger = nil },
		"no protocol version": func(c *coordinator.LauncherConfig) { c.Self.Protocol = 0 },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base()
			break_(&cfg)
			if _, err := coordinator.NewLauncher(cfg); err == nil {
				t.Fatalf("NewLauncher accepted a configuration with %s", name)
			}
		})
	}
}

func TestARuntimeDirectoryThatCannotBeCreatedIsReported(t *testing.T) {
	// A regular file where the runtime directory must be: MkdirAll fails,
	// and the launcher has to say so rather than spawn into nowhere.
	base := t.TempDir()
	blocked := filepath.Join(base, "run")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	spawner := &countingSpawner{}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          blocked,
		Client:       &scriptedDiscoverer{},
		Spawner:      spawner,
		Stopper:      &recordingStopper{},
		ReadyTimeout: time.Second,
		PollInterval: 5 * time.Millisecond,
	})
	_, err := l.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch succeeded with a file where the runtime directory belongs")
	}
	if !strings.Contains(err.Error(), blocked) {
		t.Errorf("the failure does not name the directory: %v", err)
	}
	if spawner.spawns() != 0 {
		t.Error("a daemon was spawned although the runtime directory was unusable")
	}
}

func TestASpawnerFailureIsReported(t *testing.T) {
	dir := shortDir(t)
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:    dir,
		Client: &scriptedDiscoverer{},
		Spawner: &countingSpawner{body: func() (coordinator.Spawned, error) {
			return coordinator.Spawned{}, errors.New("fork/exec: permission denied")
		}},
		Stopper:      &recordingStopper{},
		ReadyTimeout: time.Second,
		PollInterval: 5 * time.Millisecond,
	})
	_, err := l.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch succeeded although the spawn failed")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the failure does not carry the cause: %v", err)
	}
}

func TestACancelledContextStopsTheWait(t *testing.T) {
	dir := shortDir(t)
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       &scriptedDiscoverer{},
		Spawner:      &countingSpawner{},
		Stopper:      &recordingStopper{},
		ReadyTimeout: time.Minute,
		PollInterval: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	done := make(chan error, 1)
	go func() {
		_, err := l.Launch(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Launch reported success after its context was cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Launch ignored the cancelled context")
	}
}

func TestASpawnLockHeldByAnotherLauncherIsReported(t *testing.T) {
	// The loser of a spawn race waits for the winner, and the wait is
	// bounded: a launcher that crashed while holding the lock — or, as
	// here, one that holds it and never raises anything — must not keep
	// every later window blank forever.
	dir := shortDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockPath := filepath.Join(dir, "spawn.lock")
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("open the lock: %v", err)
	}
	defer func() { _ = held.Close() }()
	if flockErr := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		t.Fatalf("hold the lock: %v", flockErr)
	}

	spawner := &countingSpawner{}
	l := newLauncher(t, coordinator.LauncherConfig{
		Dir:          dir,
		Client:       &scriptedDiscoverer{},
		Spawner:      spawner,
		Stopper:      &recordingStopper{},
		ReadyTimeout: 150 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
	})
	_, err = l.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch succeeded although another launcher held the spawn lock")
	}
	if !strings.Contains(err.Error(), lockPath) {
		t.Errorf("the failure does not name the lock: %v", err)
	}
	if spawner.spawns() != 0 {
		t.Errorf("%d daemons raised without the spawn lock", spawner.spawns())
	}
}
