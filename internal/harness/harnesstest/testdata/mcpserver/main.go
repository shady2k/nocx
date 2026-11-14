// The test MCP server is one tiny stdio server for the conformance suite. It
// answers initialize and tools/call, reports the environment it was launched
// with and the value the agent passed in its arguments, and exits when its
// stdin closes — the same pipe fact the agent relies on.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

func main() {
	if os.Getenv("MCP_BLOCK") == "1" {
		// A blocked call never reaches the scan loop again, so the only
		// thing that can end this process is its stdin closing. Nothing
		// else reads stdin in this mode, which is why the watchdog can.
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}()
	}
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
		case "tools/call":
			if os.Getenv("MCP_BLOCK") == "1" {
				// A blocked call: only the death of this process's stdin
				// ends it, which is what Cancel and a dead backend must do.
				select {}
			}
			agentSecret, _ := request.Params.Arguments["agent_secret"].(string)
			text := fmt.Sprintf("server_secret=%s;agent_secret=%s", os.Getenv("NOCX_TEST_SECRET"), agentSecret)
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
