package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/testwait"
)

func TestOpenResolvesRealRepository(t *testing.T) {
	dir := newGitRepo(t)
	gitWrite(t, dir, "f.txt", "hi")

	repo, outcome, err := NewFactory(WithEnv(gitEnv(t))).Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("State = %s", outcome.State)
	}
	if outcome.Toplevel != dir {
		t.Fatalf("Toplevel = %q, want %q", outcome.Toplevel, dir)
	}
	if outcome.GitDir != filepath.Join(dir, ".git") {
		t.Fatalf("GitDir = %q", outcome.GitDir)
	}
	if !strings.HasPrefix(outcome.GitVersion, "git version 2.") {
		t.Fatalf("GitVersion = %q", outcome.GitVersion)
	}
	if outcome.EnvState != git.EnvResolved {
		t.Fatalf("EnvState = %s", outcome.EnvState)
	}
	if repo == nil {
		t.Fatal("ok outcome with a nil repo")
	}
}

func TestOpenEmptyCwd(t *testing.T) {
	repo, outcome, err := NewFactory(WithEnv(gitEnv(t))).Open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenNoCwd {
		t.Fatalf("State = %s, want noCwd", outcome.State)
	}
	if repo != nil {
		t.Fatal("noCwd outcome with a repo")
	}
}

func TestOpenNotARepository(t *testing.T) {
	dir := t.TempDir() // exists, but not a repository
	repo, outcome, err := NewFactory(WithEnv(gitEnv(t))).Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenNotARepository {
		t.Fatalf("State = %s, want notARepository", outcome.State)
	}
	if repo != nil {
		t.Fatal("notARepository outcome with a repo")
	}
}

func TestOpenGitUnavailable(t *testing.T) {
	// A PATH with no git at all: the probe cannot find the binary.
	empty := t.TempDir()
	env := []string{"PATH=" + empty, "HOME=" + t.TempDir()}
	repo, outcome, err := NewFactory(WithEnv(env)).Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenGitUnavailable {
		t.Fatalf("State = %s, want gitUnavailable", outcome.State)
	}
	if repo != nil {
		t.Fatal("gitUnavailable outcome with a repo")
	}
}

func TestOpenGitTooOld(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_GIT_VERSION": "2.20.0"})
	repo, outcome, err := NewFactory(WithEnv(env)).Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenGitTooOld {
		t.Fatalf("State = %s, want gitTooOld", outcome.State)
	}
	if outcome.GitVersion != "git version 2.20.0" {
		t.Fatalf("GitVersion = %q — the outcome carries the version it found", outcome.GitVersion)
	}
	if repo != nil {
		t.Fatal("gitTooOld outcome with a repo")
	}
}

func TestOpenCurrentGitAccepted(t *testing.T) {
	env := fakeGitEnv(t, nil) // default version 2.55.0
	repo, outcome, err := NewFactory(WithEnv(env)).Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("State = %s", outcome.State)
	}
	if repo == nil {
		t.Fatal("ok outcome with a nil repo")
	}
}

// TestOpenRevParseMalformed: rev-parse's output is validated, not trusted —
// one line, three lines, or a relative path is notARepository, never a path
// we hand to a subprocess.
func TestOpenRevParseMalformed(t *testing.T) {
	cases := map[string]string{
		"one line":      "/only/one\n",
		"three lines":   "/one\n/two\n/three\n",
		"relative path": "relative\nrelative/.git\n",
		"empty":         "\n\n",
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			env := fakeGitEnv(t, map[string]string{"FAKE_REVPARSE": answer})
			repo, outcome, err := NewFactory(WithEnv(env)).Open(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if outcome.State != git.OpenNotARepository {
				t.Fatalf("State = %s, want notARepository for rev-parse output %q", outcome.State, answer)
			}
			if repo != nil {
				t.Fatal("notARepository outcome with a repo")
			}
		})
	}
}

func TestOpenRevParseFailingExit(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_REVPARSE": "FAIL"})
	repo, outcome, err := NewFactory(WithEnv(env)).Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenNotARepository {
		t.Fatalf("State = %s, want notARepository", outcome.State)
	}
	if repo != nil {
		t.Fatal("notARepository outcome with a repo")
	}
}

func TestOpenContextCancelled(t *testing.T) {
	env := fakeGitEnv(t, map[string]string{"FAKE_STATUS": "sleep"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := NewFactory(WithEnv(env)).Open(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open returned %v, want context.Canceled", err)
	}
}

// TestOpenOutcomeRepoConsistency is the factory's half of the ownership rule
// (spec §5.1 rule 1): a non-nil Repo iff the outcome is ok. The composition
// layer still checks both directions explicitly; the factory must never
// produce the malformed pair in the first place.
func TestOpenOutcomeRepoConsistency(t *testing.T) {
	dir := newGitRepo(t)
	realEnv := gitEnv(t)
	empty := t.TempDir()

	cases := []struct {
		name string
		env  []string
		cwd  string
	}{
		{"real repo", realEnv, dir},
		{"empty cwd", realEnv, ""},
		{"not a repo", realEnv, empty},
		{"no git", []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}, empty},
		{"too old", fakeGitEnv(t, map[string]string{"FAKE_GIT_VERSION": "git version 2.10.0"}), empty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, outcome, err := NewFactory(WithEnv(c.env)).Open(context.Background(), c.cwd)
			if err != nil {
				return // cancelled-like errors are not outcome pairs
			}
			ok := outcome.State == git.OpenOK
			if ok && repo == nil {
				t.Fatal("ok outcome with a nil repo — the (nil, ok) direction")
			}
			if !ok && repo != nil {
				t.Fatalf("refusing outcome %s with a non-nil repo", outcome.State)
			}
		})
	}
}

