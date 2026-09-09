package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServeInitializesAbsorbsNotificationAndLists(t *testing.T) {
	socket := startEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method != "tools.catalogue" {
			t.Errorf("endpoint method = %q, want tools.catalogue", request.Method)
		}
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"tools":[{"name":"alpha.run","summary":"run alpha","params":{"type":"object"},"result":{"type":"object"}}]}`)})
	})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"client","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	}, "\n") + "\n"
	output := runAdapter(t, socket, input)
	lines := splitJSONLines(t, output)
	if len(lines) != 2 {
		t.Fatalf("responses = %d, want 2 (notification must be silent): %s", len(lines), output)
	}

	var initialized struct {
		Result struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	mustDecode(t, lines[0], &initialized)
	if initialized.Result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("negotiated protocol = %q", initialized.Result.ProtocolVersion)
	}
	if _, ok := initialized.Result.Capabilities["tools"]; !ok {
		t.Fatalf("initialize capabilities = %v, want tools", initialized.Result.Capabilities)
	}
	var toolsCapability map[string]json.RawMessage
	if err := json.Unmarshal(initialized.Result.Capabilities["tools"], &toolsCapability); err != nil {
		t.Fatalf("decode tools capability: %v", err)
	}
	if _, ok := toolsCapability["listChanged"]; ok {
		t.Fatal("initialize advertised unsupported tools/listChanged")
	}

	var listed struct {
		Result struct {
			Tools []struct {
				Name         string          `json:"name"`
				Description  string          `json:"description"`
				InputSchema  json.RawMessage `json:"inputSchema"`
				OutputSchema json.RawMessage `json:"outputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	mustDecode(t, lines[1], &listed)
	if len(listed.Result.Tools) != 1 || listed.Result.Tools[0].Name != "alpha.run" {
		t.Fatalf("translated tools = %s", lines[1])
	}
	if listed.Result.Tools[0].Description != "run alpha" || string(listed.Result.Tools[0].InputSchema) != `{"type":"object"}` || string(listed.Result.Tools[0].OutputSchema) != `{"type":"object"}` {
		t.Fatalf("translated schemas = %+v", listed.Result.Tools[0])
	}
}

func TestServeForwardsCallAndWrapsDomainRefusal(t *testing.T) {
	var mu sync.Mutex
	var forwarded []rpcEnvelope
	socket := startEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		mu.Lock()
		forwarded = append(forwarded, request)
		mu.Unlock()
		if request.Method == "tools.catalogue" {
			writeJSONLine(t, conn, rpcEnvelope{
				JSONRPC: "2.0",
				ID:      request.ID,
				Result:  json.RawMessage(`{"tools":[{"name":"alpha.run","summary":"run alpha","params":{"type":"object"},"result":{"type":"object"}}]}`),
			})
			return
		}
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Error: &rpcErrorEnvelope{Code: -32000, Message: "refused", Data: map[string]string{"reason": "permission denied"}}})
	})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"alpha.run","arguments":{"value":7},"_meta":{"ignored":true}}}`,
	}, "\n") + "\n"
	output := runAdapter(t, socket, input)
	lines := splitJSONLines(t, output)
	if len(lines) != 2 {
		t.Fatalf("responses = %d, want 2: %s", len(lines), output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forwarded) != 2 {
		t.Fatalf("endpoint requests = %d, want 2", len(forwarded))
	}
	if forwarded[0].Method != "tools.catalogue" || forwarded[1].Method != "alpha.run" || string(forwarded[1].Params) != `{"value":7}` {
		t.Fatalf("forwarded requests = %+v", forwarded)
	}

	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	mustDecode(t, lines[1], &response)
	if response.Error != nil || !response.Result.IsError || len(response.Result.Content) != 1 || response.Result.Content[0].Text != "permission denied" {
		t.Fatalf("refusal response = %s", lines[1])
	}
}

func TestServeNamesEndpointFailureAndDoesNotReturnEmptyTools(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "missing.sock")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"alpha.run","arguments":{}}}`,
	}, "\n") + "\n"
	lines := splitJSONLines(t, runAdapter(t, socket, input))
	if len(lines) != 3 {
		t.Fatalf("responses = %d, want 3", len(lines))
	}
	for _, line := range lines[1:] {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
				Data    struct {
					Reason string `json:"reason"`
				} `json:"data"`
			} `json:"error"`
		}
		mustDecode(t, line, &envelope)
		if string(envelope.ID) == "0" {
			continue
		}
		if envelope.Error == nil && !envelope.Result.IsError {
			t.Fatalf("endpoint failure was empty success: %s", line)
		}
		if envelope.Error != nil && !strings.Contains(envelope.Error.Data.Reason+envelope.Error.Message, "endpoint") {
			t.Fatalf("endpoint failure was unnamed: %s", line)
		}
	}
}

