// The test MCP server is one tiny stdio server for the conformance suite. It
// answers initialize and tools/call, reports the environment it was launched
// with and the value the agent passed in its arguments, and exits when its
// stdin closes — the same pipe fact the agent relies on.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func main() {
	if pidFile := os.Getenv("MCP_PIDFILE"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			continue
		}
		switch request.Method {
		case "initialize":
			_ = out.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"protocolVersion": "2025-11-25",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "test-mcp", "version": "1"},
				},
			})
		case "notifications/initialized":
		case "tools/list":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]any{{"name": "probe", "description": "Probe bridge environment", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}}}}}})
		case "tools/call":
			if os.Getenv("MCP_BLOCK") == "1" {
				_ = os.WriteFile(os.Getenv("MCP_BLOCKEDFILE"), []byte("tools/call"), 0o600)
				gate := os.Getenv("MCP_CANCEL_GATE")
				if gate == "" {
					// Keep scanning: one reader owns requests and observes EOF.
					continue
				}
				cancel, err := os.Open(gate)
				if err != nil {
					return
				}
				var signal [1]byte
				_, _ = cancel.Read(signal[:])
				_ = cancel.Close()
				_ = out.Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": "cancelled"}},
						"isError": true,
					},
				})
				continue
			}
			agentSecret, _ := request.Params.Arguments["agent_secret"].(string)
			environment, _ := json.Marshal(request.Params.Arguments["agent_environment"])
			text := fmt.Sprintf("server_secret=%s;agent_secret=%s", os.Getenv("NOCX_TEST_SECRET"), agentSecret)
			text += ";agent_environment=" + string(environment)
			_ = out.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": text}},
					"isError": false,
				},
			})
		default:
			if request.ID != nil {
				_ = out.Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32601, "message": "method not found"},
				})
			}
		}
	}
}
