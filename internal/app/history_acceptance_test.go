package app

// The composition-root acceptance for the ContentDB key (nocx-rtg0.14) and
// the write path (nocx-rtg0.13), in the owner's words:
//
//	Run a command. Restart. Press Up. The command is there, and the panel
//	says source: store.
//
// On a host with NO OS keystore and a SEALED vault — the vault is never
// unsealed anywhere in this test, so it cannot be anything but sealed — the
// app must come up with the REAL store, never the stub, and a command
// recorded over the real socket must be readable after a full restart of
// the composition root. The seal is irrelevant: neither branch of the key
// lifecycle touches it, which is exactly what used to fail here ("content
// key: probe \"file\": vault is sealed").

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"

	"github.com/gorilla/websocket"
)

func TestHistory_NoKeystoreSealedVault_RecordSurvivesRestart(t *testing.T) {
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	binary := filepath.Join(helperRoot(home, src.hash()), "nocx-helper")
	t.Cleanup(func() { endTheDaemon(t, binary) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	// The derived-key artifacts landed where the design says: the salt in
	// the CONFIG directory — a copy of the data directory carries nothing
	// that opens it — and the database in the DATA directory.
	//
	// Asked of storage rather than rebuilt from the environment. Setting
	// XDG_CONFIG_HOME and then joining it by hand was two derivations of one
	// answer, and on darwin they disagreed: paths.go resolves that platform
	// from os.UserHomeDir(), so the app wrote the runner's real profile while
	// the test looked in a temp directory nothing had touched (nocx-8ax9).
	paths, err := storage.NewAppPaths()
	if err != nil {
		t.Fatalf("NewAppPaths: %v", err)
	}
	saltPath := filepath.Join(paths.ConfigDir(), "contentkey.salt")
	if _, statErr := os.Stat(saltPath); statErr != nil {
		t.Fatalf("salt not minted in config dir: %v", statErr)
	}
	dbPath := filepath.Join(paths.DataDir(), "content.db")
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Fatalf("content.db not created in data dir: %v", statErr)
	}

	// Run a command through the authenticated lifecycle path. The receipt is
	// server-owned and arrives only after the shell completion closes the row.
	lifecycle := openLifecycleAppSession(t, a)
	_ = lifecycle.record(t, "echo survived", "/srv", 2)
	_ = lifecycle.conn.Close()

	// Restart: shut the first composition root down and build a second one
	// over the same directories — the process equivalent of quitting and
	// relaunching the app.
	a.Shutdown(ctx)
	a2, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after restart: %v", startErr)
	}
	defer a2.Shutdown(ctx)

	// Press Up: the recall overlay's exact call. The command is there, and
	// the panel says source: store — the row came from the database, not
	// from this session.
	conn2 := dialAppWS(t, a2)
	defer func() { _ = conn2.Close() }()
	resp := callAppWS(t, conn2, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 50,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("history.query after restart: %+v", resp.Error)
	}
	var q struct {
		Entries []struct {
			Command string `json:"command"`
			Status  string `json:"status"`
		} `json:"entries"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(resp.Result, &q); err != nil {
		t.Fatalf("decode query result: %v (raw %s)", err, resp.Result)
	}
	if q.Source != "store" {
		t.Fatalf("source = %q, want store (the panel must say the row came from the store)", q.Source)
	}
	if len(q.Entries) != 1 || q.Entries[0].Command != "echo survived" {
		t.Fatalf("entries = %+v, want the recorded command after restart", q.Entries)
	}
	if q.Entries[0].Status != "success" {
		t.Fatalf("status = %q, want success", q.Entries[0].Status)
	}
}

// ── helpers: the real socket, the real token, the real JSON-RPC ───────────

type wsRPCResult struct {
	ID     int             `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func dialAppWS(t *testing.T, a *App) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", a.Transport.Port()), Path: "/session"}
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + a.Transport.Token()}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func callAppWS(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) *wsRPCResult {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if writeErr := conn.WriteMessage(websocket.TextMessage, req); writeErr != nil {
		t.Fatalf("write %s: %v", method, writeErr)
	}
	// Responses may arrive out of order (a slow handler answering an
	// earlier id after a later request was sent): read until the response
	// for THIS id shows up.
	for {
		messageType, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s response: %v", method, err)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var resp wsRPCResult
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode %s response: %v (raw %s)", method, err, raw)
		}
		if resp.ID == id {
			return &resp
		}
	}
}

type lifecycleAppSession struct {
	conn   *websocket.Conn
	id     string
	domain string
}

func openLifecycleAppSession(t *testing.T, a *App) lifecycleAppSession {
	t.Helper()
	conn := dialAppWS(t, a)
	open := callAppWS(t, conn, "open", map[string]any{
		"cols": 80,
		"rows": 24,
	}, 1)
	if open.Error != nil {
		t.Fatalf("open: %+v", open.Error)
	}
	var opened struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(open.Result, &opened); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	if opened.SessionID == "" {
		t.Fatal("open returned no session id")
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("wait for lifecycle prompt: %v", err)
		}
		var notification struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &notification) != nil || notification.Method != "lifecycle.changed" {
			continue
		}
		var fact struct {
			Lifecycle string `json:"lifecycle"`
			Domain    string `json:"domain"`
		}
		if json.Unmarshal(notification.Params, &fact) == nil &&
			fact.Lifecycle == "prompt_ready" && fact.Domain != "" {
			return lifecycleAppSession{conn: conn, id: opened.SessionID, domain: fact.Domain}
		}
	}
}

