package claudeconformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/mcpstdio"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
)

var workerTools = []string{
	"workers.holdings",
	"workers.spawn",
	"workers.say",
	"workers.wait",
	"workers.close",
}

// TestMCPBridgeProcess is the child process named by the staged MCP config.
// It deliberately lives in the test binary so this check exercises the real
// mcpstdio adapter without building or finding a second bridge executable.
func TestMCPBridgeProcess(t *testing.T) {
	socket := os.Getenv("NOCX_CONFORMANCE_ENDPOINT")
	if socket == "" {
		return
	}
	if marker := os.Getenv("NOCX_CONFORMANCE_FAILURE_MARKER"); marker != "" {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("failure fixture unexpectedly reached endpoint %q", socket)
		}
		if writeErr := os.WriteFile(marker, []byte(err.Error()+"\n"), 0o600); writeErr != nil {
			t.Fatalf("write failure marker: %v", writeErr)
		}
	}
	if err := mcpstdio.Serve(context.Background(), os.Stdin, os.Stdout, socket); err != nil {
		t.Fatalf("MCP bridge: %v", err)
	}
}

func TestClaudeOffersAndInvokesEveryWorkerSurface(t *testing.T) {
	claude := requireClaude(t)
	endpoint := startCatalogueEndpoint(t)
	config := writeMCPConfig(t, endpoint.socket, "")

	stdout, stderr, err := runClaude(t, claude, filepath.Dir(config), os.Environ(), config,
		"Call the workers.holdings tool exactly once. It is read-only. After receiving its result, answer exactly READY.")
	if err != nil {
		t.Fatalf("claude failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	seenCatalogue := endpoint.waitForMethod(t, "tools.catalogue")
	var catalogue struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(seenCatalogue.result, &catalogue); err != nil {
		t.Fatalf("decode catalogue: %v", err)
	}
	seen := make(map[string]bool, len(catalogue.Tools))
	for _, tool := range catalogue.Tools {
		seen[tool.Name] = true
	}
	for _, want := range workerTools {
		if !seen[want] {
			// This assertion is intentionally tied to the tools/list response:
			// when Claude's MCP contract changes and tools/list stops arriving,
			// this check must fail instead of reporting an empty surface as green.
			t.Errorf("tools/list omitted %q; offered tools = %v", want, seen)
		}
	}

	call := endpoint.waitForMethod(t, "workers.holdings")
	if string(call.result) != `{"held":[]}` {
		t.Fatalf("workers.holdings result = %s, want the real bridge answer", call.result)
	}
}

func TestClaudeStrictMCPConfigExcludesDeveloperServer(t *testing.T) {
	claude := requireClaude(t)
	endpoint := startCatalogueEndpoint(t)
	project := t.TempDir()
	marker := filepath.Join(project, "developer-started")
	developer := map[string]any{
		"mcpServers": map[string]any{
			"developer": map[string]any{
				"type":    "stdio",
				"command": "/bin/sh",
				"args":    []string{"-c", "printf started > \"$1\"; cat", "sh", marker},
			},
		},
	}
	writeJSON(t, filepath.Join(project, ".mcp.json"), developer)
	config := writeMCPConfig(t, endpoint.socket, "")

	stdout, stderr, err := runClaude(t, claude, project, os.Environ(), config,
		"Reply with exactly READY without using any tools.")
	if err != nil {
		t.Fatalf("claude strict launch failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	endpoint.waitForMethod(t, "tools.catalogue")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("developer MCP server was started under --strict-mcp-config: stat error = %v", err)
	}
}

func TestClaudeEndpointFailurePublishesUnavailableFact(t *testing.T) {
	claude := requireClaude(t)
	failureMarker := filepath.Join(t.TempDir(), "bridge-failure")
	h := startSurfaceHarness(t)
	missingSocket := filepath.Join(t.TempDir(), "missing-tool.sock")
	config := writeMCPConfig(t, missingSocket, failureMarker)

	parentEnv := h.parentEnv
	stdout, stderr, err := runClaude(t, claude, t.TempDir(), parentEnv, config,
		"Reply with exactly READY.")
	if err != nil {
		t.Fatalf("claude failed while reporting bridge failure: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	waitForPath(t, failureMarker, func(info os.FileInfo, err error) bool { return err == nil && info.Size() > 0 })
	failureRaw, readErr := os.ReadFile(failureMarker) // #nosec G304 — marker is created under this test's TempDir.
	if readErr != nil {
		t.Fatalf("read bridge failure marker: %v", readErr)
	}
	failureReason := strings.TrimSpace(string(failureRaw))
	if failureReason == "" {
		t.Fatal("bridge failure marker is empty")
	}

	// The bridge has now observed the endpoint failure. Feed that real refusal
	// fact into the existing session.toolSurfaceChanged publisher and assert
	// the user-facing control-plane notification, rather than treating Claude's
	// quiet exit as evidence.
	h.observer.Observe(toolendpoint.Observation{
		SessionID: h.sessionID,
		Kind:      toolendpoint.ObservationRefusal,
		Reason:    failureReason,
	})
	fact := h.readToolSurfaceFact(t)
	if fact.Status != "unavailable" || fact.SessionID != h.sessionID || fact.Reason != failureReason {
		t.Fatalf("tool-surface fact = %+v, want unavailable for session %q with reason %q", fact, h.sessionID, failureReason)
	}
}

func TestLaunchDirectoryIsRemovedAfterNormalAndKilledShells(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required for launch cleanup conformance: %v", err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not identify the conformance test")
	}
	script := filepath.Join(filepath.Dir(source), "..", "shellintegration", "scripts", "nocx.bash")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("shell integration script is unavailable: %v", err)
	}

	t.Run("normal exit", func(t *testing.T) {
		// #nosec G204 — bash is LookPath-validated above.
		cmd := exec.Command(bash, "-c", `set -u
# A login shell sources nocx.bash without errexit; enabling it only after the
# source keeps a missing lifecycle channel from aborting the fixture mid-file.
. "$1"
set -e
__nocx_agent_stage /bin/true /tmp/nocx-conformance-tool.sock
path="$__nocx_agent_launch_dir"
__nocx_agent_capture_traps
printf '%s\n' "$path"
__nocx_agent_cleanup
__nocx_agent_restore_traps
		`, "bash", script)
		cmd.Env = append(os.Environ(), "NOCX_SHELL_INTEGRATION=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("normal launch probe: %v\noutput:\n%s", err, out)
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			t.Fatal("normal launch probe did not report its directory")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("normal launch directory still exists: %v", err)
		}
	})

	t.Run("killed shell", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launch-path")
		// #nosec G204 — bash is LookPath-validated above.
		cmd := exec.Command(bash, "-c", `set -u
# A login shell sources nocx.bash without errexit; enabling it only after the
# source keeps a missing lifecycle channel from aborting the fixture mid-file.
. "$1"
set -e
__nocx_agent_stage /bin/true /tmp/nocx-conformance-tool.sock
path="$__nocx_agent_launch_dir"
__nocx_agent_capture_traps
printf '%s\n' "$path" > "$2"
sleep 1000
		`, "bash", script, marker)
		cmd.Env = append(os.Environ(), "NOCX_SHELL_INTEGRATION=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start killed launch probe: %v", err)
		}
		path := readMarkerPath(t, marker)
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatalf("kill launch shell: %v", err)
		}
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		select {
		case err := <-waited:
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("killed launch probe: %v", err)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("killed launch probe did not exit")
		}
		waitForPath(t, path, func(_ os.FileInfo, err error) bool { return errors.Is(err, os.ErrNotExist) })
	})
}

