package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestAgentDump_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.dump.schema.json")
	raw, err := json.Marshal(agentDumpResponse{
		Request:  []agentDumpDrive{{Text: "request", Truncated: false}},
		Response: []agentDumpDrive{{Text: "response", Truncated: true}},
	})
	if err != nil {
		t.Fatalf("marshal agent dump: %v", err)
	}
	validateJSON(t, schema, raw, "agent.dump DTO")
}

func TestAgentDump_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.dump.schema.json")
	ws, db, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	askPaneIn(t, db)
	if err := db.Ledger().EnsureEnvironment(context.Background(), content.Environment{
		ID: localEnvironmentID(), Kind: content.EnvLocal,
	}); err != nil {
		t.Fatalf("ensure environment: %v", err)
	}
	entryID := "01930000-0000-7000-8000-0000000000d1"
	if _, err := db.Ledger().Submit(context.Background(), content.SubmitEntry{
		ID:            entryID,
		Client:        "agent",
		EnvironmentID: localEnvironmentID(),
		Cwd:           "/repo",
		Kind:          content.EntryAsk,
		Intent:        "explain this",
		PaneID:        strPtr(askPaneID),
		Source:        content.SourceUser,
	}); err != nil {
		t.Fatalf("submit turn: %v", err)
	}
	appendDumpArtifact(t, db, entryID, "01930000-0000-7000-8000-0000000000d2", "request", []byte("raw request"))
	appendDumpArtifact(t, db, entryID, "01930000-0000-7000-8000-0000000000d3", "response", []byte("raw response"))

	frame := jsonrpcCallWithID(t, conn, "agent.dump", map[string]any{"entryId": entryID}, 1)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		t.Fatalf("decode agent.dump response: %v\nraw: %s", err, frame)
	}
	if envelope.Error != nil {
		t.Fatalf("agent.dump returned error: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "agent.dump over the wire")
	var got agentDumpResponse
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode dump result: %v", err)
	}
	if len(got.Request) != 1 || got.Request[0].Text != "raw request" {
		t.Fatalf("request dump = %+v", got.Request)
	}
	if len(got.Response) != 1 || got.Response[0].Text != "raw response" {
		t.Fatalf("response dump = %+v", got.Response)
	}

	emptyID := "01930000-0000-7000-8000-0000000000d4"
	if _, err := db.Ledger().Submit(context.Background(), content.SubmitEntry{
		ID: emptyID, Client: "agent", EnvironmentID: localEnvironmentID(), Cwd: "/repo",
		Kind: content.EntryAsk, Intent: "no capture", PaneID: strPtr(askPaneID),
		Source: content.SourceUser,
	}); err != nil {
		t.Fatalf("submit empty turn: %v", err)
	}
	emptyFrame := jsonrpcCallWithID(t, conn, "agent.dump", map[string]any{"entryId": emptyID}, 2)
	var emptyEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(emptyFrame, &emptyEnvelope); err != nil {
		t.Fatalf("decode empty dump response: %v", err)
	}
	if emptyEnvelope.Error != nil {
		t.Fatalf("empty agent.dump returned error: %+v", emptyEnvelope.Error)
	}
	validateJSON(t, schema, emptyEnvelope.Result, "empty agent.dump over the wire")
	var empty agentDumpResponse
	if err := json.Unmarshal(emptyEnvelope.Result, &empty); err != nil {
		t.Fatalf("decode empty dump result: %v", err)
	}
	if empty.Request == nil || empty.Response == nil {
		t.Fatalf("empty dump arrays must be non-nil: %+v", empty)
	}
}

func appendDumpArtifact(t *testing.T, db content.ContentDB, entryID, artifactID, wire string, body []byte) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"wire": wire, "runId": "1"})
	if err != nil {
		t.Fatalf("marshal dump marker: %v", err)
	}
	if _, err := db.Ledger().AppendArtifact(context.Background(), content.AppendArtifact{
		ID: artifactID, EntryID: entryID, MediaType: content.MediaText,
		CaptureMethod: content.CaptureRawOutput, CaptureVersion: 1,
		Payload: string(payload),
	}); err != nil {
		t.Fatalf("append %s artifact: %v", wire, err)
	}
	if err := db.Ledger().AppendChunk(context.Background(), artifactID, 1, body); err != nil {
		t.Fatalf("append %s chunk: %v", wire, err)
	}
}
