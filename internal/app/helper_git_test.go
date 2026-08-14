package app

// Tests for the helper-backed git factory selection (the remote-helper
// design): the env configuration that says a helper is available, the
// dialing factory that brings one helper up per session and never leaks
// it — a refusing open closes the lane, an exec the server refuses closes
// the lane, and two bindings on one session share ONE helper process (D4)
// that dies when the last binding closes and is redialed on the next open.
// The wire is met in process: the real host, the real git service, over
// io.Pipe.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// fixtureRepo builds a real repository with a commit, a modified file and
// an untracked file, so a successful open is non-trivial. The recipe is
// the one internal/git/helper's test uses, inline.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	home := t.TempDir()
	cfg := "[user]\n\tname = Test User\n\temail = test@nocx.invalid\n[init]\n\tdefaultBranch = master\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + filepath.Dir(gitBin) + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"LANG=C",
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...) // #nosec G204 — gitBin is LookPath-resolved; args are fixed test literals
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@nocx.invalid")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "one")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "u.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeLaneConn is a HelperConn backed by io.Pipe whose peer is the REAL
// helper host — the client side of the wire meets the actual
// implementation, in process, no SSH. It records Close so a test can prove
// the factory released the lane.
type fakeLaneConn struct {
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader

	exited   chan struct{}
	exitCode int

	startErr error

	mu     sync.Mutex
	closed int
}

func newFakeLaneConn(peer func(stdin io.Reader, stdout io.Writer) int) *fakeLaneConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeLaneConn{
		stdin:  toPeerW,
		stdout: fromPeerR,
		stderr: bytes.NewReader(nil),
		exited: make(chan struct{}),
	}
	go func() {
		code := peer(toPeerR, fromPeerW)
		_ = fromPeerW.Close()
		f.exitCode = code
		close(f.exited)
	}()
	return f
}

func (f *fakeLaneConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeLaneConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeLaneConn) Stderr() io.Reader     { return f.stderr }
func (f *fakeLaneConn) Start(string) error    { return f.startErr }
func (f *fakeLaneConn) Wait() (int, error)    { <-f.exited; return f.exitCode, nil }
func (f *fakeLaneConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeLaneConn) LostErr() error        { return nil }

func (f *fakeLaneConn) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return f.stdin.Close()
}

func (f *fakeLaneConn) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// fakeLaneProvider hands out a fresh scripted lane per HelperConn call and
// records them, so a test can prove how many helpers were brought up.
type fakeLaneProvider struct {
	peer     func(in io.Reader, out io.Writer) int
	startErr error

	mu    sync.Mutex
	conns []*fakeLaneConn
}

func (p *fakeLaneProvider) HelperConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.HelperConn, error) {
	c := newFakeLaneConn(p.peer)
	c.startErr = p.startErr
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()
	return c, nil
}

func (p *fakeLaneProvider) laneCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *fakeLaneProvider) lane(i int) *fakeLaneConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[i]
}

// realHelperPeer serves the REAL helper host with the REAL git service.
func realHelperPeer() func(in io.Reader, out io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, "testhash", "instance-1", discardLogger())
		h.Register(hostsvc.New(localgit.NewFactory()))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// fakeRemoteSession is a session.Session whose only interesting facts are
// the id, kind, host and SSH options the helper selection reads.
type fakeRemoteSession struct {
	id   session.ID
	host string
}

func (s *fakeRemoteSession) ID() session.ID            { return s.id }
func (s *fakeRemoteSession) Kind() session.Kind        { return session.KindRemote }
func (s *fakeRemoteSession) Host() string              { return s.host }
func (s *fakeRemoteSession) Cwd() string               { return "" }
func (s *fakeRemoteSession) ProfileID() string         { return "" }
func (s *fakeRemoteSession) CredentialID() string      { return "" }
func (s *fakeRemoteSession) Write([]byte) (int, error) { return 0, nil }
func (s *fakeRemoteSession) EnqueueWrite([]byte) bool  { return false }
func (s *fakeRemoteSession) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (s *fakeRemoteSession) Close() error { return nil }
func (s *fakeRemoteSession) Done() <-chan struct{} {
	return make(chan struct{})
}

func (s *fakeRemoteSession) StartOutput(context.Context, session.OutputHandler) error {
	return nil
}
func (s *fakeRemoteSession) ShellIntegrationReason() ssh.RefusalReason { return ssh.ReasonNone }
func (s *fakeRemoteSession) SSHOptions() []ssh.ConnectOption           { return nil }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func configuredSelector(t *testing.T, provider *fakeLaneProvider) transport.GitFactoryFor {
	t.Helper()
	t.Setenv(helperCommandEnvVar, "/opt/nocx-helper")
	t.Setenv(helperHashEnvVar, "testhash")
	return helperGitFactory(provider, discardLogger())
}

