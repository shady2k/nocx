package transport

// Behavioral tests for the git.* control plane (spec §5.2): the two guards
// on the wire, the ownership-transfer rule (spec §5.1) whose leak class has
// been caught twice in review and never in code, the unborn-branch unstage
// state, and the git.changed teardown addressing — closing a session must
// deliver the notification to the closing connection, which is the fix
// nocx-lzfb's files side still needs. The contract-conformance rows live in
// ws_contract_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// ── test doubles ──────────────────────────────────────────────────────────

// stubGitRepo is a scripted git.Repo for the transport tests: every method
// returns a canned domain value, so the tests exercise the WIRE, not git.
// The real git is covered in internal/git (99 tests) and in the one
// real-git round trip below.
type stubGitRepo struct {
	mu        sync.Mutex
	closed    bool
	status    git.Status
	statusErr error

	diff       git.Diff
	diffErr    error
	mutateErr  error // Stage / StageAll / UnstageAll
	unstageErr error // Unstage — the one operation with a renderable refusal state
	log        git.Log
	logErr     error
	logMax     int // the bound the last Log call carried — policy check
	commit     git.CommitOutcome
	commitErr  error
	headMsg    git.HeadMessage
	headMsgErr error
	remoteURL  string
	remoteErr  error
	envState   git.EnvState // the scripted environment fact (nocx-69ey)
	envReason  string
}

func (r *stubGitRepo) Log(_ context.Context, max int) (git.Log, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return git.Log{}, errors.New("stub: repo closed")
	}
	r.logMax = max
	return r.log, r.logErr
}

func (r *stubGitRepo) Status(_ context.Context) (git.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return git.Status{}, errors.New("stub: repo closed")
	}
	return r.status, r.statusErr
}

func (r *stubGitRepo) EnvState() (git.EnvState, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.envState, r.envReason
}

func (r *stubGitRepo) Diff(_ context.Context, _ string, _ git.Side, _ int64) (git.Diff, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.diff, r.diffErr
}

func (r *stubGitRepo) Stage(_ context.Context, _ []string) (git.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mutateErr != nil {
		return git.Status{}, r.mutateErr
	}
	return r.status, nil
}

func (r *stubGitRepo) Unstage(_ context.Context, _ []string) (git.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unstageErr != nil {
		return git.Status{}, r.unstageErr
	}
	return r.status, nil
}

func (r *stubGitRepo) StageAll(_ context.Context) (git.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mutateErr != nil {
		return git.Status{}, r.mutateErr
	}
	return r.status, nil
}

func (r *stubGitRepo) UnstageAll(_ context.Context) (git.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mutateErr != nil {
		return git.Status{}, r.mutateErr
	}
	return r.status, nil
}

func (r *stubGitRepo) Commit(_ context.Context, _ string, _ bool) (git.CommitOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commit, r.commitErr
}

func (r *stubGitRepo) HeadMessage(_ context.Context) (git.HeadMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headMsg, r.headMsgErr
}

func (r *stubGitRepo) RemoteURL(_ context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remoteURL, r.remoteErr
}

// Close is nil-receiver-safe on purpose: the Register-failure leak test
// drives Register's typed-nil refusal through the wire, and the handler's
// close-on-register-failure path must be able to call Close on it.
func (r *stubGitRepo) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func stubStatus() git.Status {
	added, deleted := 3, 1
	return git.Status{
		Branch:       "main",
		Head:         "abc1234",
		Staged:       []git.Entry{{Path: "staged.txt", X: 'M', Y: '.'}},
		Unstaged:     []git.Entry{{Path: "unstaged.txt", X: '.', Y: 'M', Added: &added, Deleted: &deleted}},
		Conflicted:   []git.Entry{},
		Total:        2,
		Completeness: git.CompletenessComplete,
	}
}

func stubOpenOutcome() git.OpenOutcome {
	return git.OpenOutcome{
		State:      git.OpenOK,
		Toplevel:   "/tmp/repo",
		GitDir:     "/tmp/repo/.git",
		GitVersion: "2.55.0",
		EnvState:   git.EnvResolved,
	}
}