func TestCapabilityProbeCached(t *testing.T) {
	env := fakeGitEnv(t, nil)
	f := NewFactory(WithEnv(env))
	for i := 0; i < 3; i++ {
		repo, outcome, err := f.Open(context.Background(), t.TempDir())
		if err != nil || outcome.State != git.OpenOK || repo == nil {
			t.Fatalf("open %d: %v %s", i, err, outcome.State)
		}
	}
	// The probe ran once: --version appears exactly once in the argv log.
	calls := fakeGitLog(t, env)
	versions := 0
	for _, call := range calls {
		if len(call) == 1 && call[0] == "--version" {
			versions++
		}
	}
	if versions != 1 {
		t.Fatalf("version probe ran %d times, want 1 (cached)", versions)
	}
}

func TestVersionFloor(t *testing.T) {
	cases := []struct {
		version string
		below   bool
	}{
		{"git version 2.24.9", true},
		{"git version 2.25.0", false},
		{"git version 2.55.0", false},
		{"git version 3.0.0", false},
		{"git version 1.8.3", true},
		{"garbage", true},
	}
	for _, c := range cases {
		if got := belowFloor(c.version); got != c.below {
			t.Errorf("belowFloor(%q) = %v, want %v", c.version, got, c.below)
		}
	}
}