type mcpCall struct {
	method string
	result json.RawMessage
}

type catalogueEndpoint struct {
	listener net.Listener
	calls    chan mcpCall
	close    chan struct{}
	once     sync.Once
	socket   string
}

func startCatalogueEndpoint(t *testing.T) *catalogueEndpoint {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen endpoint: %v", err)
	}
	endpoint := &catalogueEndpoint{
		listener: listener,
		calls:    make(chan mcpCall, 32),
		close:    make(chan struct{}),
		socket:   path,
	}
	t.Cleanup(endpoint.shutdown)
	go endpoint.accept()
	return endpoint
}

func (e *catalogueEndpoint) accept() {
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			select {
			case <-e.close:
				return
			default:
			}
			continue
		}
		go e.serve(conn)
	}
}

func (e *catalogueEndpoint) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || request.Method == "" {
			continue
		}
		switch request.Method {
		case "tools.catalogue":
			result := map[string]any{"tools": catalogueTools()}
			raw, _ := json.Marshal(result)
			e.calls <- mcpCall{method: request.Method, result: raw}
			e.write(conn, request.ID, raw)
		case "workers.holdings", "workers.spawn", "workers.say", "workers.wait", "workers.close":
			raw := json.RawMessage(`{"held":[]}`)
			e.calls <- mcpCall{method: request.Method, result: raw}
			e.write(conn, request.ID, raw)
		default:
			raw := json.RawMessage(`{"error":"unknown method"}`)
			e.writeError(conn, request.ID, raw)
		}
	}
}