// newStubGitRepo returns a repo whose defaults make every over-the-wire
// contract test pass: a populated status, an ok diff, an ok commit and an
// ok head message.
func newStubGitRepo() *stubGitRepo {
	return &stubGitRepo{
		status:    stubStatus(),
		diff:      git.Diff{State: git.DiffOK, Text: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n"},
		log:       stubLog(),
		commit:    git.CommitOutcome{State: git.CommitOK, Head: "abc1234", Status: stubStatus()},
		headMsg:   git.HeadMessage{State: git.HeadMessageOK, Message: "subject\n\nbody"},
		remoteURL: "git@github.com:shady2k/nocx.git",
		envState:  git.EnvResolved,
	}
}

func stubLog() git.Log {
	return git.Log{
		Entries: []git.LogEntry{{
			Hash:       "5738d62b66777a78af894c0708d3a7e8798a4d8d",
			ShortHash:  "5738d62",
			Subject:    "third",
			AuthorName: "Test Author",
			AuthoredAt: time.Date(2026, 8, 7, 12, 52, 40, 0, time.FixedZone("", 3*60*60)),
			Refs:       []string{"main"},
		}},
		Total:        1,
		Completeness: git.CompletenessComplete,
	}
}

// stubGitFactory is a scripted git.RepoFactory. mkRepo is called on every
// Open; the default mints a fresh stub repo so two opens get two bindings.
type stubGitFactory struct {
	mu      sync.Mutex
	mkRepo  func() git.Repo // nil → Open returns a nil repo
	outcome git.OpenOutcome
	err     error
	opens   int
}

func newStubGitFactory() *stubGitFactory {
	return &stubGitFactory{
		mkRepo:  func() git.Repo { return newStubGitRepo() },
		outcome: stubOpenOutcome(),
	}
}

func (f *stubGitFactory) Open(_ context.Context, _ string) (git.Repo, git.OpenOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.mkRepo == nil {
		return nil, f.outcome, f.err
	}
	return f.mkRepo(), f.outcome, f.err
}

// ── test env ──────────────────────────────────────────────────────────────

// gitTestEnv boots a WSServer wired with the git registry and connects one
// client. The repo factory is supplied per test through opts, the same way
// filesTestEnv takes provider options.
type gitTestEnv struct {
	ws   *WSServer
	conn *websocket.Conn
}

func newGitTestEnv(t *testing.T, opts ...WSServerOption) *gitTestEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	all := append([]WSServerOption{WithGitRegistry(git.New())}, opts...)
	ws := NewWSServer(logger, reg, all...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &gitTestEnv{ws: ws, conn: conn}
}

// openSession opens a local session over the wire and returns its
// server-authoritative sessionId.
func (e *gitTestEnv) openSession(t *testing.T, id int) string {
	t.Helper()
	return openSessionOnConn(t, e.ws, e.conn, id)
}

// openGitBinding opens a git binding over the wire and returns its
// bindingId, failing the test unless the open answers ok.
func (e *gitTestEnv) openGitBinding(t *testing.T, sid, cwd string, id int) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.conn, "git.open", map[string]any{
		"sessionId": sid,
		"cwd":       cwd,
	}, id)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("git.open: %+v", envelope.Error)
	}
	var got struct {
		State     string `json:"state"`
		BindingID string `json:"bindingId"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("git.open: decode result: %v", err)
	}
	if got.State != "ok" || got.BindingID == "" {
		t.Fatalf("git.open: state=%q bindingId=%q, want ok with a binding", got.State, got.BindingID)
	}
	return got.BindingID
}

// gitBindingCount is the transport bookkeeping size for a session — the
// in-package probe for "no binding was returned".
func (e *gitTestEnv) gitBindingCount(sid string) int {
	e.ws.gitMu.Lock()
	defer e.ws.gitMu.Unlock()
	return len(e.ws.gitBySession[session.ID(sid)])
}

// ── the two guards ────────────────────────────────────────────────────────

// TestGitOpen_RemoteSessionRefusedBeforeTheFactory is D3 on the wire: an
// SSH session answers the remoteUnsupported RESULT state, and the factory
// is never consulted (nothing is spawned).
func TestGitOpen_RemoteSessionRefusedBeforeTheFactory(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return ssh.NewStubChannel(logger), nil
		},
	})
	factory := newStubGitFactory()
	ws := NewWSServer(logger, reg,
		WithGitRegistry(git.New()),
		WithGitRepoFactory(factory),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(_ string) (string, *ssh.ConnectConfig, error) {
				return "host.example", &ssh.ConnectConfig{User: "test", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	openResp := jsonrpcCallWithID(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"kind": "ssh", "profileId": "ssh:test:1",
	}, 1)
	var openEnv struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(openResp, &openEnv); err != nil {
		t.Fatalf("open: unmarshal: %v", err)
	}
	if openEnv.Error != nil {
		t.Fatalf("open: %+v", openEnv.Error)
	}
	var openGot struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openEnv.Result, &openGot); err != nil {
		t.Fatalf("open: decode: %v", err)
	}

	resp := jsonrpcCallWithID(t, conn, "git.open", map[string]any{
		"sessionId": openGot.SessionID,
		"cwd":       "/some/cwd",
	}, 2)
	var got struct {
		Result struct {
			State string `json:"state"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if got.Error != nil {
		t.Fatalf("git.open: %+v", got.Error)
	}
	if got.Result.State != string(git.OpenRemoteUnsupported) {
		t.Errorf("state = %q, want %q", got.Result.State, git.OpenRemoteUnsupported)
	}
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 0 {
		t.Errorf("factory consulted %d times for a remote session, want 0 — the refusal must happen before anything is spawned", opens)
	}
}

