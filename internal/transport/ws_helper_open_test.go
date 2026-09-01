package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

type helperOpenTestChannel struct {
	done chan struct{}
	once sync.Once
}

func (c *helperOpenTestChannel) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *helperOpenTestChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *helperOpenTestChannel) Close() error                { c.once.Do(func() { close(c.done) }); return nil }

func (c *helperOpenTestChannel) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (c *helperOpenTestChannel) Done() <-chan struct{} { return c.done }

type helperOpenTestOpener struct {
	reg    *session.Reg
	id     session.ID
	called bool
}

func (o *helperOpenTestOpener) OpenHosted(_ context.Context, cfg session.Config) (HostedSessionOpen, bool, error) {
	o.called = true
	sess, err := o.reg.Adopt(cfg, o.id, &helperOpenTestChannel{done: make(chan struct{})})
	if err != nil {
		return HostedSessionOpen{}, false, err
	}

	return HostedSessionOpen{Session: sess, Host: cfg.Host, Account: "alice", Generation: "gen-test"}, true, nil
}

type helperOpenTestInventory struct {
	sessionID  string
	host       string
	account    string
	generation string
}

func (i helperOpenTestInventory) Owns(id string) bool { return id == i.sessionID }
func (i helperOpenTestInventory) Generation() string  { return i.generation }
func (i helperOpenTestInventory) Host() string        { return i.host }
func (i helperOpenTestInventory) Account() string     { return i.account }
func (i helperOpenTestInventory) LiveSessions(context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{i.sessionID: {}}, nil
}

func TestShippedOpenUsesHelperMintedSessionID(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	helperID := session.ID("0123456789abcdef0123456789abcdef")
	opener := &helperOpenTestOpener{reg: reg, id: helperID}
	ws := NewWSServer(log.NewSlogAdapter(nil), reg,
		WithProfileResolver(&fakeResolver{
			resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
				return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
			},
		}),
		WithHelperSessionOpener(opener),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", map[string]any{"kind": "ssh", "profileId": "p", "cols": 80, "rows": 24})
	var result struct {
		SessionID string `json:"sessionId"`
	}
	decodeJSONRPCResult(t, resp, &result)
	if !opener.called {
		t.Fatal("shipped coordinator did not ask the helper opener")
	}
	if result.SessionID != string(helperID) {
		t.Fatalf("open sessionId = %q, want helper-minted %q", result.SessionID, helperID)
	}
}

func TestShippedHelperOpenPersistsBindingForReconciliation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	if _, err = db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "workspace:default", Name: "default"},
		content.Tab{ID: "tab-default", WorkspaceID: "workspace:default", Layout: content.LayoutRow},
		content.Pane{ID: "pane-default", TabID: "tab-default", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		_ = db.Close()
		t.Fatalf("CreateWorkspace: %v", err)
	}

	reg := newRegWithStub(log.NewSlogAdapter(nil))
	helperID := session.ID("fedcba9876543210fedcba9876543210")
	opener := &helperOpenTestOpener{reg: reg, id: helperID}
	ws := NewWSServer(log.NewSlogAdapter(nil), reg,
		WithContentDB(db),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
				return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
			},
		}),
		WithHelperSessionOpener(opener),
	)
	if err = ws.Start(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, ws)
	resp := jsonrpcCall(t, conn, "open", map[string]any{"kind": "ssh", "profileId": "p", "cols": 80, "rows": 24})
	var result struct {
		SessionID string `json:"sessionId"`
	}
	decodeJSONRPCResult(t, resp, &result)
	if result.SessionID != string(helperID) {
		t.Fatalf("open sessionId = %q, want helper-minted %q", result.SessionID, helperID)
	}
	_ = conn.Close()
	if err = ws.Stop(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("Stop: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatalf("close content db: %v", err)
	}

	reopened, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("reopen content db: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	pending, err := reopened.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the session opened through the shipped path carries no generation, so no inventory may judge it: pending=%+v", pending)
	}
	got := pending[0]
	if got.SessionID != string(helperID) || got.Host != "remote.example" ||
		got.Account != "alice" || got.Generation != "gen-test" {
		t.Fatalf("pending binding = %+v, want %s at alice@remote.example generation gen-test", got, helperID)
	}
	inventory := helperOpenTestInventory{
		sessionID: string(helperID), host: got.Host, account: got.Account, generation: got.Generation,
	}
	if inventory.Host() != got.Host || inventory.Account() != got.Account ||
		inventory.Generation() != got.Generation || !inventory.Owns(got.SessionID) {
		t.Fatalf("matching inventory does not own %q with the persisted binding", got.SessionID)
	}
	live, err := inventory.LiveSessions(ctx)
	if err != nil {
		t.Fatalf("matching inventory: %v", err)
	}
	verdict := content.VerdictAbsent
	if _, ok := live[got.SessionID]; ok {
		verdict = content.VerdictLive
	}
	if verdict == content.VerdictUnknown {
		t.Fatalf("matching inventory produced unknown for %q", got.SessionID)
	}
	if err = reopened.Reconcile().Apply(ctx, content.SessionJudgement{
		SessionID: got.SessionID, Verdict: verdict,
	}); err != nil {
		t.Fatalf("Apply(%s): %v", verdict, err)
	}
	after, err := reopened.Reconcile().Pending(ctx)
	if err != nil {
		t.Fatalf("Pending after live inventory answer: %v", err)
	}
	if len(after) != 1 || !after[0].SessionExists {
		t.Fatalf("reconciliation left the real session unknown after matching inventory: %+v", after)
	}
}

func TestShippedHelperOpenRefusesUnpersistedBinding(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	real, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	if _, err = real.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "workspace:default", Name: "default"},
		content.Tab{ID: "tab-default", WorkspaceID: "workspace:default", Layout: content.LayoutRow},
		content.Pane{ID: "pane-default", TabID: "tab-default", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		_ = real.Close()
		t.Fatalf("CreateWorkspace: %v", err)
	}

	reg := newRegWithStub(log.NewSlogAdapter(nil))
	helperID := session.ID("abcdef0123456789abcdef0123456789")
	opener := &helperOpenTestOpener{reg: reg, id: helperID}
	db := &failingLedgerDB{
		ContentDB: real, failOn: "CreateSession", err: errors.New("binding store unavailable"),
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), reg,
		WithContentDB(db),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
				return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
			},
		}),
		WithHelperSessionOpener(opener),
	)
	if err := ws.Start(ctx); err != nil {
		_ = real.Close()
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, ws)
	resp := jsonrpcCall(t, conn, "open", map[string]any{"kind": "ssh", "profileId": "p", "cols": 80, "rows": 24})
	_ = conn.Close()
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode open response: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("helper open succeeded after its durable binding write failed")
	}
	if _, err := reg.Get(helperID); err == nil {
		t.Fatal("helper session remained registered after its binding write failed")
	}
	if err := ws.Stop(ctx); err != nil {
		_ = real.Close()
		t.Fatalf("Stop: %v", err)
	}
	if err := real.Close(); err != nil {
		t.Fatalf("close content db: %v", err)
	}
}

func decodeJSONRPCResult(t *testing.T, raw []byte, out any) {
	t.Helper()
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("open returned error: %+v", envelope.Error)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}