func TestResolveGitScansEnvPath(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	if err := writeFileExec(script, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	got, err := resolveGit([]string{"PATH=" + dir + ":/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if got != script {
		t.Fatalf("resolveGit = %q, want %q", got, script)
	}
	if _, err := resolveGit([]string{"PATH=" + t.TempDir()}); err == nil {
		t.Fatal("resolveGit found git in an empty PATH")
	}
}

func writeFileExec(path, content string) error {
	return writeFile(path, content, 0o755)
}

// TestOpenDoesNotWaitForEnvResolution is nocx-6pz0's regression test: with an
// environment probe that never returns, Open still answers — OpenNotARepository
// for a non-repository and OpenOK for a repository — well inside the probe's
// 5 s timeout. Before the fix, Open resolved the environment first and paid
// the full timeout before answering either way.
func TestOpenDoesNotWaitForEnvResolution(t *testing.T) {
	dir := t.TempDir()
	hang := filepath.Join(dir, "hang-shell")
	if err := writeFileExec(hang, "#!/bin/sh\nsleep 1000\n"); err != nil {
		t.Fatal(err)
	}
	f := NewFactory(WithShell(hang))
	// The background attempt must stay in flight through the opens below —
	// that is the "never returns" premise. Stop afterwards, and bound it:
	// with a broken Stop this test hangs rather than ending with an orphan.
	defer func() {
		stopDone := make(chan struct{})
		go func() { f.Stop(); close(stopDone) }()
		select {
		case <-stopDone:
		case <-time.After(5 * time.Second):
			t.Error("Stop did not cancel the hanging environment resolution")
		}
	}()

	repoDir := newGitRepo(t) // the probe falls back to the process PATH
	for _, c := range []struct {
		name string
		cwd  string
		want git.OpenState
	}{
		{"non-repository", t.TempDir(), git.OpenNotARepository},
		{"repository", repoDir, git.OpenOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				repo, outcome, err := f.Open(context.Background(), c.cwd)
				if err == nil && outcome.State != c.want {
					err = fmt.Errorf("state = %s, want %s", outcome.State, c.want)
				}
				if err == nil && c.want == git.OpenOK && repo == nil {
					err = errors.New("ok outcome with a nil repo")
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Open waited on the environment probe (probe timeout is 5 s)")
			}
		})
	}
}

// TestOpenFailureNotReattemptedPerOpen: N opens against a failing environment
// probe cost one attempt, not N. The resolution runs once, in the background
// from construction; the remembered failure is served for the cooldown
// instead of being re-attempted by every open (nocx-6pz0).
func TestOpenFailureNotReattemptedPerOpen(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "fail-shell")
	count := filepath.Join(dir, "count")
	if err := writeFileExec(shell, "#!/bin/sh\nprintf x >> "+count+"\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	f := NewFactory(WithShell(shell))
	defer f.Stop()

	// The background attempt must have run before we count; poll its marker.
	testwait.WaitFor(t, "the background resolution to run", func() bool {
		// #nosec G304 — the count path is a test TempDir.
		b, err := os.ReadFile(count)
		return err == nil && len(b) > 0
	})

	for i := range 5 {
		_, outcome, err := f.Open(context.Background(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if outcome.State != git.OpenNotARepository {
			t.Fatalf("open %d: state = %s, want notARepository", i, outcome.State)
		}
		if outcome.EnvState != git.EnvDegraded {
			t.Fatalf("open %d: EnvState = %s, want degraded (the failure is remembered)", i, outcome.EnvState)
		}
	}
	// #nosec G304 — the count path is a test TempDir.
	if b, _ := os.ReadFile(count); len(b) != 1 {
		t.Fatalf("the failing probe ran %d times across 5 opens, want 1", len(b))
	}
}

// TestEnvStateSettlesResolvedWithoutReopen is the interval, both ends
// (nocx-69ey, AGENTS.md rule 3): an open that lands while the background
// resolution is still in flight reports degraded, and once the resolution
// settles the SAME repo reports resolved — the panel can withdraw its
// warning without re-opening. The shell is gated on marker files, so the
// test never races the resolution: it cannot settle before the open, and
// the test controls when it does.
func TestEnvStateSettlesResolvedWithoutReopen(t *testing.T) {
	repoDir := newGitRepo(t)
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	shell := filepath.Join(dir, "gated-shell")
	body := "#!/bin/sh\n" +
		"printf started > " + started + "\n" +
		"while [ ! -f " + release + " ]; do sleep 0.01; done\n" +
		"export PATH=/usr/bin\n" +
		dumpEnvScript
	if err := writeFileExec(shell, body); err != nil {
		t.Fatal(err)
	}
	f := NewFactory(WithShell(shell))
	defer f.Stop()

	// The background attempt is in flight (gated): the resolution cannot
	// settle until the test releases it, so the open below is guaranteed
	// to land in the pre-settle window.
	testwait.WaitFor(t, "the background resolution to start", func() bool {
		// #nosec G304 — the marker path is a test TempDir.
		_, err := os.Stat(started)
		return err == nil
	})

	repo, outcome, err := f.Open(context.Background(), repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("State = %s, want ok", outcome.State)
	}
	if outcome.EnvState != git.EnvDegraded {
		t.Fatalf("open EnvState = %s, want degraded (the resolution is still in flight)", outcome.EnvState)
	}

	// Release the resolution; it settles to resolved.
	// #nosec G306 — the marker path is a test TempDir.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.env.waitSettled()

	// The SAME repo now reports resolved — no re-open (nocx-69ey).
	state, reason := repo.EnvState()
	if state != git.EnvResolved {
		t.Fatalf("EnvState() = %s (%s), want resolved once the resolution settled", state, reason)
	}
}

// TestCommitRunsWithResolvedEnvironment is D6's paired success test
// (AGENTS.md rule 2): on an ordinary machine the environment IS resolved, and
// the commit runs with it. Every behavior the commit path needs lives in the
// SHELL's output — the fake git on PATH, FAKE_STATUS=staged so the preflight
// sees staged work, FAKE_COMMIT=ok — so a CommitOK proves the resolved
// environment reached the git child: under the fallback environment the
// preflight would see nothing staged and refuse.
func TestCommitRunsWithResolvedEnvironment(t *testing.T) {
	fakeDir := writeFakeGit(t)
	repo := t.TempDir()
	shell := filepath.Join(t.TempDir(), "ok-shell")
	// The resolver runs `shell -i -c "printf <marker>; exec env -0"`; the
	// script must reach that dump, or the resolver sees no environment.
	body := "#!/bin/sh\n" +
		"export PATH=" + fakeDir + ":$PATH\n" +
		"export FAKE_GIT_LOG=" + filepath.Join(fakeDir, "argv.log") + "\n" +
		"export FAKE_TOPLEVEL=" + repo + "\n" +
		"export FAKE_GITDIR=" + filepath.Join(repo, ".git") + "\n" +
		"export FAKE_STATUS=staged\n" +
		"export FAKE_COMMIT=ok\n" +
		dumpEnvScript
	if err := writeFileExec(shell, body); err != nil {
		t.Fatal(err)
	}
	f := NewFactory(WithShell(shell))
	defer f.Stop()

	// Join the background attempt: on an ordinary machine it resolves. If it
	// did not, the commit below could not prove anything.
	env, state, reason := f.env.resolve(context.Background())
	if state != git.EnvResolved {
		t.Fatalf("environment not resolved on an ordinary machine: %s (%s)", state, reason)
	}

	repo2, outcome, err := f.Open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("State = %s", outcome.State)
	}
	if outcome.EnvState != git.EnvResolved {
		t.Fatalf("EnvState = %s, want resolved", outcome.EnvState)
	}

	res, err := repo2.Commit(context.Background(), "subject", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != git.CommitOK {
		t.Fatalf("Commit state = %s", res.State)
	}

	committed := false
	for _, entry := range fakeGitLog(t, env) {
		if len(entry) > 0 && entry[0] == "commit" {
			committed = true
		}
	}
	if !committed {
		t.Fatal("the fake git never saw a commit invocation")
	}
}