// TestGitOpen_NoCwdRefusedBeforeTheFactory: no verified OSC 7 cwd means no
// repository (D2), decided from the caller's origin before the factory.
func TestGitOpen_NoCwdRefusedBeforeTheFactory(t *testing.T) {
	factory := newStubGitFactory()
	e := newGitTestEnv(t, WithGitRepoFactory(factory))
	sid := e.openSession(t, 1)

	resp := jsonrpcCallWithID(t, e.conn, "git.open", map[string]any{"sessionId": sid}, 2)
	var got struct {
		Result struct {
			State string `json:"state"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if got.Error != nil {
		t.Fatalf("git.open: %+v", got.Error)
	}
	if got.Result.State != string(git.OpenNoCwd) {
		t.Errorf("state = %q, want %q", got.Result.State, git.OpenNoCwd)
	}
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 0 {
		t.Errorf("factory consulted %d times without a cwd, want 0", opens)
	}
}

// ── the ownership-transfer rule (spec §5.1) ──────────────────────────────

// TestGitOpen_RefusingOutcomeClosesRepo is the leak test the brief demands:
// Open returns a live Repo on a refusing outcome — that repo belongs to
// nobody and must be closed before the refusal is returned, with no binding
// registered. This leak class has been caught twice in review and never in
// code.
func TestGitOpen_RefusingOutcomeClosesRepo(t *testing.T) {
	repo := newStubGitRepo()
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo:  func() git.Repo { return repo },
		outcome: git.OpenOutcome{State: git.OpenNotARepository},
	}))
	sid := e.openSession(t, 1)

	resp := jsonrpcCallWithID(t, e.conn, "git.open", map[string]any{
		"sessionId": sid, "cwd": "/tmp/not-a-repo",
	}, 2)
	var got struct {
		Result struct {
			State     string `json:"state"`
			BindingID string `json:"bindingId"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if got.Error != nil {
		t.Fatalf("git.open: %+v", got.Error)
	}
	if got.Result.State != string(git.OpenNotARepository) {
		t.Errorf("state = %q, want %q", got.Result.State, git.OpenNotARepository)
	}
	if got.Result.BindingID != "" {
		t.Errorf("bindingId = %q on a refusing outcome, want none", got.Result.BindingID)
	}
	repo.mu.Lock()
	closed := repo.closed
	repo.mu.Unlock()
	if !closed {
		t.Error("the refusing outcome's repo was not closed — it leaks")
	}
	if n := e.gitBindingCount(sid); n != 0 {
		t.Errorf("%d binding(s) registered for a refusing outcome, want 0", n)
	}
}

// TestGitOpen_NilRepoWithOKOutcomeIsInternalError is the other direction of
// the same lie: Go cannot encode "repo is non-nil iff ok" in a three-value
// return, so an ok outcome with no repository is an internal error, not a
// binding with nothing behind it.
func TestGitOpen_NilRepoWithOKOutcomeIsInternalError(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo:  func() git.Repo { return nil },
		outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)

	resp := jsonrpcCallWithID(t, e.conn, "git.open", map[string]any{
		"sessionId": sid, "cwd": "/tmp/repo",
	}, 2)
	var got struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if got.Error == nil {
		t.Fatal("git.open with a nil repo on an ok outcome succeeded, want an internal error")
	}
	if got.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", got.Error.Code)
	}
	if n := e.gitBindingCount(sid); n != 0 {
		t.Errorf("%d binding(s) registered, want 0", n)
	}
}

// TestGitOpen_RegisterFailureClosesRepoAndReturnsNoBinding drives Register's
// one reachable failure through the wire: a typed-nil repo passes the
// handler's `repo == nil` check and is refused by the registry itself
// (isNilRepo). The handler must close it, surface the register error, and
// return no binding.
func TestGitOpen_RegisterFailureClosesRepoAndReturnsNoBinding(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		// A typed nil: `repo == nil` is false, Register refuses it.
		mkRepo:  func() git.Repo { return (*stubGitRepo)(nil) },
		outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)

	resp := jsonrpcCallWithID(t, e.conn, "git.open", map[string]any{
		"sessionId": sid, "cwd": "/tmp/repo",
	}, 2)
	var got struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if got.Error == nil {
		t.Fatal("git.open with a typed-nil repo succeeded, want a register failure")
	}
	if got.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", got.Error.Code)
	}
	if !strings.Contains(got.Error.Message, "git.open:") {
		t.Errorf("error message %q does not surface the register failure", got.Error.Message)
	}
	if n := e.gitBindingCount(sid); n != 0 {
		t.Errorf("%d binding(s) registered after a register failure, want 0", n)
	}
}

// ── read half ─────────────────────────────────────────────────────────────

// TestGitStatus_UnknownAndClosedBindingAnswersUnknownBinding: a status on a
// binding that never existed, and on one that was closed, answers the
// unknownBinding error — never a panic (acceptance criterion).
func TestGitStatus_UnknownAndClosedBindingAnswersUnknownBinding(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))

	resp := jsonrpcCallWithID(t, e.conn, "git.status", map[string]any{"bindingId": "never-existed"}, 1)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.status: unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32602 {
		t.Fatalf("git.status on an unknown binding: %+v, want -32602", got.Error)
	}

	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	closeResp := jsonrpcCallWithID(t, e.conn, "git.close", map[string]any{"bindingId": bid}, 3)
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeResp, &closeEnv); err != nil {
		t.Fatalf("git.close: unmarshal: %v", err)
	}
	if closeEnv.Error != nil {
		t.Fatalf("git.close: %+v", closeEnv.Error)
	}

	resp = jsonrpcCallWithID(t, e.conn, "git.status", map[string]any{"bindingId": bid}, 4)
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.status: unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32602 {
		t.Fatalf("git.status on a closed binding: %+v, want -32602", got.Error)
	}
}

