package mcpstdio

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
)

func TestUnknownCallIsRefusedWithoutForwarding(t *testing.T) {
	var mu sync.Mutex
	var requests []rpcEnvelope
	socket := startEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		if request.Method == "tools.catalogue" {
			writeJSONLine(t, conn, rpcEnvelope{
				JSONRPC: "2.0",
				ID:      request.ID,
				Result:  json.RawMessage(`{"tools":[{"name":"alpha.run","summary":"run alpha","params":{"type":"object"},"result":{"type":"object"}}]}`),
			})
		}
	})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"alpha.missing","arguments":{}}}`,
	}, "\n") + "\n"
	lines := splitJSONLines(t, runAdapter(t, socket, input))
	if len(lines) != 2 {
		t.Fatalf("responses = %d, want 2: %s", len(lines), strings.Join(lines, "\n"))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0].Method != "tools.catalogue" {
		t.Fatalf("endpoint requests = %+v, want only tools.catalogue", requests)
	}
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	mustDecode(t, lines[1], &response)
	if !response.Result.IsError || len(response.Result.Content) != 1 || !strings.Contains(response.Result.Content[0].Text, "alpha.missing") {
		t.Fatalf("unknown call response = %s", lines[1])
	}
}
