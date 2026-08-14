// Package helper_test drives the backend's git factory over the helper
// protocol: a helper-backed git.Repo whose read methods are one Call each
// (plan Task 7). The acceptance test is the contract in one assertion —
// a status read through the helper-backed repo returns the same git.Status
// the LOCAL factory returns for the same repository, field by field —
// against an in-process helper speaking the REAL protocol (the real host,
// the real git service, over io.Pipe).
package helper_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
)

// fixtureRepo builds a real repository with a commit, a modified file and
// an untracked file, so the status read is non-trivial on every list.
// internal/git/local's own fixture is unexported; this is the same recipe
// inline, the way hostsvc's test does it.
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

// fakeConn is a HelperConn backed by io.Pipe whose peer is the REAL helper
// host serving the REAL git service — the client side of the wire meets the
// actual implementation, in process, no SSH. The peer function's return
// value is the remote process's exit status.
type fakeConn struct {
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader

	exited   chan struct{}
	exitCode int
}

func newFakeConn(peer func(stdin io.Reader, stdout io.Writer) int) *fakeConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeConn{
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

func (f *fakeConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeConn) Stderr() io.Reader     { return f.stderr }
func (f *fakeConn) Start(string) error    { return nil }
func (f *fakeConn) Wait() (int, error)    { <-f.exited; return f.exitCode, nil }
func (f *fakeConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeConn) LostErr() error        { return nil }
func (f *fakeConn) Close() error          { return f.stdin.Close() }

// TestHelperRepoStatusMatchesLocal is the contract in one assertion: the
// panel must say the same thing on both machines, so the helper-backed repo
// is only correct if it agrees with the local implementation on the same
// repository — field by field, through the real protocol.
func TestHelperRepoStatusMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t)

	local := localgit.NewFactory()
	repoLocal, outcome, err := local.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	want, err := repoLocal.Status(context.Background())
	if err != nil {
		t.Fatalf("local status: %v", err)
	}

	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, "testhash", "instance-1",
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		h.Register(hostsvc.New(localgit.NewFactory()))
		if serveErr := h.Serve(context.Background()); serveErr != nil {
			return 1
		}
		return 0
	})
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("helper dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	factory := helpergit.NewFactory(c)
	repoHelper, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("helper open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("helper open outcome = %s", outcome.State)
	}
	defer func() { _ = repoHelper.Close() }()

	got, err := repoHelper.Status(context.Background())
	if err != nil {
		t.Fatalf("helper status: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("helper-backed status disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}
}
