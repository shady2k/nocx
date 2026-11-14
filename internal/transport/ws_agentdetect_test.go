package transport

// agentRecords.detect is the one method Settings reads to draw the agents it
// found: every agent the assistant can run, once, with what the backend saw on
// the person's login shell's PATH.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/agentdetect"
	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/log"
)

// detectionShell answers the login-shell question with a PATH this test made,
// so the detector's real resolution and real probes run against fixtures.
type detectionShell struct{ path string }

func (s detectionShell) Environment(context.Context) ([]string, string, error) {
	cwd, err := os.UserHomeDir()
	return []string{"PATH=" + s.path}, cwd, err
}

func agentDetectionConnection(t *testing.T, detector AgentDetector) (*websocket.Conn, func()) {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithAgentDetection(detector))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, ws)
	return conn, func() { _ = conn.Close(); _ = ws.Stop(ctx) }
}

// The REAL result off the REAL socket, with a login-shell PATH holding a pi
// that prints its version and answers auth check ready, and no claude: pi is
// found, with its version and ready; claude is not installed; every agent of
// the record appears exactly once.
func TestAgentRecordsDetect_OverTheWireConformsToContract(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) printf 'pi 1.5.2\\n' ;;\n  auth) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	conn, cleanup := agentDetectionConnection(t, agentdetect.New(detectionShell{path: dir}))
	defer cleanup()

	resp := jsonrpcCall(t, conn, "agentRecords.detect", map[string]any{})
	var envelope rpcEnvelope
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("agentRecords.detect response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("agentRecords.detect: %+v", envelope.Error)
	}
	validateJSON(t, loadSchema(t, "agentRecords.detect.schema.json"), envelope.Result, "agentRecords.detect wire")

	var result agentRecordsDetectResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	want := agentrecord.SupportedAgents()
	if len(result.Agents) != len(want) {
		t.Fatalf("method answered %d agents, the record holds %d", len(result.Agents), len(want))
	}
	byID := make(map[string]agentDetectionRow, len(result.Agents))
	for _, row := range result.Agents {
		if _, duplicate := byID[row.ID]; duplicate {
			t.Fatalf("agent %q appears more than once: %+v", row.ID, result.Agents)
		}
		byID[row.ID] = row
	}
	for _, agent := range want {
		if _, ok := byID[agent.ID]; !ok {
			t.Fatalf("agent %q is missing from %+v", agent.ID, result.Agents)
		}
	}

	pi := byID["pi"]
	if !pi.Found || pi.Version != "pi 1.5.2" || pi.Readiness != agentrecord.ReadinessReady {
		t.Fatalf("pi = %+v, want found with its version and ready", pi)
	}
	claude := byID["claude"]
	if claude.Found || claude.Version != "" || claude.Readiness != agentrecord.ReadinessNotInstalled {
		t.Fatalf("claude = %+v, want not installed", claude)
	}
}

func TestAgentRecordsDetect_DTOConformsToContract(t *testing.T) {
	rows := []agentDetectionRow{
		{ID: "pi", DisplayName: "pi", Found: true, Version: "pi 1.5.2", Readiness: agentrecord.ReadinessReady},
		{ID: "claude", DisplayName: "Claude Code", Found: false, Version: "", Readiness: agentrecord.ReadinessNotInstalled},
		{ID: "codex", DisplayName: "Codex", Found: true, Version: "", Readiness: agentrecord.ReadinessUnknown},
		{ID: "omp", DisplayName: "omp", Found: true, Version: "omp 18.18.5", Readiness: agentrecord.ReadinessInstalled},
	}
	for _, test := range []struct {
		name  string
		value agentRecordsDetectResult
	}{
		{name: "populated", value: agentRecordsDetectResult{Agents: rows}},
		{name: "empty", value: agentRecordsDetectResult{Agents: []agentDetectionRow{}}},
	} {
		raw, err := json.Marshal(test.value)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		validateJSON(t, loadSchema(t, "agentRecords.detect.schema.json"), raw, test.name+" DTO")
	}
}

func TestAgentRecordsDetectRefreshRejectsNull(t *testing.T) {
	for _, raw := range []string{`{}`, `{"refresh":false}`, `{"refresh":true}`} {
		if reason := validateAgentRecordsDetect(json.RawMessage(raw)); reason != "" {
			t.Fatalf("valid params %s rejected: %s", raw, reason)
		}
	}
	if reason := validateAgentRecordsDetect(json.RawMessage(`{"refresh":null}`)); reason == "" {
		t.Fatal("refresh:null accepted, but the wire schema requires a boolean when present")
	}
}

func TestAgentRecordsDetectRejectsNullRefreshOverTheWire(t *testing.T) {
	conn, cleanup := agentDetectionConnection(t, nil)
	defer cleanup()
	raw := jsonrpcCall(t, conn, "agentRecords.detect", map[string]any{"refresh": nil})
	var response rpcEnvelope
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("null refresh response = %s, want invalid params", raw)
	}
}