func (e *catalogueEndpoint) write(conn net.Conn, id, result json.RawMessage) {
	response := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	_ = json.NewEncoder(conn).Encode(response)
}

func (e *catalogueEndpoint) writeError(conn net.Conn, id, message json.RawMessage) {
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32601, "message": string(message)},
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (e *catalogueEndpoint) waitForMethod(t *testing.T, want string) mcpCall {
	t.Helper()
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case call := <-e.calls:
			if call.method == want {
				return call
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for endpoint method %q", want)
			return mcpCall{}
		}
	}
}

func (e *catalogueEndpoint) shutdown() {
	e.once.Do(func() {
		close(e.close)
		_ = e.listener.Close()
	})
}

func catalogueTools() []map[string]any {
	tools := make([]map[string]any, 0, len(workerTools))
	for _, name := range workerTools {
		tools = append(tools, map[string]any{
			"name":    name,
			"summary": "A coordinator worker operation.",
			"params":  json.RawMessage(`{"type":"object"}`),
			"result":  json.RawMessage(`{"type":"object"}`),
		})
	}
	return tools
}

func writeMCPConfig(t *testing.T, socket, failureMarker string) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "mcp.json")
	env := map[string]string{"NOCX_CONFORMANCE_ENDPOINT": socket}
	if failureMarker != "" {
		env["NOCX_CONFORMANCE_FAILURE_MARKER"] = failureMarker
	}
	writeJSON(t, config, map[string]any{
		"mcpServers": map[string]any{
			"nocx": map[string]any{
				"type":    "stdio",
				"command": os.Args[0],
				"args":    []string{"-test.run=TestMCPBridgeProcess"},
				"env":     env,
			},
		},
	})
	return config
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func requireClaude(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("Claude Code is not installed; install claude before running this package: %v", err)
	}
	status := exec.Command(path, "auth", "status") // #nosec G204 — path is LookPath-validated; fixed auth probe.
	if out, err := status.CombinedOutput(); err != nil {
		t.Fatalf("Claude Code is installed but authentication status failed; run claude auth status: %v\n%s", err, out)
	} else if !bytes.Contains(out, []byte(`"loggedIn": true`)) && !bytes.Contains(out, []byte(`"loggedIn":true`)) {
		t.Fatalf("Claude Code is installed but not authenticated; run claude auth login\n%s", out)
	}
	return path
}

