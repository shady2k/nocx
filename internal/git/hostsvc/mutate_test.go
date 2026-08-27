// The mutation half of the git service (plan Task 8): stage, unstage,
// stageAll, unstageAll, commit, headMessage and remoteURL served over
// internal/git/local. The load-bearing assertions are D8 (paths and
// messages reach git through stdin, never argv), D11 (a cancel naming an
// in-flight mutation is refused and the mutation completes) and the wire
// guards (empty lists marshal as [], never null). The service tests
// against the real host when the behavior is the host's (the cancel
// refusal), and against the service directly otherwise.
package hostsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	// #nosec G306 — a repository working-tree file or a test marker, not a secret
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withIdentity writes a repo-local git identity, so commits through the
// real factory — whose environment is resolved from the login shell and
// therefore carries the developer's real HOME and gitconfig — find
// user.name and user.email in the repository itself. Hermetic: the commit
// succeeds or fails on the repository's own config, never on whoever runs
// the test.
func withIdentity(t *testing.T, dir string) {
	t.Helper()
	gitBin, env := repoTooling(t)
	for _, kv := range [][]string{
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@nocx.invalid"},
	} {
		cmd := exec.Command(gitBin, kv...) // #nosec G204 — gitBin is LookPath-resolved; args are fixed literals
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", kv, err, out)
		}
	}
}

// newService builds the git service over the real factory, exactly as the
// helper binary does; the tests' repositories carry their own identity
// (withIdentity), so a commit's success never depends on the environment
// the factory resolves from the login shell.
func newService(t *testing.T) *hostsvc.Service {
	t.Helper()
	return hostsvc.New(localgit.NewFactory())
}

func stageThroughService(t *testing.T, svc *hostsvc.Service, id string, paths []string) git.Status {
	t.Helper()
	result, err := svc.Call(context.Background(), "stage", mustJSON(t, hostsvc.StageParams{BindingID: id, Paths: paths}))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	st, ok := result.(git.Status)
	if !ok {
		t.Fatalf("stage result is %T, want git.Status", result)
	}
	return st
}

func commitThroughService(t *testing.T, svc *hostsvc.Service, id, msg string, amend bool) git.CommitOutcome {
	t.Helper()
	result, err := svc.Call(context.Background(), "commit", mustJSON(t, hostsvc.CommitParams{BindingID: id, Message: msg, Amend: amend}))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	out, ok := result.(git.CommitOutcome)
	if !ok {
		t.Fatalf("commit result is %T, want git.CommitOutcome", result)
	}
	return out
}

// headMessageOf reads HEAD's message straight from the repository — the
// ground truth a commit message must match byte for byte.
func headMessageOf(t *testing.T, dir string) string {
	t.Helper()
	gitBin, env := repoTooling(t)
	cmd := exec.Command(gitBin, "log", "-1", "--format=%B") // #nosec G204 — gitBin is LookPath-resolved; args are fixed literals
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	return string(out)
}

// TestStageAcceptsAHostilePath is D8 through the service: a path with a
// space, a quote, a leading dash and a newline stages exactly itself. A
// pathspec interpolated into argv, or read as a glob, could never do this.
func TestStageAcceptsAHostilePath(t *testing.T) {
	dir := fixtureRepo(t, false)
	hostile := "a file -with 'quotes' and\na newline.txt"
	writeFile(t, filepath.Join(dir, hostile), "x")

	svc := newService(t)
	id := openThroughService(t, svc, dir)
	st := stageThroughService(t, svc, id.BindingID, []string{hostile})

	if len(st.Staged) != 1 || st.Staged[0].Path != hostile {
		t.Fatalf("want exactly %q staged, got %+v", hostile, st.Staged)
	}
}

