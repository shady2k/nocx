package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/coordinator"
	agentharness "github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/harness/pi"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/turnbearer"
)

// prefixConversation holds the harness conversation constant so the test can
// change the asking pane. The shipped transport currently keys conversations by
// pane; this adapter changes only that identity, not facts or authority.
type prefixConversation struct {
	agentharness.Harness
	observations *prefixAuthorizer
}

func (h prefixConversation) Run(ctx context.Context, spec agentharness.TurnSpec) (agentharness.Turn, error) {
	spec.ConversationID = "0198f2b0-0000-7000-8000-00000000ff01"
	return h.Harness.Run(ctx, spec)
}

// Discovery is observed without changing the admitted run's catalogue or text.
type prefixAuthorizer struct {
	askTurnAuthorizer
	mu           sync.Mutex
	instructions []string
}

func (a *prefixAuthorizer) Admit(peer toolendpoint.Peer, publish func(string, toolendpoint.AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	inv, done, err := a.askTurnAuthorizer.Admit(peer, publish)
	if err == nil {
		tools, ok := inv.Dispatcher.(*assistant.RunTools)
		if !ok {
			done()
			return assistant.ToolInvocation{}, nil, fmt.Errorf("admitted dispatcher %T is not assistant RunTools", inv.Dispatcher)
		}
		inv.Dispatcher = &prefixDispatcher{RunTools: tools, observer: a}
	}
	return inv, done, err
}

type prefixDispatcher struct {
	*assistant.RunTools
	observer *prefixAuthorizer
}

func (d *prefixDispatcher) Catalogue(grant content.Grant) []agenttools.Tool {
	return d.RunTools.Catalogue(grant)
}

func (d *prefixDispatcher) Instructions() string {
	text := d.RunTools.Instructions()
	d.observer.mu.Lock()
	defer d.observer.mu.Unlock()
	d.observer.instructions = append(d.observer.instructions, text)
	return text
}

func realPrefixPi(t *testing.T, responses ...fakemodel.Response) (prefixConversation, *fakemodel.Server) {
	t.Helper()
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("real pi is not installed on PATH")
	}
	fake := fakemodel.New(responses...)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"nocx-test": map[string]any{
		"baseUrl": server.URL + "/v1", "api": "openai-completions", "apiKey": "disposable-test-key",
		"models": []map[string]any{{
			"id": "test-model", "name": "Disposable model", "reasoning": true,
			"contextWindow": 128000, "maxTokens": 4096, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		}},
	}}}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(agentDir, "models.json"), raw, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PI_CODING_AGENT_DIR=" + agentDir, "PI_OFFLINE=1",
		"ANTHROPIC_API_KEY=", "OPENAI_API_KEY=", "GEMINI_API_KEY=", "GOOGLE_API_KEY=", "XAI_API_KEY=", "GROQ_API_KEY=", "OPENROUTER_API_KEY=", "MISTRAL_API_KEY=",
	}
	_, helper := buildAskPrograms(t)
	auth := &prefixAuthorizer{}
	logger := logtest.Slog(t)
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > uint64(^uint32(0)) {
		t.Fatalf("uid outside uint32 range: %d", uid)
	}
	ep, err := toolendpoint.New(toolendpoint.Config{Dir: dir, Peers: coordinator.SystemPeerCredentials{}, Owner: coordinator.SystemPathOwner{}, SelfUID: uint32(uid), Auth: auth, Dispatch: askEndpointDispatcher{}, Logger: logger}) //nolint:gosec // uid is checked against the uint32 range above.
	if err != nil {
		t.Fatal(err)
	}
	minter := turnbearer.NewMinter(ep, helper)
	auth.minter = minter
	if err := ep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	c := pi.NewWithBase(dir, env, agentharness.Base{Environment: func(context.Context) ([]string, error) { return env, nil }, Tools: minter.ToolSessions()})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return prefixConversation{Harness: c, observations: auth}, fake
}

type prefixRemoteOpener struct{ helperOpenTestOpener }

func (o *prefixRemoteOpener) OpenHosted(ctx context.Context, cfg session.Config, route string) (HostedSessionOpen, bool, error) {
	if cfg.PaneID != locatePaneThis {
		return HostedSessionOpen{}, false, nil
	}
	cfg.Kind = session.KindRemote
	cfg.Host = "prefix.remote.example"
	return o.helperOpenTestOpener.OpenHosted(ctx, cfg, route)
}

