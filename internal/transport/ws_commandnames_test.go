package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/log"
)

// stubCommandNames answers a scripted result and records what target it was
// asked about — the routing facts must reach the resolver, or the cache key
// is built from nothing.
type stubCommandNames struct {
	res    commandnames.Result
	target capability.SessionTarget
	calls  int
}

func (s *stubCommandNames) CommandNames(_ context.Context, target capability.SessionTarget) commandnames.Result {
	s.calls++
	s.target = target
	return s.res
}

// Every state the schema names marshals into something the schema accepts —
// including the two that carry no names, where `[]` and not `null` is what
// the contract requires (the defect vault.status's `providers` field had).
func TestShellCommandNames_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.commandNames.schema.json")

	cases := map[string]commandnames.Result{
		"ready":     {State: commandnames.StateReady, Names: []string{"git", "ls"}},
		"stale":     {State: commandnames.StateStale, Names: []string{"git"}, Age: 90 * time.Second},
		"timed-out": {State: commandnames.StateTimedOut, Reason: "the command-name scan did not finish inside its deadline"},
		"failed":    {State: commandnames.StateFailed, Reason: "remote host refused the exec"},
		"truncated": {State: commandnames.StateReady, Names: []string{"git"}, Truncated: true},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(toWireCommandNames(res))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "shell.commandNames DTO")

			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded["names"] == nil {
				t.Fatalf("names marshalled as null; the contract wants an array and the renderer iterates it")
			}
		})
	}
}

// The real result off the real socket. A test that validates a payload the
// test itself built proves the struct is well-formed, not that the server
// sends it.
func TestShellCommandNames_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.commandNames.schema.json")
	ctx := context.Background()

	stub := &stubCommandNames{res: commandnames.Result{
		State: commandnames.StateReady,
		Names: []string{"git", "ls"},
	}}
	ws := NewWSServer(
		log.NewSlogAdapter(nil),
		newRegWithStub(log.NewSlogAdapter(nil)),
		WithCommandNames(stub),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 800, "ypixel": 600,
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("open error: %+v", openResp.Error)
	}
	var openResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &openResult); err != nil {
		t.Fatalf("unmarshal open result: %v", err)
	}

	resp := vaultCall(t, conn, "shell.commandNames", map[string]any{"sessionId": openResult.SessionID}, 2)
	if resp.Error != nil {
		t.Fatalf("shell.commandNames error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "shell.commandNames over the wire")

	if stub.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", stub.calls)
	}

	var got shellCommandNamesResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != "ready" || len(got.Names) != 2 {
		t.Fatalf("result = %+v", got)
	}
}

// An unwired service is a stated degrade, not a JSON-RPC error: the dropdown
// must never show a spinner that never resolves, and the renderer already
// has a sentence for `failed`.
func TestShellCommandNames_UnwiredAnswersAStatedFailure(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 800, "ypixel": 600,
	}, 1)
	var openResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &openResult); err != nil {
		t.Fatalf("unmarshal open result: %v", err)
	}

	resp := vaultCall(t, conn, "shell.commandNames", map[string]any{"sessionId": openResult.SessionID}, 2)
	if resp.Error != nil {
		t.Fatalf("an unwired service answered a JSON-RPC error: %+v", resp.Error)
	}
	var got shellCommandNamesResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != "failed" || got.Reason == "" {
		t.Fatalf("result = %+v, want a named failure", got)
	}
}

// A forged or stale session id reaches no scan at all.
func TestShellCommandNames_RefusesAnUnknownSession(t *testing.T) {
	ctx := context.Background()
	stub := &stubCommandNames{res: commandnames.Result{State: commandnames.StateReady}}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithCommandNames(stub))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "shell.commandNames", map[string]any{
		"sessionId": "0123456789abcdef0123456789abcdef",
	}, 1)
	if resp.Error == nil {
		t.Fatalf("an unknown session was answered: %s", resp.Result)
	}
	if stub.calls != 0 {
		t.Fatalf("the resolver ran for an unknown session")
	}
}

// The ingress validator refuses a params shape that could never name a live
// session, before anything is looked up.
func TestValidateShellCommandNamesRaw(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want bool // want a refusal
	}{
		"good":       {`{"sessionId":"0123456789abcdef0123456789abcdef"}`, false},
		"no params":  {``, true},
		"not object": {`[]`, true},
		"empty id":   {`{"sessionId":""}`, true},
		"bad shape":  {`{"sessionId":"not-a-session"}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := validateShellCommandNamesRaw(json.RawMessage(tc.raw))
			if (msg != "") != tc.want {
				t.Fatalf("validate(%s) = %q", tc.raw, msg)
			}
		})
	}
}