// TestGitClose_OwnershipRecheckedAndRepoReleased: a binding is closed by the
// connection that owns its session — never by whoever knows its id — and
// the close releases the repository.
func TestGitClose_OwnershipRecheckedAndRepoReleased(t *testing.T) {
	repo := newStubGitRepo()
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	// A second connection that does not own the session cannot close it.
	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	respB := jsonrpcCallWithID(t, connB, "git.close", map[string]any{"bindingId": bid}, 3)
	var gotB struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(respB, &gotB); err != nil {
		t.Fatalf("git.close: unmarshal: %v", err)
	}
	if gotB.Error == nil || gotB.Error.Code != -32602 {
		t.Fatalf("git.close from a non-owner: %+v, want -32602", gotB.Error)
	}
	repo.mu.Lock()
	closed := repo.closed
	repo.mu.Unlock()
	if closed {
		t.Fatal("the repo was closed by a non-owner")
	}

	resp := jsonrpcCallWithID(t, e.conn, "git.close", map[string]any{"bindingId": bid}, 4)
	var got struct {
		Result struct {
			Closed bool `json:"closed"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.close: unmarshal: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("git.close: %+v", got.Error)
	}
	if !got.Result.Closed {
		t.Error("closed = false on a successful close")
	}
	repo.mu.Lock()
	closed = repo.closed
	repo.mu.Unlock()
	if !closed {
		t.Error("the repo was not released by its own close")
	}
}

// ── the unborn-branch unstage state ───────────────────────────────────────

// TestGitUnstage_UnbornBranchIsARenderableState: per-file unstage on an
// unborn branch fails with git's own error (git reset with pathspecs
// resolves HEAD), and that failure must arrive as the state "unborn" with a
// fresh status — not as a transport error (brief, worker A's item 2).
func TestGitUnstage_UnbornBranchIsARenderableState(t *testing.T) {
	unborn := git.Status{
		Branch: "master", Unborn: true,
		Staged:       []git.Entry{{Path: "f.txt", X: 'A', Y: '.'}},
		Unstaged:     []git.Entry{},
		Conflicted:   []git.Entry{},
		Total:        1,
		Completeness: git.CompletenessComplete,
	}
	repo := &stubGitRepo{
		status:     unborn,
		unstageErr: errors.New("git reset: exit 128: fatal: could not resolve 'HEAD'"),
	}
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	resp := jsonrpcCallWithID(t, e.conn, "git.unstage", map[string]any{
		"bindingId": bid, "paths": []string{"f.txt"},
	}, 3)
	var got struct {
		Result struct {
			State  string `json:"state"`
			Status struct {
				Unborn bool `json:"unborn"`
			} `json:"status"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.unstage: unmarshal: %v\nraw: %s", err, resp)
	}
	if got.Error != nil {
		t.Fatalf("git.unstage on an unborn branch came back as a transport error: %+v", got.Error)
	}
	if got.Result.State != "unborn" {
		t.Errorf("state = %q, want unborn", got.Result.State)
	}
	if !got.Result.Status.Unborn {
		t.Error("the unborn state must carry the fresh status, so the panel can repaint")
	}
}

// TestGitUnstage_OrdinaryFailureIsATransportError: any other unstage
// failure stays an error — the panel repaints from its next poll.
func TestGitUnstage_OrdinaryFailureIsATransportError(t *testing.T) {
	repo := &stubGitRepo{
		status:     stubStatus(),
		unstageErr: errors.New("git reset: exit 128: fatal: index.lock exists"),
	}
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	resp := jsonrpcCallWithID(t, e.conn, "git.unstage", map[string]any{
		"bindingId": bid, "paths": []string{"f.txt"},
	}, 3)
	var got struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.unstage: unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("an ordinary unstage failure came back as a result, want a transport error")
	}
	if got.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", got.Error.Code)
	}
}

// TestGitLog_UnknownBindingAnswersUnknownBinding — the same guard every
// later git.* call re-checks (D15): a log on a binding the caller cannot
// use answers the unknownBinding error, never a panic.
func TestGitLog_UnknownBindingAnswersUnknownBinding(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))
	resp := jsonrpcCallWithID(t, e.conn, "git.log", map[string]any{"bindingId": "never-existed"}, 1)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.log: unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32602 {
		t.Fatalf("git.log on an unknown binding: %+v, want -32602", got.Error)
	}
}

// TestGitLog_BoundIsPolicy — the handler bounds the read by
// git.MaxLogEntries (D9): the panel never asks for an unbounded log, and
// the limit lives with the rest of the work ceilings, not on the wire.
func TestGitLog_BoundIsPolicy(t *testing.T) {
	repo := newStubGitRepo()
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	gitWireCall(t, e, "git.log", map[string]any{"bindingId": bid}, 3)

	repo.mu.Lock()
	got := repo.logMax
	repo.mu.Unlock()
	if got != git.MaxLogEntries {
		t.Fatalf("handler asked for max = %d, want git.MaxLogEntries (%d)", got, git.MaxLogEntries)
	}
}