func TestServeReconnectsAfterIdle(t *testing.T) {
	var mu sync.Mutex
	connections := 0
	listener := listenEndpoint(t)
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections++
			mu.Unlock()
			go func() {
				defer func() { _ = conn.Close() }()
				request, err := readJSONLine(conn)
				if err == nil {
					if request.Method == "tools.catalogue" {
						writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"tools":[{"name":"alpha.one","summary":"one","params":{"type":"object"},"result":{"type":"object"}},{"name":"alpha.two","summary":"two","params":{"type":"object"},"result":{"type":"object"}}]}`)})
					} else {
						writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"value":true}`)})
					}
				}
			}()
		}
	}()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"alpha.one","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"alpha.two","arguments":{}}}`,
	}, "\n") + "\n"
	lines := splitJSONLines(t, runAdapter(t, listener.Addr().String(), input))
	if len(lines) != 3 {
		t.Fatalf("responses = %d, want 3: %s", len(lines), strings.Join(lines, "\n"))
	}
	for _, line := range lines[1:] {
		var response struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				StructuredContent json.RawMessage `json:"structuredContent"`
				IsError           bool            `json:"isError"`
			} `json:"result"`
		}
		mustDecode(t, line, &response)
		if response.Result.IsError || len(response.Result.Content) != 1 || response.Result.Content[0].Text != `{"value":true}` || string(response.Result.StructuredContent) != `{"value":true}` {
			t.Fatalf("wrapped success = %s", line)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 3 {
		t.Fatalf("endpoint connections = %d, want 3 (catalogue refresh plus two calls)", connections)
	}
}

func TestSourceContainsNoDomainVocabulary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, forbidden := range []string{"workers.spawn", "workers.say", "workers.wait", "workers.holdings", "workers.close", "worker"} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Errorf("%s contains forbidden domain vocabulary %q", entry.Name(), forbidden)
			}
		}
	}
}

type rpcEnvelope struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Params  json.RawMessage   `json:"params,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *rpcErrorEnvelope `json:"error,omitempty"`
}

type rpcErrorEnvelope struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    map[string]string `json:"data,omitempty"`
}

func startEndpoint(t *testing.T, handler func(net.Conn, rpcEnvelope)) string {
	t.Helper()
	listener := listenEndpoint(t)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				request, err := readJSONLine(conn)
				if err == nil {
					handler(conn, request)
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func listenEndpoint(t *testing.T) *net.UnixListener {
	t.Helper()
	path := filepath.Join(t.TempDir(), "endpoint.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func runAdapter(t *testing.T, socket, input string) string {
	t.Helper()
	reader, writer := io.Pipe()
	output := &recordingWriter{written: make(chan struct{}, 16)}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), reader, output, socket) }()
	if _, err := io.WriteString(writer, input); err != nil {
		t.Fatal(err)
	}
	expected := 0
	for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &envelope); err == nil {
			if id := envelope["id"]; len(id) > 0 && !bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
				expected++
			}
		}
	}
	for i := range expected {
		select {
		case <-output.written:
		case <-time.After(5 * time.Second):
			t.Fatalf("adapter wrote %d/%d responses", i, expected)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not finish")
	}
	return output.String()
}

type recordingWriter struct {
	mu      sync.Mutex
	data    bytes.Buffer
	written chan struct{}
}

func (w *recordingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(data)
	w.written <- struct{}{}
	return n, err
}

func (w *recordingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func readJSONLine(conn net.Conn) (rpcEnvelope, error) {
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return rpcEnvelope{}, err
	}
	var request rpcEnvelope
	if err := json.Unmarshal(line, &request); err != nil {
		return rpcEnvelope{}, err
	}
	return request, nil
}

func writeJSONLine(t *testing.T, conn net.Conn, value rpcEnvelope) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Errorf("marshal endpoint response: %v", err)
		return
	}
	if _, err := conn.Write(append(data, '\n')); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("write endpoint response: %v", err)
	}
}

func splitJSONLines(t *testing.T, output string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("invalid JSON output line %q", line)
		}
	}
	return lines
}

func mustDecode(t *testing.T, line string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(line), target); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
}