// TestCommitMessageReachesHEADByteForByte is D8's commit half: a message
// with newlines, quotes and non-ASCII crosses the service and becomes
// HEAD's message exactly — git's -F - stored what the helper supplied,
// nothing added, nothing parsed.
func TestCommitMessageReachesHEADByteForByte(t *testing.T) {
	dir := fixtureRepo(t, true)
	msg := "subject with 'quotes' and \"double\"\n\nbody line\nnéwlines ✓"

	svc := newService(t)
	withIdentity(t, dir)
	id := openThroughService(t, svc, dir)
	stageThroughService(t, svc, id.BindingID, []string{"file.txt"})
	out := commitThroughService(t, svc, id.BindingID, msg, false)
	if out.State != git.CommitOK {
		t.Fatalf("commit state = %s, want ok", out.State)
	}
	want := msg + "\n\n" // git appends one trailing newline to stored messages; log -B adds its own
	if got := headMessageOf(t, dir); got != want {
		t.Fatalf("HEAD message is not the one sent:\nwant %q\ngot  %q", want, got)
	}
}

// TestMutationStatusListsMarshalAsArrays pins the wire guard for the
// mutation results: the fresh status a mutation returns has empty lists
// where the repository is clean on that side, and an empty list marshals
// as [], never null — the same defect the first contract schema in this
// repository caught for the status result.
func TestMutationStatusListsMarshalAsArrays(t *testing.T) {
	dir := fixtureRepo(t, true)
	svc := newService(t)
	id := openThroughService(t, svc, dir)
	st := stageThroughService(t, svc, id.BindingID, []string{"file.txt"})

	raw := mustJSON(t, st)
	for _, field := range []string{`"Unstaged":null`, `"Conflicted":null`} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("an empty list marshalled as null: %s", raw)
		}
	}
}

// gitInRepo runs one git plumbing command in dir and returns its trimmed
// output. It is the ground truth these comparisons are set up and checked
// against: neither implementation under test may also be the thing that
// prepares or verifies the comparison.
func gitInRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	gitBin, env := repoTooling(t)
	cmd := exec.Command(gitBin, args...) // #nosec G204 — gitBin is LookPath-resolved; args are test literals
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// resetIndex puts the index back to HEAD, leaving the working tree alone —
// the fixture's dirty state exactly as the first implementation found it.
func resetIndex(t *testing.T, dir string) {
	t.Helper()
	gitInRepo(t, dir, "reset", "-q")
}

// resetToCommit puts HEAD, the index and the working tree back to commit and
// re-dirties the fixture's file, so the second commit is made from the same
// parent, over the same change, as the first.
func resetToCommit(t *testing.T, dir, commit string) {
	t.Helper()
	gitInRepo(t, dir, "reset", "-q", "--hard", commit)
	writeFile(t, filepath.Join(dir, "file.txt"), "two\n")
}

// headsOf reads the repository's current head in the two shapes the domain
// types carry it: git.CommitOutcome.Head is rev-parse --short, and
// git.Status.Head is the first seven hex digits of the full oid.
func headsOf(t *testing.T, dir string) (outcomeHead, statusHead string) {
	t.Helper()
	full := gitInRepo(t, dir, "rev-parse", "HEAD")
	if len(full) < 7 {
		t.Fatalf("rev-parse HEAD returned %q", full)
	}
	return gitInRepo(t, dir, "rev-parse", "--short", "HEAD"), full[:7]
}

// TestStageMatchesLocal and TestCommitMatchesLocal are the mutation half
// of the one contract: the panel must say the same thing on both machines,
// so a mutation through the service is only correct if it agrees with the
// local implementation on the same repository — field by field.
//
// "On the same repository" is load-bearing, and for a while the fixture did
// not deliver it (nocx-8j7b5): each test built a repository per side and
// compared the two results with reflect.DeepEqual. A commit hash covers the
// author and committer timestamps at one-second granularity, so two
// fixtures with byte-identical content agree on Head only while both their
// initial commits land inside one second. On an idle machine they always
// do; under the load of a full package run they sometimes do not, and the
// pair failed on backend-linux with every field equal but the hash. So both
// implementations now run against ONE repository, each from the same
// starting state, restored in between by git itself — which makes every
// field including Head legitimately comparable, and keeps DeepEqual's teeth
// rather than loosening the assertion to accommodate a fixture defect. One
// factory serves both sides for the same reason, matching
// TestServiceStatusMatchesLocal: the environment git runs in is then also
// shared, so the only variable left is the service against local.
func TestStageMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t, true)
	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	id := openThroughService(t, svc, dir)
	want := stageThroughService(t, svc, id.BindingID, []string{"file.txt"})

	// The local side must perform the same mutation, not observe one
	// already performed — so the index goes back to where the service
	// found it. Nothing here moves HEAD, so both statuses report the one
	// head this repository has.
	resetIndex(t, dir)

	repoLocal, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	got, err := repoLocal.Stage(context.Background(), []string{"file.txt"})
	if err != nil {
		t.Fatalf("local stage: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("service mutation disagrees with local (-service +local):\nwant: %+v\ngot:  %+v", want, got)
	}
}