type appHistoryCapture struct {
	ID            string `json:"id"`
	SuggestedName string `json:"suggestedName"`
}

type appHistoryReceipt struct {
	AttemptID   string `json:"attemptId"`
	EntryID     string `json:"entryId"`
	MaskedCount int    `json:"maskedCount"`
	Redactions  []struct {
		Kind   string `json:"kind"`
		Prefix string `json:"prefix"`
		Suffix string `json:"suffix"`
	} `json:"redactions"`
	Captures []appHistoryCapture `json:"captures"`
}

func (s lifecycleAppSession) record(t *testing.T, command, cwd string, id int) appHistoryReceipt {
	t.Helper()
	submit := callAppWS(t, s.conn, "lifecycle.submitAttempt", map[string]any{
		"domain":  s.domain,
		"command": command,
		"cwd":     cwd,
		"source":  "user",
	}, id)
	if submit.Error != nil {
		t.Fatalf("lifecycle.submitAttempt: %+v", submit.Error)
	}
	var attempt struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(submit.Result, &attempt); err != nil {
		t.Fatalf("decode lifecycle.submitAttempt: %v", err)
	}
	if attempt.ID == "" {
		t.Fatal("lifecycle.submitAttempt returned no attempt id")
	}
	sidBytes, err := session.IDToBytes(session.ID(s.id))
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	frame := transport.Frame{
		Version:   transport.FrameVersion,
		MsgType:   transport.MsgTypeData,
		SessionID: sidBytes,
		Payload:   []byte(command + "\r"),
	}
	if err := s.conn.WriteMessage(websocket.BinaryMessage, frame.Encode()); err != nil {
		t.Fatalf("write command: %v", err)
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var receipt *appHistoryReceipt
	promptReady := false
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			t.Fatalf("wait for history.recorded: %v", err)
		}
		var notification struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &notification) != nil {
			continue
		}
		switch notification.Method {
		case "history.recorded":
			var got appHistoryReceipt
			if err := json.Unmarshal(notification.Params, &got); err != nil {
				t.Fatalf("decode history.recorded: %v", err)
			}
			if got.AttemptID == attempt.ID {
				receipt = &got
			}
		case "lifecycle.changed":
			var fact struct {
				Lifecycle string `json:"lifecycle"`
				Domain    string `json:"domain"`
			}
			if json.Unmarshal(notification.Params, &fact) == nil &&
				fact.Lifecycle == "prompt_ready" && fact.Domain == s.domain {
				promptReady = true
			}
		}
		if receipt != nil && promptReady {
			return *receipt
		}
	}
}

// The guard, end to end, in the owner's words: a command carrying a real key
// shape is recorded masked, and the fact of the masking survives a restart
// with the row. Record a curl with a Bearer key over the real socket,
// restart the composition root over the same directories, query it back —
// the row reads sk-p...7890, the entry says one secret was masked and of
// what kind, and the raw key appears nowhere in the marshalled result.
func TestHistory_KeyMaskedOnTheWireAndAcrossRestart(t *testing.T) {
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	binary := filepath.Join(helperRoot(home, src.hash()), "nocx-helper")
	t.Cleanup(func() { endTheDaemon(t, binary) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rawKey := "sk-proj-abcdef1234567890"
	command := `curl -H "Authorization: Bearer ` + rawKey + `" https://api.example.com`

	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	lifecycle := openLifecycleAppSession(t, a)
	receipt := lifecycle.record(t, command, "/srv", 1)
	if receipt.MaskedCount != 1 || len(receipt.Redactions) != 1 {
		t.Fatalf("receipt facts = %+v, want one mask and redaction", receipt)
	}
	_ = lifecycle.conn.Close()

	// Restart: the row must read masked from the encrypted store, with the
	// facts intact — the durable text is the masked one, by construction.
	a.Shutdown(ctx)
	a2, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after restart: %v", startErr)
	}
	defer a2.Shutdown(ctx)

	conn2 := dialAppWS(t, a2)
	defer func() { _ = conn2.Close() }()
	resp := callAppWS(t, conn2, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 50,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("history.query after restart: %+v", resp.Error)
	}

	// Grep the WHOLE marshalled result for the raw key — a field we did not
	// think of is exactly what that catches. Then read the entry the way
	// the recall panel will: the masked command, and the facts.
	raw := string(resp.Result)
	if strings.Contains(raw, rawKey) {
		t.Fatalf("the raw key appears in the query result: %s", raw)
	}

	var q struct {
		Entries []struct {
			Command     string   `json:"command"`
			MaskedCount int      `json:"maskedCount"`
			MaskedKinds []string `json:"maskedKinds"`
		} `json:"entries"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(resp.Result, &q); err != nil {
		t.Fatalf("decode query result: %v (raw %s)", err, resp.Result)
	}
	if q.Source != "store" {
		t.Fatalf("source = %q, want store", q.Source)
	}
	if len(q.Entries) != 1 {
		t.Fatalf("entries = %+v, want one row after restart", q.Entries)
	}
	e := q.Entries[0]
	if e.Command != `curl -H "Authorization: Bearer sk-p...7890" https://api.example.com` {
		t.Errorf("command = %q, want the masked row", e.Command)
	}
	if e.MaskedCount != 1 || len(e.MaskedKinds) != 1 || e.MaskedKinds[0] != "openai" {
		t.Errorf("entry facts = %d %v, want 1 [openai]", e.MaskedCount, e.MaskedKinds)
	}
}
