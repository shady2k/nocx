package hostsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// fixtureRepo builds a real repository and returns its path. dirty commits
// one file and then modifies it, so status carries an unstaged entry; clean
// leaves the committed tree untouched, so every status list is empty.
// internal/git/local's own fixture is unexported, so this is the same recipe
// inline rather than a second implementation with a different shape.
func fixtureRepo(t *testing.T, dirty bool) string {
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
		cmd := exec.Command(args[0], args[1:]...) // #nosec G204 — gitBin is LookPath-resolved; args are test literals
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run(gitBin, "init", "-q")
	run(gitBin, "config", "commit.gpgsign", "false")
	// #nosec G306 — a repository working-tree file, not a secret
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(gitBin, "add", "file.txt")
	run(gitBin, "commit", "-m", "initial")
	if dirty {
		// #nosec G306 — a repository working-tree file, not a secret
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stubFactory lets the service be exercised against scripted outcomes, for
// the paths a real factory's own tests already own (the capability probe).
type stubFactory struct {
	repo    git.Repo
	outcome git.OpenOutcome
	err     error
}

func (f *stubFactory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	return f.repo, f.outcome, f.err
}

func openThroughService(t *testing.T, svc *hostsvc.Service, cwd string) hostsvc.OpenResult {
	t.Helper()
	result, err := svc.Call(context.Background(), "open", mustJSON(t, hostsvc.OpenParams{Cwd: cwd}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	openResult, ok := result.(hostsvc.OpenResult)
	if !ok {
		t.Fatalf("open result is %T, want hostsvc.OpenResult", result)
	}
	return openResult
}

func statusThroughService(t *testing.T, svc *hostsvc.Service, id string) git.Status {
	t.Helper()
	result, err := svc.Call(context.Background(), "status", mustJSON(t, hostsvc.BindingParams{BindingID: id}))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	st, ok := result.(git.Status)
	if !ok {
		t.Fatalf("status result is %T, want git.Status", result)
	}
	return st
}

// TestServiceStatusMatchesLocal is the contract in one assertion: the panel
// must say the same thing on both machines, so the service is only correct
// if it agrees with the local implementation on the same repository.
func TestServiceStatusMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t, true)
	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	repoLocal, outcome, err := factory.Open(context.Background(), dir)
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

	got := statusThroughService(t, svc, openThroughService(t, svc, dir).BindingID)
	if !reflect.DeepEqual(want, got) {
		w, _ := json.MarshalIndent(want, "", "  ")
		g, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("service disagrees with local:\nwant: %s\ngot:  %s", w, g)
	}
}

// stubRepo is a git.Repo whose only live method is EnvState; everything else
// answers an explicit error, so an unexpected service call surfaces as a
// failure rather than a silent nil.
type stubRepo struct {
	state  git.EnvState
	reason string
}

func (r *stubRepo) Status(ctx context.Context) (git.Status, error) {
	return git.Status{}, errors.New("stubRepo: Status not stubbed")
}
func (r *stubRepo) EnvState() (git.EnvState, string) { return r.state, r.reason }
func (r *stubRepo) Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error) {
	return git.Diff{}, errors.New("stubRepo: Diff not stubbed")
}

func (r *stubRepo) Log(ctx context.Context, max int) (git.Log, error) {
	return git.Log{}, errors.New("stubRepo: Log not stubbed")
}

func (r *stubRepo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	return git.Status{}, errors.New("stubRepo: Stage not stubbed")
}

func (r *stubRepo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	return git.Status{}, errors.New("stubRepo: Unstage not stubbed")
}

func (r *stubRepo) StageAll(ctx context.Context) (git.Status, error) {
	return git.Status{}, errors.New("stubRepo: StageAll not stubbed")
}

func (r *stubRepo) UnstageAll(ctx context.Context) (git.Status, error) {
	return git.Status{}, errors.New("stubRepo: UnstageAll not stubbed")
}

func (r *stubRepo) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	return git.CommitOutcome{}, errors.New("stubRepo: Commit not stubbed")
}

func (r *stubRepo) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	return git.HeadMessage{}, errors.New("stubRepo: HeadMessage not stubbed")
}

func (r *stubRepo) RemoteURL(ctx context.Context) (string, error) {
	return "", errors.New("stubRepo: RemoteURL not stubbed")
}
func (r *stubRepo) Close() error { return nil }