// TestHelperConfigFromEnv: the helper is available only when BOTH the
// command and the expected hash are set — a half-set configuration is no
// configuration, and the refusal stands.
func TestHelperConfigFromEnv(t *testing.T) {
	t.Setenv(helperCommandEnvVar, "")
	t.Setenv(helperHashEnvVar, "")
	if _, _, ok := helperConfigFromEnv(); ok {
		t.Fatal("no configuration must answer not-ok")
	}
	t.Setenv(helperCommandEnvVar, "/opt/nocx-helper")
	if _, _, ok := helperConfigFromEnv(); ok {
		t.Fatal("a command without a hash must answer not-ok")
	}
	t.Setenv(helperHashEnvVar, "abc123")
	command, hash, ok := helperConfigFromEnv()
	if !ok || command != "/opt/nocx-helper" || hash != "abc123" {
		t.Fatalf("configured: got (%q, %q, %v), want (/opt/nocx-helper, abc123, true)", command, hash, ok)
	}
}

// TestHelperSelectorUnconfiguredIsNil: no configuration means no helper is
// available — the selection returns nil, which keeps the transport's
// OpenRemoteUnsupported refusal standing.
func TestHelperSelectorUnconfiguredIsNil(t *testing.T) {
	t.Setenv(helperCommandEnvVar, "")
	t.Setenv(helperHashEnvVar, "")
	sel := helperGitFactory(&fakeLaneProvider{}, discardLogger())
	if got := sel(&fakeRemoteSession{host: "host.example"}); got != nil {
		t.Fatalf("unconfigured selection = %v, want nil", got)
	}
}

// TestHelperSharesOneProcessAcrossOpens is D4 on the wire: two bindings on
// one session share ONE helper process — one lane acquired, one dial — and
// the process dies when the last binding closes and is redialed on the
// next open.
func TestHelperSharesOneProcessAcrossOpens(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	factory := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	dir := fixtureRepo(t)

	repo1, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("first open outcome = %s, want ok", outcome.State)
	}
	repo2, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("second open outcome = %s, want ok", outcome.State)
	}
	if got := provider.laneCount(); got != 1 {
		t.Fatalf("two opens brought up %d helper processes, want 1 — the second open must share the first's process (D4)", got)
	}

	// Both bindings reference the helper; closing the first must NOT kill
	// the process the second still uses.
	if err = repo1.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if got := provider.lane(0).closeCount(); got != 0 {
		t.Fatalf("lane closed %d times after the first binding closed, want 0 — the second binding still references it", got)
	}

	// The last binding closes → the helper process dies with it.
	if err = repo2.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("lane closed %d times after the last binding closed, want 1", got)
	}

	// The next open must NOT reuse the dead client — a fresh helper comes
	// up.
	repo3, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open after the helper died: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("third open outcome = %s, want ok", outcome.State)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("open after the helper died brought up %d helpers total, want 2 — the dead client must not be reused", got)
	}
	_ = repo3.Close()
}

// TestHelperSessionsDoNotShareAProcess: two sessions — even to the same
// host — each get their own helper. The pool key that would prove they
// share one connection is not exposed, and sharing across principals would
// be an authorization error.
func TestHelperSessionsDoNotShareAProcess(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	dir := fixtureRepo(t)

	factoryA := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	factoryB := sel(&fakeRemoteSession{id: "s2", host: "host.example"})
	repoA, _, err := factoryA.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("session A open: %v", err)
	}
	repoB, _, err := factoryB.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("session B open: %v", err)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("two sessions brought up %d helpers, want 2 — each session owns its helper", got)
	}
	_ = repoA.Close()
	_ = repoB.Close()
}

// TestHelperDialFactory_RefusingOpenClosesTheLane: an open that answers
// notARepository carries no repo, and nothing else references the helper
// process — the factory must close the client (and so the lane) rather
// than leaking it on the far host.
func TestHelperDialFactory_RefusingOpenClosesTheLane(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	factory := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	_, outcome, err := factory.Open(context.Background(), t.TempDir()) // not a repository
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if outcome.State != git.OpenNotARepository {
		t.Fatalf("outcome = %s, want notARepository", outcome.State)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("lane closed %d times, want 1 — a refusing open must not leak the helper process", got)
	}
}

// TestHelperDialFactory_ExecForbiddenClosesTheLane: a server that refuses
// the exec is a helper that cannot start — Dial reports the sentinel, and
// the lane the factory acquired is closed rather than leaked.
func TestHelperDialFactory_ExecForbiddenClosesTheLane(t *testing.T) {
	provider := &fakeLaneProvider{
		peer:     func(io.Reader, io.Writer) int { return 0 },
		startErr: errors.New("exec request failed"),
	}
	sel := configuredSelector(t, provider)
	factory := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	_, _, err := factory.Open(context.Background(), "/some/cwd")
	if !errors.Is(err, client.ErrExecForbidden) {
		t.Fatalf("open error = %v, want ErrExecForbidden", err)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("lane closed %d times, want 1", got)
	}
}