func TestAgentAskRealPiPrefixAcrossPaneHostAndNarrowGrant(t *testing.T) {
	backend, fake := realPrefixPi(t,
		fakemodel.Response{Text: []string{"first"}}, fakemodel.Response{Text: []string{"second"}}, fakemodel.Response{Text: []string{"third"}})
	policy := autonomousPolicyStore(t)
	const remote = "0123456789abcdef0123456789abcdee"
	opener := &prefixRemoteOpener{helperOpenTestOpener: helperOpenTestOpener{id: session.ID(remote)}}
	h := newAskHarnessWithOpts(t, answerScript("unused"), WithAgentHarness("pi", backend), WithAgentPolicy(policy), WithHelperSessionOpener(opener))
	var ok bool
	opener.reg, ok = h.ws.registry.(*session.Reg)
	if !ok {
		t.Fatal("ask harness must use the real session registry")
	}
	seedPaneChain(t, h.db, locatePaneOther, locatePaneThis)
	local, err := openSessionInPane(t, h.conn, locatePaneOther, 1)
	if err != nil {
		t.Fatal(err)
	}
	remoteID, remoteErr := openSessionInPane(t, h.conn, locatePaneThis, 2)
	if remoteErr != nil || remoteID != remote {
		t.Fatalf("remote open: %s %+v", remoteID, remoteErr)
	}
	ask := func(id, sid, cwd string) {
		t.Helper()
		result, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": id, "sessionId": sid, "question": "where am I?", "cwd": cwd, "attachedContent": []any{}}, 10)
		if rpcErr != nil {
			t.Fatalf("ask: %+v", rpcErr)
		}
		_, state, reason := collectAnswer(t, h, result.RunID, 20*time.Second)
		if state != "completed" {
			t.Fatalf("state=%s reason=%s", state, reason)
		}
	}
	ask("prefix-local", local, "/prefix/local")
	ask("prefix-remote", remote, "/prefix/remote")
	narrow := autonomousMatrixForTests()
	narrow.Observe.Scopes = []content.GrantScope{{Kind: content.ResourcePath, ID: "/prefix/narrow"}}
	narrow.MutateReversible.Decision = content.DecisionRefuse
	narrow.MutateDestructive.Decision = content.DecisionRefuse
	narrow.PrivilegeChange.Decision = content.DecisionRefuse
	narrow.Disclose.Decision = content.DecisionRefuse
	narrow.CrossBoundary.Decision = content.DecisionRefuse
	narrow.Delegate.Decision = content.DecisionRefuse
	if err := policy.SetPolicy(narrow); err != nil {
		t.Fatal(err)
	}
	closer, ok := backend.Harness.(interface {
		CloseConversation(context.Context, string) error
	})
	if !ok {
		t.Fatal("pi connector cannot restart its conversation")
	}
	if err := closer.CloseConversation(t.Context(), "0198f2b0-0000-7000-8000-00000000ff01"); err != nil {
		t.Fatal(err)
	}
	ask("prefix-narrow", remote, "/prefix/narrow")
	observations := backend.observations
	observations.mu.Lock()
	instructions := append([]string(nil), observations.instructions...)
	observations.mu.Unlock()
	if len(instructions) != 2 || instructions[0] == "" || instructions[0] != instructions[1] {
		t.Fatalf("MCP initialization instructions changed across restart/narrow grant: %q", instructions)
	}
	requests := fake.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests=%d, want 3", len(requests))
	}
	registry, assembleErr := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if assembleErr != nil {
		t.Fatal(assembleErr)
	}
	var expectedNames []string
	for _, tool := range registry.All() {
		if tool.Narrow != nil {
			expectedNames = append(expectedNames, tool.Name)
		}
	}
	var offeredNames []string
	for _, raw := range requests[0].Tools {
		var tool struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if decodeErr := json.Unmarshal(raw, &tool); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		offeredNames = append(offeredNames, strings.TrimPrefix(tool.Function.Name, "mcp__nocx__"))
	}
	// Pi escapes the dots in MCP names for providers that require identifiers.
	for i, name := range expectedNames {
		expectedNames[i] = strings.ReplaceAll(name, ".", "_")
	}
	if !reflect.DeepEqual(offeredNames, expectedNames) {
		t.Fatalf("MCP catalogue names=%v, want executable registry in order %v", offeredNames, expectedNames)
	}
	for i, request := range requests {
		var first struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(request.Messages[0], &first); err != nil {
			t.Fatal(err)
		}

		if first.Role != "system" && first.Role != "developer" {
			t.Fatalf("first message=%s", request.Messages[0])
		}
		for _, fact := range []string{"/prefix/local", "/prefix/remote", "/prefix/narrow", "prefix.remote.example"} {
			if strings.Contains(string(first.Content), fact) {
				t.Errorf("request %d put turn fact %q in system", i, fact)
			}
		}
		if i == 0 {
			continue
		}
		previous := requests[i-1]
		if !bytes.Equal(previous.Messages[0], request.Messages[0]) {
			t.Errorf("request %d changed system/MCP instructions", i)
		}
		if !reflect.DeepEqual(previous.Tools, request.Tools) {
			t.Errorf("request %d changed ordered tool declarations across pane/grant", i)
		}
		if len(request.Messages) < len(previous.Messages) || !reflect.DeepEqual(previous.Messages, request.Messages[:len(previous.Messages)]) {
			t.Errorf("request %d did not preserve ordered model-input prefix", i)
		}
	}
	for i, want := range []string{"/prefix/local", "/prefix/remote", "/prefix/narrow"} {
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(requests[i].Messages[len(requests[i].Messages)-1], &message); err != nil {
			t.Fatal(err)
		}
		if message.Role != "user" || !strings.Contains(string(message.Content), want) {
			t.Errorf("turn %d user message=%+v, want cwd %s", i, message, want)
		}
		if i > 0 && !strings.Contains(string(message.Content), "prefix.remote.example") {
			t.Errorf("turn %d user message lost remote host: %s", i, message.Content)
		}
	}
}