// TestEnvStateMatchesLocal pins envState to exactly what a local-shaped repo
// reports. The real factory's env state settles in the background between
// calls (nocx-6pz0), which would make two moments disagree on a value the
// mapping itself never changed — so the mapping is asserted against a stub
// carrying a fixed (state, reason) pair.
func TestEnvStateMatchesLocal(t *testing.T) {
	svc := hostsvc.New(&stubFactory{
		repo:    &stubRepo{state: git.EnvDegraded, reason: "the login shell did not answer"},
		outcome: git.OpenOutcome{State: git.OpenOK},
	})
	id := openThroughService(t, svc, "/repo").BindingID

	result, err := svc.Call(context.Background(), "envState", mustJSON(t, hostsvc.BindingParams{BindingID: id}))
	if err != nil {
		t.Fatalf("envState: %v", err)
	}
	env, ok := result.(hostsvc.EnvStateResult)
	if !ok {
		t.Fatalf("envState result is %T, want hostsvc.EnvStateResult", result)
	}
	if env.State != git.EnvDegraded || env.Reason != "the login shell did not answer" {
		t.Fatalf("envState: want {%s %q}, got {%s %q}", git.EnvDegraded, "the login shell did not answer", env.State, env.Reason)
	}
}

func TestOpenNotARepository(t *testing.T) {
	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	result := openThroughService(t, svc, t.TempDir())
	if result.State != git.OpenNotARepository {
		t.Fatalf("open outcome = %s, want notARepository", result.State)
	}
	if result.BindingID != "" {
		t.Fatalf("a failed open must not mint a binding, got %q", result.BindingID)
	}
}

// TestOpenGitUnavailable drives the capability-probe outcome through the
// service; the probe itself is internal/git/local's own tested domain.
func TestOpenGitUnavailable(t *testing.T) {
	svc := hostsvc.New(&stubFactory{
		outcome: git.OpenOutcome{State: git.OpenGitUnavailable},
	})
	result := openThroughService(t, svc, "/some/dir")
	if result.State != git.OpenGitUnavailable {
		t.Fatalf("open outcome = %s, want gitUnavailable", result.State)
	}
	if result.BindingID != "" {
		t.Fatalf("a failed open must not mint a binding, got %q", result.BindingID)
	}
}

// TestEmptyStatusListsMarshalAsArrays pins the wire contract the first
// contract schema in this repository caught: an empty list marshals as [],
// never null. The status result crosses the wire as git.Status, so the
// marshalled form of the service's answer is the wire form.
func TestEmptyStatusListsMarshalAsArrays(t *testing.T) {
	dir := fixtureRepo(t, false)
	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	st := statusThroughService(t, svc, openThroughService(t, svc, dir).BindingID)
	if st.Staged == nil || st.Unstaged == nil || st.Conflicted == nil {
		t.Fatalf("status lists must be non-nil: %+v", st)
	}
	wire, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"staged", "unstaged", "conflicted"} {
		if strings.TrimSpace(string(fields[key])) == "null" {
			t.Fatalf("%q marshals as null: %s", key, wire)
		}
	}
}

// TestUnknownBindingIsAnError covers the seam's failure path: an id that
// open never issued must not silently serve a repository.
func TestUnknownBindingIsAnError(t *testing.T) {
	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	_, err := svc.Call(context.Background(), "status", mustJSON(t, hostsvc.BindingParams{BindingID: "no-such-binding"}))
	if err == nil {
		t.Fatal("status on an unknown binding must error")
	}
	_, err = svc.Call(context.Background(), "envState", mustJSON(t, hostsvc.BindingParams{BindingID: "no-such-binding"}))
	if err == nil {
		t.Fatal("envState on an unknown binding must error")
	}
}

// TestOpenIsDeterministicPerDirectory pins the binding id to the directory:
// a second open of the same directory returns the same id, so it replaces —
// not multiplies — the held repository. Only the deterministic identity is
// compared: EnvState and EnvReason legitimately move as the factory's
// background resolution settles between the two opens (nocx-6pz0).
func TestOpenIsDeterministicPerDirectory(t *testing.T) {
	dir := fixtureRepo(t, false)
	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	first := openThroughService(t, svc, dir)
	second := openThroughService(t, svc, dir)
	if first.BindingID != second.BindingID || first.BindingID == "" {
		t.Fatalf("open must be deterministic per directory: %q then %q", first.BindingID, second.BindingID)
	}
	if first.State != git.OpenOK || first.Toplevel != second.Toplevel {
		t.Fatalf("the resolved identity must be stable across opens: %+v vs %+v", first, second)
	}
}

// TestOpenErrorSurfaces pins the factory-error path: an open failure is a
// service failure, not a silent empty outcome.
func TestOpenErrorSurfaces(t *testing.T) {
	boom := errors.New("factory exploded")
	svc := hostsvc.New(&stubFactory{err: boom})
	_, err := svc.Call(context.Background(), "open", mustJSON(t, hostsvc.OpenParams{Cwd: "/x"}))
	if !errors.Is(err, boom) {
		t.Fatalf("want the factory error through, got %v", err)
	}
}