// TestGitLog_RepoFailureIsATransportError — a log that cannot be made or
// completed is an error, never an empty list the panel renders as a
// commitless branch.
func TestGitLog_RepoFailureIsATransportError(t *testing.T) {
	repo := &stubGitRepo{
		status: stubStatus(),
		logErr: errors.New("git log: exit 128: fatal: bad object HEAD"),
	}
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	resp := jsonrpcCallWithID(t, e.conn, "git.log", map[string]any{"bindingId": bid}, 3)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.log: unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("a failing log came back as a result, want a transport error")
	}
	if got.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", got.Error.Code)
	}
}

// ── git.remote (brief, nocx-hc0m) ──────────────────────────────────────

// TestGitRemote_OkAnswersTheRemoteURL — a branch tracking a remote answers
// the remote's own URL, verbatim: the wire carries what git said, and the
// conversion to a web page is the renderer's.
func TestGitRemote_OkAnswersTheRemoteURL(t *testing.T) {
	repo := newStubGitRepo()
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	resp := jsonrpcCallWithID(t, e.conn, "git.remote", map[string]any{"bindingId": bid}, 3)
	var got struct {
		Result gitRemoteResult  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.remote: unmarshal: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("git.remote: %+v", got.Error)
	}
	if got.Result.State != "ok" || got.Result.URL != "git@github.com:shady2k/nocx.git" {
		t.Fatalf("result = %+v, want ok with the remote's URL", got.Result)
	}
}

// TestGitRemote_NoRemoteIsAResultState — detached HEAD, no upstream, a
// deleted remote: the none RESULT state, never a transport error — the
// panel draws no link (D14) instead of failing.
func TestGitRemote_NoRemoteIsAResultState(t *testing.T) {
	repo := &stubGitRepo{
		status:    stubStatus(),
		remoteErr: &git.ErrNoRemote{},
	}
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	resp := jsonrpcCallWithID(t, e.conn, "git.remote", map[string]any{"bindingId": bid}, 3)
	var got struct {
		Result gitRemoteResult  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.remote: unmarshal: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("no remote came back as an error: %+v", got.Error)
	}
	if got.Result.State != "none" || got.Result.URL != "" {
		t.Fatalf("result = %+v, want the none state", got.Result)
	}
}

// TestGitRemote_RepoFailureIsATransportError — an invocation that could
// not be made or completed is an error, never a silent "no link".
func TestGitRemote_RepoFailureIsATransportError(t *testing.T) {
	repo := &stubGitRepo{
		status:    stubStatus(),
		remoteErr: errors.New("git for-each-ref: exit 1: fatal: bad revision"),
	}
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo }, outcome: stubOpenOutcome(),
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	resp := jsonrpcCallWithID(t, e.conn, "git.remote", map[string]any{"bindingId": bid}, 3)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.remote: unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("a failing remote read came back as a result, want a transport error")
	}
	if got.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", got.Error.Code)
	}
}

// TestGitRemote_UnknownBindingAnswersUnknownBinding — the same guard every
// later git.* call re-checks (D15): a remote read on a binding the caller
// cannot use answers the unknownBinding error, never a panic.
func TestGitRemote_UnknownBindingAnswersUnknownBinding(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	_ = bid
	resp := jsonrpcCallWithID(t, e.conn, "git.remote", map[string]any{"bindingId": "nope"}, 3)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.remote: unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("git.remote on an unknown binding succeeded")
	}
	if got.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", got.Error.Code)
	}
}

// ── the teardown notification ─────────────────────────────────────────────

// TestGitChanged_DeliveredOnSessionClose is the addressing test the brief
// demands: close a real session and assert the client receives git.changed
// with the right bindingId and reason. The explicit-close path captured no
// subscriber before the teardown fix, so closing a terminal destroyed its
// bindings silently — the mechanism behind nocx-lzfb. This test asserts the
// delivery the fix exists to produce.
//
// The delivery and the close response are read through ONE loop (see
// closeSessionCollectNotification): the teardown write goroutine can deliver
// git.changed before the response, and the wire order between the two is
// unspecified, so a wait that discards id-less frames while hunting the
// response loses the very delivery this test exists to catch.
func TestGitChanged_DeliveredOnSessionClose(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	closeResp, raw := closeSessionCollectNotification(t, e.conn, sid, "git.changed", 3, wantWithin)
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeResp, &closeEnv); err != nil {
		t.Fatalf("close: unmarshal: %v", err)
	}
	if closeEnv.Error != nil {
		t.Fatalf("close: %+v", closeEnv.Error)
	}

	var params gitChangedParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("git.changed: decode: %v\nraw: %s", err, raw)
	}
	if params.BindingID != bid {
		t.Errorf("bindingId = %q, want %q", params.BindingID, bid)
	}
	if params.Reason != "sessionClosed" {
		t.Errorf("reason = %q, want sessionClosed", params.Reason)
	}

	// The notification's interval, both ends: the binding is removed from
	// the registry BEFORE the notification is written (spec §5.2), so by
	// the time the client has been told the binding is gone — which this
	// test has just observed — no call can acquire it: a status on it
	// answers the error, never a panic.
	resp := jsonrpcCallWithID(t, e.conn, "git.status", map[string]any{"bindingId": bid}, 4)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.status: unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32602 {
		t.Fatalf("git.status after session close: %+v, want -32602", got.Error)
	}
}

