package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

const resolverRepository = "agentmail-to/agentmail-skills"

var resolverCandidatePaths = []string{
	"agentmail/SKILL.md",
	"agentmail-cli/SKILL.md",
	"agentmail-check-email/SKILL.md",
	"agentmail-send-email/SKILL.md",
	"inboxes/SKILL.md",
	"threads/SKILL.md",
	"webhooks/SKILL.md",
	"agentmail-mcp/SKILL.md",
	"agentmail-automation/SKILL.md",
}

type resolverForge struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []string
	files    map[string]string
}

func newResolverForge(t *testing.T) *resolverForge {
	t.Helper()
	f := &resolverForge{files: make(map[string]string, len(resolverCandidatePaths))}
	for _, path := range resolverCandidatePaths {
		name := strings.TrimSuffix(strings.TrimPrefix(path, ""), "/SKILL.md")
		f.files[path] = fmt.Sprintf("---\nname: %s\ndescription: %s skill\n---\n# %s\n\nThis is the selected forge bundle.\n", strings.ReplaceAll(name, "/", "-"), name, name)
	}
	f.files[resolverCandidatePaths[0]] = "---\nname: agentmail\ndescription: \"AgentMail integrations for inboxes, threads, and webhooks.\"\n---\n# AgentMail\n\nUse AgentMail to manage inboxes, threads, and webhooks.\n"
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path)
		f.mu.Unlock()

		switch r.URL.Path {
		case "/docs/integrations/skills":
			_, _ = io.WriteString(w, "AgentMail skills live in https://github.com/"+resolverRepository+".\n")
		case "/api/repos/" + resolverRepository:
			writeResolverJSON(w, map[string]any{"default_branch": "main"})
		case "/api/repos/" + resolverRepository + "/commits/main":
			writeResolverJSON(w, map[string]any{"sha": "commit-123"})
		case "/api/repos/" + resolverRepository + "/git/trees/commit-123":
			tree := make([]map[string]string, 0, len(resolverCandidatePaths))
			for _, path := range resolverCandidatePaths {
				tree = append(tree, map[string]string{"path": path, "type": "blob"})
			}
			writeResolverJSON(w, map[string]any{"truncated": false, "tree": tree})
		default:
			const rawPrefix = "/raw/" + resolverRepository + "/commit-123/"
			if strings.HasPrefix(r.URL.Path, rawPrefix) {
				path := strings.TrimPrefix(r.URL.Path, rawPrefix)
				text, ok := f.files[path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, text)
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func writeResolverJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (f *resolverForge) sawPath(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.requests {
		if got == path {
			return true
		}
	}
	return false
}

type resolverModel struct {
	server  *httptest.Server
	pageURL string
	calls   atomic.Int32

	mu                   sync.Mutex
	resolveResultHadNine bool
}

func newResolverModel(t *testing.T, pageURL string) *resolverModel {
	t.Helper()
	m := &resolverModel{pageURL: pageURL}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch m.calls.Add(1) {
		case 1:
			resolverStreamToolCalls(w, resolverToolCallSpec{
				name: "fetch.url",
				args: fmt.Sprintf(`{"url":%q}`, m.pageURL),
			})
		case 2:
			resolverStreamToolCalls(w, resolverToolCallSpec{
				name: "skills.resolve",
				args: fmt.Sprintf(`{"url":%q}`, "https://github.com/"+resolverRepository),
			})
		case 3:
			handle := resolverHandle(string(body))
			m.mu.Lock()
			m.resolveResultHadNine = true
			for _, path := range resolverCandidatePaths {
				if !strings.Contains(string(body), path) {
					m.resolveResultHadNine = false
					break
				}
			}
			m.mu.Unlock()
			if handle == "" {
				http.Error(w, "missing resolution handle", http.StatusBadRequest)
				return
			}
			resolverStreamToolCalls(w, resolverToolCallSpec{
				name: "skills.install",
				args: fmt.Sprintf(`{"handle":%q,"paths":[%q]}`, handle, resolverCandidatePaths[0]),
			})
		default:
			resolverStreamAnswer(w, "Installed agentmail from the resolved AgentMail repository.")
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func resolverHandle(body string) string {
	const prefix = "resolution-"
	at := strings.Index(body, prefix)
	if at < 0 {
		return ""
	}
	end := at + len(prefix)
	for end < len(body) && body[end] >= '0' && body[end] <= '9' {
		end++
	}
	if end == at+len(prefix) {
		return ""
	}
	return body[at:end]
}

type resolverToolCallSpec struct {
	name string
	args string
	id   string
}

func resolverStreamToolCalls(w http.ResponseWriter, calls ...resolverToolCallSpec) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	toolCalls := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		id := call.id
		if id == "" {
			id = fmt.Sprintf("resolver-call-%d", i+1)
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      call.name,
				"arguments": call.args,
			},
		})
	}
	payload, _ := json.Marshal(map[string]any{
		"id":      "resolver-completion",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "resolver-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "tool_calls": toolCalls},
			"finish_reason": "tool_calls",
		}},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func resolverStreamAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	payload, _ := json.Marshal(map[string]any{
		"id":      "resolver-answer",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "resolver-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func TestSkillsResolverHappyPathThroughProductionWiring(t *testing.T) {
	storagetest.IsolateWithHome(t)
	forge := newResolverForge(t)
	pageURL := forge.server.URL + "/docs/integrations/skills"
	model := newResolverModel(t, pageURL)

	a, err := newTestApp(t, WithSkillForge(forge.server.URL+"/api", forge.server.URL+"/raw"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(context.Background()); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	defer a.Shutdown(context.Background())

	conn, _, err := (&websocket.Dialer{
		Subprotocols: []string{"nocx.token." + a.Transport.Token()},
	}).Dial(fmt.Sprintf("ws://127.0.0.1:%d/session", a.Transport.Port()), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	openRaw := jsonrpcCallRaw(t, conn, "open", map[string]any{"cols": 80, "rows": 24}, 1)
	var open struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if openErr := json.Unmarshal(openRaw, &open); openErr != nil {
		t.Fatalf("decode open: %v", openErr)
	}
	if open.Result.SessionID == "" {
		t.Fatal("open returned no session id")
	}

	vaultSetup := jsonrpcCallRaw(t, conn, "vault.setup", map[string]any{
		"passphrase": "resolver test passphrase",
	}, 2)
	if strings.Contains(string(vaultSetup), `"error"`) {
		t.Fatalf("vault.setup: %s", vaultSetup)
	}
	endpointRaw := jsonrpcCallRaw(t, conn, "endpoints.create", map[string]any{
		"name":    "Resolver fake model",
		"baseUrl": model.server.URL + "/v1",
		"schema":  "openai-compatible",
		"key":     "sk-resolver-test",
		"models":  []map[string]any{{"name": "resolver-model"}},
	}, 3)
	var endpoint struct {
		Result struct {
			Endpoint struct {
				ID string `json:"id"`
			} `json:"endpoint"`
		} `json:"result"`
	}
	if endpointErr := json.Unmarshal(endpointRaw, &endpoint); endpointErr != nil || endpoint.Result.Endpoint.ID == "" {
		t.Fatalf("endpoints.create: %s", endpointRaw)
	}
	rolesRaw := jsonrpcCallRaw(t, conn, "roles.assign", map[string]any{
		"role":       "answering",
		"endpointId": endpoint.Result.Endpoint.ID,
		"model":      "resolver-model",
	}, 4)
	if strings.Contains(string(rolesRaw), `"error"`) {
		t.Fatalf("roles.assign: %s", rolesRaw)
	}

	askRaw := jsonrpcCallRaw(t, conn, "agent.ask", map[string]any{
		"askId":           "resolver-ask",
		"sessionId":       open.Result.SessionID,
		"question":        "Install the AgentMail skill from https://www.agentmail.to/docs/integrations/skills.",
		"cwd":             "/tmp",
		"attachedContent": []any{},
	}, 5)
	var ask struct {
		Result struct {
			State string `json:"state"`
		} `json:"result"`
	}
	if askErr := json.Unmarshal(askRaw, &ask); askErr != nil {
		t.Fatalf("decode agent.ask: %v", askErr)
	}
	if ask.Result.State != "prepared" {
		t.Fatalf("agent.ask state = %q, want prepared: %s", ask.Result.State, askRaw)
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var approval struct {
		RunID   string `json:"runId"`
		Attempt int    `json:"attempt"`
		Tool    string `json:"tool"`
		CallID  string `json:"callId"`
		ArgHash string `json:"argHash"`
		Install *struct {
			Source        string `json:"source"`
			Destination   string `json:"destination"`
			OriginsDiffer *bool  `json:"originsDiffer"`
			Ref           string `json:"ref"`
			Commit        string `json:"commit"`
			Skills        []struct {
				Path  string `json:"path"`
				Name  string `json:"name"`
				Files []struct {
					Path string `json:"path"`
					Text string `json:"text"`
				} `json:"files"`
			} `json:"skills"`
		} `json:"install"`
	}
	approvalRPCID := 6
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("waiting for approval: %v", readErr)
		}
		var notification struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &notification) != nil || notification.Method != "agent.approvalRequested" {
			continue
		}
		if approvalErr := json.Unmarshal(notification.Params, &approval); approvalErr != nil {
			t.Fatalf("decode approval: %v", approvalErr)
		}
		if approval.Install != nil {
			break
		}
		if approval.Tool != "fetch.url" && approval.Tool != "skills.resolve" {
			t.Fatalf("unexpected preliminary approval for %q", approval.Tool)
		}
		autoApproveRaw := jsonrpcCallRaw(t, conn, "agent.approve", map[string]any{
			"runId":    approval.RunID,
			"attempt":  approval.Attempt,
			"tool":     approval.Tool,
			"callId":   approval.CallID,
			"argHash":  approval.ArgHash,
			"approved": true,
			"scope":    "once",
		}, approvalRPCID)
		if strings.Contains(string(autoApproveRaw), `"error"`) {
			t.Fatalf("preliminary agent.approve: %s", autoApproveRaw)
		}
		approvalRPCID++
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
	if approval.Install == nil {
		t.Fatal("approval omitted resolved install facts")
	}
	// THE SIXTH FACT (nocx-b6stz). The source is where the route STARTED —
	// the documentation page nocx itself fetched, whose bytes name the
	// repository — and NOT the address the model handed to skills.resolve,
	// which is the destination. The two are on different hosts, so the window
	// can say so; while the source was read off the model's arguments the two
	// were always equal and this note could never render.
	if approval.Install.Source != pageURL {
		t.Fatalf("approval source = %q, want the fetched documentation page %q", approval.Install.Source, pageURL)
	}
	if approval.Install.Source == "https://github.com/"+resolverRepository {
		t.Fatal("approval source is the model's own skills.resolve argument: the origin must be established by nocx, not asserted by the model")
	}
	if approval.Install.Destination != resolverRepository {
		t.Fatalf("approval destination = %q, want %q", approval.Install.Destination, resolverRepository)
	}
	if approval.Install.OriginsDiffer == nil || !*approval.Install.OriginsDiffer {
		t.Fatalf("approval originsDiffer = %v, want true: the page is on %s and the repository is on github.com",
			approval.Install.OriginsDiffer, pageURL)
	}
	if approval.Install.Ref != "main" || approval.Install.Commit != "commit-123" {
		t.Fatalf("approval pin = ref %q commit %q", approval.Install.Ref, approval.Install.Commit)
	}
	if len(approval.Install.Skills) != 1 || approval.Install.Skills[0].Path != resolverCandidatePaths[0] {
		t.Fatalf("approval skills = %+v", approval.Install.Skills)
	}
	if len(approval.Install.Skills[0].Files) == 0 || approval.Install.Skills[0].Files[0].Path != "SKILL.md" {
		t.Fatalf("approval bundle = %+v", approval.Install.Skills[0].Files)
	}
	shownText := approval.Install.Skills[0].Files[0].Text

	approveRaw := jsonrpcCallRaw(t, conn, "agent.approve", map[string]any{
		"runId":    approval.RunID,
		"attempt":  approval.Attempt,
		"tool":     approval.Tool,
		"callId":   approval.CallID,
		"argHash":  approval.ArgHash,
		"approved": true,
		"scope":    "once",
	}, approvalRPCID)
	if strings.Contains(string(approveRaw), `"error"`) {
		t.Fatalf("agent.approve: %s", approveRaw)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("waiting for run completion: %v", readErr)
		}
		var notification struct {
			Method string `json:"method"`
			Params struct {
				State string `json:"state"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &notification) != nil || notification.Method != "agent.runState" {
			continue
		}
		if notification.Params.State == "completed" {
			break
		}
	}

	paths, err := storage.NewAppPaths()
	if err != nil {
		t.Fatalf("app paths: %v", err)
	}
	diskPath := filepath.Join(paths.ConfigDir(), "installed-skills", "agentmail", "SKILL.md")
	// #nosec G304 -- this path is inside the isolated app profile and uses a fixed skill name.
	diskText, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if !bytes.Equal(diskText, []byte(shownText)) {
		t.Fatalf("installed bytes differ from approval bundle: shown=%q disk=%q", shownText, string(diskText))
	}

	listRaw := jsonrpcCallRaw(t, conn, "skills.list", map[string]any{}, 7)
	var listed skill.ListResult
	var listEnvelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(listRaw, &listEnvelope); err != nil {
		t.Fatalf("decode skills.list envelope: %v", err)
	}
	if err := json.Unmarshal(listEnvelope.Result, &listed); err != nil {
		t.Fatalf("decode skills.list: %v", err)
	}
	found := false
	for _, item := range listed.Skills {
		if item.Name == "agentmail" {
			found = true
			if item.Enabled {
				t.Fatal("installed skill is enabled; external skills must arrive disabled")
			}
		}
	}
	if !found {
		t.Fatalf("skills.list omitted installed agentmail: %+v", listed.Skills)
	}
	if !forge.sawPath("/docs/integrations/skills") {
		t.Fatal("documentation page was not fetched")
	}
	model.mu.Lock()
	resolveResultHadNine := model.resolveResultHadNine
	model.mu.Unlock()
	if !resolveResultHadNine {
		t.Fatal("skills.resolve result did not expose all nine candidates to the model")
	}
	if model.calls.Load() != 4 {
		t.Fatalf("model calls = %d, want fetch, resolve, install, answer", model.calls.Load())
	}
}