func TestCommitMatchesLocal(t *testing.T) {
	const msg = "same subject\n\nsame body"

	dir := fixtureRepo(t, true)
	withIdentity(t, dir)
	base := gitInRepo(t, dir, "rev-parse", "HEAD")

	factory := localgit.NewFactory()
	defer factory.Stop()
	svc := hostsvc.New(factory)

	id := openThroughService(t, svc, dir)
	stageThroughService(t, svc, id.BindingID, []string{"file.txt"})
	want := commitThroughService(t, svc, id.BindingID, msg, false)
	wantHead, wantStatusHead := headsOf(t, dir)

	// Back to the parent, with the same change to commit again: the local
	// side's commit has the same parent, the same tree and the same
	// message as the service's.
	resetToCommit(t, dir, base)

	repoLocal, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	if _, err = repoLocal.Stage(context.Background(), []string{"file.txt"}); err != nil {
		t.Fatalf("local stage: %v", err)
	}
	got, err := repoLocal.Commit(context.Background(), msg, false)
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}
	gotHead, gotStatusHead := headsOf(t, dir)

	// The two head fields are the only ones one repository cannot make
	// comparable, and the reason is permanent rather than a fixture
	// accident: a commit hash covers the commit's OWN author and committer
	// timestamps, so two commits made at two moments differ even with an
	// identical parent, tree and message. The seam that would pin them —
	// GIT_AUTHOR_DATE through local.WithEnv — is declared in that package's
	// options_test.go and therefore does not exist outside it. So each side
	// asserts its head against the repository's real HEAD after its own
	// commit: the field is excluded from the cross-check, never from the
	// test, and "" would fail both assertions.
	if want.Head != wantHead {
		t.Fatalf("service commit head = %q, the repository's is %q", want.Head, wantHead)
	}
	if want.Status.Head != wantStatusHead {
		t.Fatalf("service post-commit status head = %q, the repository's is %q", want.Status.Head, wantStatusHead)
	}
	if got.Head != gotHead {
		t.Fatalf("local commit head = %q, the repository's is %q", got.Head, gotHead)
	}
	if got.Status.Head != gotStatusHead {
		t.Fatalf("local post-commit status head = %q, the repository's is %q", got.Status.Head, gotStatusHead)
	}

	// Everything else — the state, the staleness flag, the output and every
	// field of the fresh status — is the panel's truth and must agree.
	wantRest, gotRest := want, got
	wantRest.Head, gotRest.Head = "", ""
	wantRest.Status.Head, gotRest.Status.Head = "", ""
	if !reflect.DeepEqual(wantRest, gotRest) {
		t.Fatalf("service commit disagrees with local, head aside:\nwant: %+v\ngot:  %+v", wantRest, gotRest)
	}
}

// TestStageAllRefusedWhileConflictedThroughService is D19 through the
// service: while a merge is unresolved the panel does not touch the index,
// and the refusal crosses as the typed *git.ErrConflicted — path intact —
// the error the transport switches on.
func TestStageAllRefusedWhileConflictedThroughService(t *testing.T) {
	dir := conflictedRepo(t)
	svc := newService(t)
	id := openThroughService(t, svc, dir)

	_, err := svc.Call(context.Background(), "stageAll", mustJSON(t, hostsvc.BindingParams{BindingID: id.BindingID}))
	var c *git.ErrConflicted
	if !errors.As(err, &c) {
		t.Fatalf("StageAll returned %T, want *git.ErrConflicted", err)
	}
	if c.Path != "f.txt" {
		t.Fatalf("ErrConflicted.Path = %q, want f.txt", c.Path)
	}
}