// closeSessionCollectNotification issues the close RPC and returns both the
// matching response and the params of the given notification.
//
// It used to be the ONLY reader in this package that retained a frame it
// had not asked for, with a doc comment describing why: gitSessionClosed
// writes git.changed from a detached goroutine, so the notification can
// precede the close response and a response-only wait would eat it. That
// was right, and it was right for every other wait in the package too —
// which is now where it lives (ws_inbox_test.go). What is left here is the
// close call and its two waits, in either order on the wire.
func closeSessionCollectNotification(t *testing.T, conn *websocket.Conn, sid, method string, id int, d time.Duration) (json.RawMessage, json.RawMessage) {
	t.Helper()
	deadline := time.Now().Add(d)
	resp := jsonrpcCallWithID(t, conn, "close", map[string]string{"sessionId": sid}, id)
	msg, err := awaitFrame(conn, deadline, isNotification(method))
	if err != nil {
		t.Fatalf("waiting for %s notification after close: %v", method, err)
	}
	f, _ := decodeFrame(msg)
	return resp, f.Params
}

// TestGitChanged_NotDeliveredWithoutSubscriber pins the other end: with no
// subscriber attached, teardown has nobody to tell and must not panic or
// block.
func TestGitChanged_NotDeliveredWithoutSubscriber(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	// Detach the subscriber, then close the session: the captured
	// subscriber is nil, so the teardown has nobody to notify and is a
	// no-op for the notification path.
	e.ws.getRx(session.ID(sid)).setSubscriber(nil, nil)
	closeResp := jsonrpcCallWithID(t, e.conn, "close", map[string]string{"sessionId": sid}, 3)
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeResp, &closeEnv); err != nil {
		t.Fatalf("close: unmarshal: %v", err)
	}
	if closeEnv.Error != nil {
		t.Fatalf("close: %+v", closeEnv.Error)
	}
	// The bindings are still torn down without a subscriber — only the
	// notification is skipped.
	resp := jsonrpcCallWithID(t, e.conn, "git.status", map[string]any{"bindingId": bid}, 4)
	var got struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("git.status: unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32602 {
		t.Fatalf("git.status after session close: %+v, want -32602", got.Error)
	}
	// Nothing arrives on this socket within the window.
	if raw := tryReadNotification(t, e.conn, "git.changed", 200*time.Millisecond); raw != nil {
		t.Fatalf("git.changed delivered with no subscriber: %s", raw)
	}
	_ = e.conn.SetReadDeadline(time.Time{})
}

// ── the real-git round trip ───────────────────────────────────────────────