// All vendor assertions use --print. The design's eager tools/list measurement
// was interactive; this check does not prove interactive and --print equivalent.
func runClaude(t *testing.T, claude, dir string, env []string, config, prompt string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude,
		"--print", "--no-session-persistence", "--dangerously-skip-permissions",
		"--strict-mcp-config", "--mcp-config", config, "-p", prompt) // #nosec G204 — claude is LookPath-validated and config/prompt are test-owned.
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("Claude Code did not finish within 90s: %v\nstdout:\n%s\nstderr:\n%s", ctx.Err(), stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), err
}

type surfaceHarness struct {
	observer  toolSurfaceObserver
	parentEnv []string
	sessionID string
	conn      *websocket.Conn
}

type toolSurfaceObserver struct {
	ws *transport.WSServer
}

func (o toolSurfaceObserver) Observe(observation toolendpoint.Observation) {
	status := "unavailable"
	if observation.Kind == toolendpoint.ObservationCatalogue {
		status = "available"
	}
	o.ws.BroadcastToolSurface(observation.SessionID, status, observation.Reason)
}

func startSurfaceHarness(t *testing.T) *surfaceHarness {
	t.Helper()
	parentEnv := os.Environ()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, stubPTYFactory{logger: logger})
	ws := transport.NewWSServer(logger, reg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := ws.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start surface transport: %v", err)
	}
	conn := dialTransport(t, ws)
	sid := openSession(t, conn)
	h := &surfaceHarness{
		observer:  toolSurfaceObserver{ws: ws},
		parentEnv: parentEnv,
		sessionID: sid,
		conn:      conn,
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = ws.Stop(ctx)
		cancel()
	})
	return h
}

type stubPTYFactory struct {
	logger log.Logger
}

func (f stubPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return pty.NewStub(f.logger), nil
}

func dialTransport(t *testing.T, server *transport.WSServer) *websocket.Conn {
	t.Helper()
	u, err := url.Parse("ws://" + server.Addr() + "/session")
	if err != nil {
		t.Fatalf("parse transport URL: %v", err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{"nocx.token." + server.Token()}}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial transport: %v", err)
	}
	return conn
}

func openSession(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "open",
		"params":  map[string]uint16{"cols": 80, "rows": 24},
	}
	if err := conn.WriteJSON(request); err != nil {
		t.Fatalf("write open: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("open deadline: %v", err)
	}
	for {
		messageType, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read open: %v", err)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				SessionID string `json:"sessionId"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &response); err != nil || string(response.ID) != "1" {
			continue
		}
		if len(response.Error) != 0 && string(response.Error) != "null" {
			t.Fatalf("open error: %s", response.Error)
		}
		if response.Result.SessionID == "" {
			t.Fatalf("open returned no session id: %s", raw)
		}
		return response.Result.SessionID
	}
}

type toolSurfaceFact struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}

func (h *surfaceHarness) readToolSurfaceFact(t *testing.T) toolSurfaceFact {
	t.Helper()
	if err := h.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("tool-surface deadline: %v", err)
	}
	for {
		messageType, raw, err := h.conn.ReadMessage()
		if err != nil {
			t.Fatalf("read tool-surface notification: %v", err)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Method != "session.toolSurfaceChanged" {
			continue
		}
		var fact toolSurfaceFact
		if err := json.Unmarshal(envelope.Params, &fact); err != nil {
			t.Fatalf("decode tool-surface notification: %v", err)
		}
		return fact
	}
}

func readMarkerPath(t *testing.T, path string) string {
	t.Helper()
	var result string
	waitForPath(t, path, func(_ os.FileInfo, err error) bool {
		if err != nil {
			return false
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 — path is the test's own temporary marker.
		if readErr != nil {
			return false
		}
		result = strings.TrimSpace(string(raw))
		return result != ""
	})
	return result
}

func waitForPath(t *testing.T, path string, predicate func(os.FileInfo, error) bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Stat(path)
		if predicate(info, err) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for path condition on %q", path)
		case <-ticker.C:
		}
	}
}