// conflictedRepo builds a repository in a conflicted merge state, the
// recipe internal/git/local's own tests use, inline because the fixture is
// unexported.
func conflictedRepo(t *testing.T) string {
	t.Helper()
	gitBin, env := repoTooling(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...) // #nosec G204 — gitBin is LookPath-resolved; args are test literals
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "topic")
	writeFile(t, filepath.Join(dir, "f.txt"), "topic\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "topic")
	run("checkout", "-q", "master")
	writeFile(t, filepath.Join(dir, "f.txt"), "master\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "master")
	merge := exec.Command(gitBin, "merge", "topic") // #nosec G204 — gitBin is LookPath-resolved; args are test literals
	merge.Dir = dir
	merge.Env = env
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", out)
	}
	return dir
}

// ── D11: the cancel refusal, against the REAL host ──────────────────────

// slowCommitHook writes a pre-commit hook that writes started, then blocks
// until release appears — the only way to hold a commit in flight
// deterministically (the repo's timing rule: wait on observables, never on
// a duration). Absolute paths, so the hook depends on nothing but sh.
func slowCommitHook(t *testing.T, dir, started, release string) {
	t.Helper()
	script := "#!/bin/sh\ntouch '" + started + "'\nwhile [ ! -f '" + release + "' ]; do sleep 0.02; done\n"
	// #nosec G306 — a hook in a test repository, deliberately executable
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The raw-frame machinery for driving the real host: the sentinel line is
// read from the stream before the frame decoder starts, exactly as the
// client does, so the two readers never race for the same bytes.
func writeRawFrame(t *testing.T, w io.Writer, ty proto.FrameType, payload []byte) {
	t.Helper()
	if _, err := w.Write(proto.EncodeFrame(ty, 0, 0, payload)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

type rawFrame struct {
	ty      proto.FrameType
	payload []byte
}

func readRawSentinel(t *testing.T, r io.Reader) {
	t.Helper()
	want := "nocx-helper " + proto.Version + " ready\n"
	got := make([]byte, 0, len(want))
	one := make([]byte, 1)
	for {
		if _, err := r.Read(one); err != nil {
			t.Fatalf("reading sentinel: %v", err)
		}
		got = append(got, one[0])
		if one[0] == '\n' {
			break
		}
	}
	if string(got) != want {
		t.Fatalf("sentinel: want %q, got %q", want, got)
	}
}

func startRawReader(t *testing.T, r io.Reader) <-chan rawFrame {
	t.Helper()
	ch := make(chan rawFrame, 16)
	go func() {
		d := proto.NewDecoder(func(ty proto.FrameType, seq, ack uint32, p []byte) {
			ch <- rawFrame{ty: ty, payload: append([]byte(nil), p...)}
		}, func(int) {})
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if ferr := d.Feed(buf[:n]); ferr != nil {
					return
				}
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	return ch
}

func readRawFrame(t *testing.T, ch <-chan rawFrame) rawFrame {
	t.Helper()
	f, ok := <-ch
	if !ok {
		t.Fatal("output stream ended before the frame arrived")
	}
	return f
}

func readRawResponse(t *testing.T, ch <-chan rawFrame) proto.Response {
	t.Helper()
	f := readRawFrame(t, ch)
	if f.ty != proto.TypeResponse {
		t.Fatalf("want a response frame, got type %v", f.ty)
	}
	var resp proto.Response
	if err := json.Unmarshal(f.payload, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// TestCancelRefusedForMutationAndItCompletes is D11 end to end against the
// real host serving the real git service: a cancel naming an in-flight
// commit is refused with ErrCodeCancelRefused — a refusal is a fact the
// caller can act on, a no-op looks like success — and the mutation runs to
// completion, its response arriving after the refusal.
func TestCancelRefusedForMutationAndItCompletes(t *testing.T) {
	dir := fixtureRepo(t, true)
	withIdentity(t, dir)
	started := filepath.Join(t.TempDir(), "hook-started")
	release := filepath.Join(t.TempDir(), "hook-release")
	slowCommitHook(t, dir, started, release)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "testhash", "instance-1",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.Register(hostsvc.New(localgit.NewFactory()))
	serveCh := make(chan error, 1)
	go func() { serveCh <- h.Serve(context.Background()) }()
	t.Cleanup(func() { _ = inW.Close(); _ = outW.Close() })

	writeRawFrame(t, inW, proto.TypeHello, mustJSON(t, proto.Hello{Version: proto.Version, Nonce: "n", Corr: "c"}))
	readRawSentinel(t, outR)
	frames := startRawReader(t, outR)
	okFrame := readRawFrame(t, frames)
	if okFrame.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK after the sentinel, got type %v", okFrame.ty)
	}
	var helloOK proto.HelloOK
	if err := json.Unmarshal(okFrame.payload, &helloOK); err != nil {
		t.Fatalf("hello-ok: %v", err)
	}
	if helloOK.Nonce != "n" || helloOK.ContentHash != "testhash" {
		t.Fatalf("hello-ok mismatch: %+v", helloOK)
	}

	openID := uint64(1)
	writeRawFrame(t, inW, proto.TypeRequest, mustJSON(t, proto.Request{
		ID: openID, Service: "git", Op: "open",
		Params: mustJSON(t, hostsvc.OpenParams{Cwd: dir}), Corr: "c1",
	}))
	openResp := readRawResponse(t, frames)
	if openResp.Error != nil {
		t.Fatalf("open refused: %+v", openResp.Error)
	}
	var openResult hostsvc.OpenResult
	if err := json.Unmarshal(openResp.Result, &openResult); err != nil {
		t.Fatalf("open result: %v", err)
	}
	bid := openResult.BindingID

	stageID := uint64(2)
	writeRawFrame(t, inW, proto.TypeRequest, mustJSON(t, proto.Request{
		ID: stageID, Service: "git", Op: "stage",
		Params: mustJSON(t, hostsvc.StageParams{BindingID: bid, Paths: []string{"file.txt"}}), Corr: "c2",
	}))
	if resp := readRawResponse(t, frames); resp.Error != nil {
		t.Fatalf("stage refused: %+v", resp.Error)
	}

	commitID := uint64(3)
	writeRawFrame(t, inW, proto.TypeRequest, mustJSON(t, proto.Request{
		ID: commitID, Service: "git", Op: "commit",
		Params: mustJSON(t, hostsvc.CommitParams{BindingID: bid, Message: "subject", Amend: false}), Corr: "c3",
	}))
	waitForFile(t, started) // the commit is now in flight inside the helper

	writeRawFrame(t, inW, proto.TypeCancel, mustJSON(t, struct {
		ID uint64 `json:"id"`
	}{ID: commitID}))

	refusalResp := readRawResponse(t, frames)
	if refusalResp.Error == nil || refusalResp.Error.Code != proto.ErrCodeCancelRefused {
		t.Fatalf("cancel was not refused: %+v", refusalResp.Error)
	}

	writeFile(t, release, "")
	commitResp := readRawResponse(t, frames)
	if commitResp.Error != nil {
		t.Fatalf("the mutation must complete despite the refused cancel: %+v", commitResp.Error)
	}
	var outcome git.CommitOutcome
	if err := json.Unmarshal(commitResp.Result, &outcome); err != nil {
		t.Fatalf("commit result: %v", err)
	}
	if outcome.State != git.CommitOK {
		t.Fatalf("commit state = %s, want ok — the mutation completed", outcome.State)
	}
	if got := headMessageOf(t, dir); got != "subject\n\n" {
		t.Fatalf("HEAD message = %q, want the committed subject", got)
	}

	select {
	case err := <-serveCh:
		t.Fatalf("host exited early: %v", err)
	default:
	}
}