// TestGitOpen_RealGitRoundTrip is the seam's smoke test: a real repository,
// the real local factory, and the whole wire — open returns the first
// status inline, status reads it back, close releases it.
func TestGitOpen_RealGitRoundTrip(t *testing.T) {
	// EvalSymlinks, because this test compares a path it chose against the
	// path git reports. On macOS /var is a symlink to /private/var and
	// t.TempDir() returns the unresolved form while `rev-parse
	// --show-toplevel` resolves it — green on Linux, red on the platform the
	// app ships on, which is exactly the disagreement CI exists to catch.
	// In a closure so the error does not shadow the `err` this test uses
	// throughout — govet's shadow check is on, and it is right that a second
	// `err` in a long test is worth a second look.
	dir := func() string {
		d, evalErr := filepath.EvalSymlinks(t.TempDir())
		if evalErr != nil {
			t.Fatalf("resolving the temp dir: %v", evalErr)
		}
		return d
	}()
	initRealGitRepo(t, dir)
	// The factory is constructed exactly as the composition root constructs
	// it — no test-only option. That is deliberate: pinning the environment
	// here would exercise a seam the product does not use, and the resolver
	// needs no help, because its failure path falls back to os.Environ(),
	// which is where this process found git in the first place. So git is
	// resolvable whichever branch the resolver takes.
	e := newGitTestEnv(t, WithGitRepoFactory(local.NewFactory()))
	sid := e.openSession(t, 1)

	resp := jsonrpcCallWithID(t, e.conn, "git.open", map[string]any{
		"sessionId": sid, "cwd": dir,
	}, 2)
	var open struct {
		Result struct {
			State     string `json:"state"`
			BindingID string `json:"bindingId"`
			Toplevel  string `json:"toplevel"`
			EnvState  string `json:"envState"`
			Status    *struct {
				Branch string `json:"branch"`
			} `json:"status"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &open); err != nil {
		t.Fatalf("git.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if open.Error != nil {
		t.Fatalf("git.open: %+v", open.Error)
	}
	if open.Result.State != "ok" || open.Result.BindingID == "" {
		t.Fatalf("git.open: state=%q bindingId=%q", open.Result.State, open.Result.BindingID)
	}
	if open.Result.Toplevel != dir {
		t.Errorf("toplevel = %q, want %q", open.Result.Toplevel, dir)
	}
	if open.Result.EnvState == "" {
		t.Error("envState is absent — the panel needs it before the first commit (D6)")
	}
	if open.Result.Status == nil {
		t.Error("the first status did not ride the open result")
	} else if open.Result.Status.Branch == "" {
		t.Error("status.branch is empty on a real repository")
	}
	statusResp := jsonrpcCallWithID(t, e.conn, "git.status", map[string]any{"bindingId": open.Result.BindingID}, 3)
	var st struct {
		Result struct {
			Status struct {
				Branch string `json:"branch"`
				Head   string `json:"head"`
			} `json:"status"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(statusResp, &st); err != nil {
		t.Fatalf("git.status: unmarshal: %v", err)
	}
	if st.Error != nil {
		t.Fatalf("git.status: %+v", st.Error)
	}
	if st.Result.Status.Head == "" {
		t.Error("status.head is empty after a commit")
	}

	// The counts are real git's answer, off the real socket: modify f.txt
	// outside the app, poll status, and read +3 −1 back on the wire
	// (brief nocx-i4ki). This is the assertion the DTO tests cannot make —
	// they prove the struct is well-formed, not that the server sends it.
	// #nosec G304 — f.txt is the fixture the test itself wrote above
	// (initRealGitRepo); the join is a fixed test literal, and the whole
	// call is the point of this assertion.
	orig, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.ReplaceAll(string(orig), "v1\n", "v1\nA\nB\nC\n")
	if modified == string(orig) {
		modified = string(orig) + "A\nB\nC\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	countsResp := jsonrpcCallWithID(t, e.conn, "git.status", map[string]any{"bindingId": open.Result.BindingID}, 4)
	var cs struct {
		Result struct {
			Status struct {
				Branch   string `json:"branch"`
				Unstaged []struct {
					Path    string `json:"path"`
					Added   *int   `json:"added"`
					Deleted *int   `json:"deleted"`
				} `json:"unstaged"`
			} `json:"status"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(countsResp, &cs); err != nil {
		t.Fatalf("git.status (counts): unmarshal: %v", err)
	}
	if cs.Error != nil {
		t.Fatalf("git.status (counts): %+v", cs.Error)
	}
	if len(cs.Result.Status.Unstaged) != 1 || cs.Result.Status.Unstaged[0].Path != "f.txt" {
		t.Fatalf("unstaged = %+v, want f.txt", cs.Result.Status.Unstaged)
	}
	entry := cs.Result.Status.Unstaged[0]
	if entry.Added == nil || entry.Deleted == nil {
		t.Fatalf("f.txt carries no counts off the real socket: %+v", entry)
	}
	if *entry.Added != 3 || *entry.Deleted != 0 {
		t.Fatalf("counts = +%d −%d, want +3 −0", *entry.Added, *entry.Deleted)
	}
	// The counts ride the same status result as the branch — one answer,
	// scoped by one epoch (D17): the counts were never fetched separately.
	if cs.Result.Status.Branch != "master" {
		t.Fatalf("branch = %q, want master", cs.Result.Status.Branch)
	}

	closeResp := jsonrpcCallWithID(t, e.conn, "git.close", map[string]any{"bindingId": open.Result.BindingID}, 5)
	var cl struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeResp, &cl); err != nil {
		t.Fatalf("git.close: unmarshal: %v", err)
	}
	if cl.Error != nil {
		t.Fatalf("git.close: %+v", cl.Error)
	}
}

// initRealGitRepo initialises a repository with one committed file.
func initRealGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "master")
	run("config", "user.email", "git-wire-test@example.com")
	run("config", "user.name", "Git Wire Test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o600); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-m", "one")
}

// tryReadNotification is the non-fatal form of readNotification: it returns
// nil when no matching notification arrives within the window.
func tryReadNotification(t *testing.T, conn *websocket.Conn, method string, d time.Duration) json.RawMessage {
	t.Helper()
	params, err := awaitNotification(conn, method, d)
	if err != nil {
		return nil
	}
	return params
}

// ── the error discriminator (nocx-bpqil) ─────────────────────────────────
//
// isUnknownBinding in the renderer treated EVERY -32602 as an unknown
// binding, so a conflicted stage-all (which also rides -32602) was silently
// re-resolved through git.open instead of surfacing as the refusal it is.
// The fix is a machine-readable reason on the wire: every git domain error
// that maps to -32602 carries data.reason so the renderer can distinguish
// them. These tests prove the reason arrives over the real socket — not
// only that the DTO could carry it, but that the handler actually sends it.

// gitErrorDataWire is the on-wire shape of a git error's data payload.
// Mirrors the Go gitErrorData struct (ws_git.go) for test-side decoding.
type gitErrorDataWire struct {
	Reason string `json:"reason"`
}

// gitErrorEnvelope decodes a JSON-RPC error response's data field.
type gitErrorEnvelope struct {
	Error *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func decodeGitErrorData(t *testing.T, raw json.RawMessage) gitErrorDataWire {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		t.Fatalf("error data is absent; the handler must send data.reason")
	}
	// The payload must satisfy the declared contract — the closed enum and
	// additionalProperties:false make the discriminator exact in both
	// directions (contracts/git.error.schema.json, AGENTS.md rule 5).
	validateJSON(t, loadSchema(t, "git.error.schema.json"), raw, "git error data (real socket)")
	var d gitErrorDataWire
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal error data: %v (raw: %s)", err, raw)
	}
	return d
}

// TestGitError_UnknownBindingCarriesReasonOverTheWire: a git.status on a
// binding that never existed answers -32602 with data.reason =
// "unknown-binding" — the discriminator the renderer's isUnknownBinding
// reads to decide whether to re-resolve through git.open.
func TestGitError_UnknownBindingCarriesReasonOverTheWire(t *testing.T) {
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))

	// A well-formed 32-hex binding id the registry never minted: it passes
	// the shape validator and Acquire answers ErrUnknownBinding — the path
	// the renderer's re-resolve listens for. (A malformed id is refused by
	// validation with a bare -32602 that carries no reason.)
	resp := jsonrpcCallWithID(t, e.conn, "git.status",
		map[string]any{"bindingId": "00000000000000000000000000000000"}, 1)
	var env gitErrorEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", env.Error)
	}
	data := decodeGitErrorData(t, env.Error.Data)
	if data.Reason != "unknown-binding" {
		t.Errorf("data.reason = %q, want %q", data.Reason, "unknown-binding")
	}
}

// TestGitError_ConflictedCarriesReasonOverTheWire: a git.stageAll that is
// refused because a merge conflict is unresolved answers -32602 with
// data.reason = "conflicted" — NOT "unknown-binding", so the renderer
// does not re-resolve and the refusal reaches the user.
func TestGitError_ConflictedCarriesReasonOverTheWire(t *testing.T) {
	repo := newStubGitRepo()
	repo.mutateErr = &git.ErrConflicted{Path: "conf.txt"}
	e := gitContractEnv(t, repo)
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	resp := jsonrpcCallWithID(t, e.conn, "git.stageAll", map[string]any{"bindingId": bid}, 3)
	var env gitErrorEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", env.Error)
	}
	data := decodeGitErrorData(t, env.Error.Data)
	if data.Reason != "conflicted" {
		t.Errorf("data.reason = %q, want %q", data.Reason, "conflicted")
	}
}

// TestGitError_NothingToCommitCarriesReasonOverTheWire: a git.commit
// refused because nothing is staged answers -32602 with data.reason =
// "nothing-to-commit" — NOT "unknown-binding".
func TestGitError_NothingToCommitCarriesReasonOverTheWire(t *testing.T) {
	repo := newStubGitRepo()
	repo.commitErr = &git.ErrNothingToCommit{}
	e := gitContractEnv(t, repo)
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	resp := jsonrpcCallWithID(t, e.conn, "git.commit", map[string]any{
		"bindingId": bid, "message": "subject", "amend": false,
	}, 3)
	var env gitErrorEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", env.Error)
	}
	data := decodeGitErrorData(t, env.Error.Data)
	if data.Reason != "nothing-to-commit" {
		t.Errorf("data.reason = %q, want %q", data.Reason, "nothing-to-commit")
	}
}

// TestGitError_AmendUnbornCarriesReasonOverTheWire: a git.commit with
// amend=true on an unborn branch answers -32602 with data.reason =
// "amend-unborn" — NOT "unknown-binding".
func TestGitError_AmendUnbornCarriesReasonOverTheWire(t *testing.T) {
	repo := newStubGitRepo()
	repo.commitErr = &git.ErrAmendUnborn{}
	e := gitContractEnv(t, repo)
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	resp := jsonrpcCallWithID(t, e.conn, "git.commit", map[string]any{
		"bindingId": bid, "message": "subject", "amend": true,
	}, 3)
	var env gitErrorEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", env.Error)
	}
	data := decodeGitErrorData(t, env.Error.Data)
	if data.Reason != "amend-unborn" {
		t.Errorf("data.reason = %q, want %q", data.Reason, "amend-unborn")
	}
}

// TestGitErrorReasonMapping verifies the reason mapping for every git
// domain error that shares -32602, so the renderer can distinguish them.
// This is the function-level check; the over-the-wire tests above prove
// the handler actually sends the reason for the two shapes the acceptance
// criteria name.
func TestGitErrorReasonMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"unknown-binding":   {&git.ErrUnknownBinding{ID: "x"}, "unknown-binding"},
		"not-owned":         {&git.ErrNotOwned{ID: "x", SessionID: session.ID("")}, "not-owned"},
		"handle-released":   {&git.ErrHandleReleased{}, "handle-released"},
		"nothing-to-commit": {&git.ErrNothingToCommit{}, "nothing-to-commit"},
		"amend-unborn":      {&git.ErrAmendUnborn{}, "amend-unborn"},
		"conflicted":        {&git.ErrConflicted{Path: "p"}, "conflicted"},
	} {
		t.Run(name, func(t *testing.T) {
			got := gitErrorReason(tc.err)
			if got == nil || got.Reason != tc.want {
				t.Fatalf("gitErrorReason(%v) = %+v, want reason %q", tc.err, got, tc.want)
			}
		})
	}

	// A non-git error carries no reason — it is an invocation failure,
	// not a domain refusal, and the renderer must not treat it as one.
	if got := gitErrorReason(errors.New("some io error")); got != nil {
		t.Fatalf("gitErrorReason(arbitrary error) = %+v, want nil", got)
	}
}
