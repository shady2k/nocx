package transport

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

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
