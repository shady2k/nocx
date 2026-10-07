package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// openRecoveryStatusRecordingStore opens the real content store used by the recording tests.
func openRecoveryStatusRecordingStore(t *testing.T, capBytes int) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(capBytes)
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(t.TempDir(), "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Policy: policy,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func readRecoveryStatus(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, params map[string]any) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "session.recoveryStatus", params)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("session.recoveryStatus: unmarshal: %v\nraw: %s", err, raw)
	}
	return env.Result, env.Error
}

func TestSessionRecoveryStatusIsMetadataOnlyOverTheWire(t *testing.T) {
	const capBytes = 512
	db := openRecoveryStatusRecordingStore(t, capBytes)
	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	sid := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1).SessionID

	if _, err := db.SessionOutput().Append(context.Background(), content.SessionOutputAppend{
		SessionID: sid, Offset: 0, Body: recordingStream(4096),
	}); err != nil {
		t.Fatalf("append recording: %v", err)
	}
	live, _ := liveSessions(t, conn, tap, 2)
	entry := entryFor(t, live, sid)
	result, rpcErr := readRecoveryStatus(t, conn, tap, 3, map[string]any{
		"sessionId": sid, "instanceId": entry.InstanceID, "sessionEpoch": entry.SessionEpoch,
	})
	if rpcErr != nil {
		t.Fatalf("session.recoveryStatus: %+v", rpcErr)
	}
	schema := loadSchema(t, "session.recoveryStatus.schema.json")
	validateJSON(t, schema, result, "session.recoveryStatus result")
	var got struct {
		SessionID string          `json:"sessionId"`
		Produced  uint64          `json:"produced"`
		Gaps      []content.Gap   `json:"gaps"`
		Runs      json.RawMessage `json:"runs"`
		Bytes     json.RawMessage `json:"bytes"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.SessionID != sid || got.Produced != 4096 || len(got.Gaps) == 0 {
		t.Fatalf("status = %+v, want id, produced offset and cap gap", got)
	}
	if got.Runs != nil || got.Bytes != nil {
		t.Fatalf("status exposed raw recording data: %s", result)
	}
}

func TestSessionRecoveryStatusReturnsAnEmptyGapList(t *testing.T) {
	db := openRecoveryStatusRecordingStore(t, 4096)
	term := newFeedablePTY()
	ws, stop := newRecordingWSServer(t, term, WithSessionOutputRecorder(db.SessionOutput()))
	defer stop()
	conn := connectWS(t, ws)
	tap := newSocketTap(conn)
	sid := openInPaneTapped(t, ws, conn, tap, reclaimPane, 1).SessionID
	live, _ := liveSessions(t, conn, tap, 2)
	entry := entryFor(t, live, sid)
	result, rpcErr := readRecoveryStatus(t, conn, tap, 3, map[string]any{
		"sessionId": sid, "instanceId": entry.InstanceID, "sessionEpoch": entry.SessionEpoch,
	})
	if rpcErr != nil {
		t.Fatalf("session.recoveryStatus: %+v", rpcErr)
	}
	schema := loadSchema(t, "session.recoveryStatus.schema.json")
	validateJSON(t, schema, result, "empty session.recoveryStatus result")
	var got struct {
		Produced uint64        `json:"produced"`
		Gaps     []content.Gap `json:"gaps"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.Produced != 0 || got.Gaps == nil || len(got.Gaps) != 0 {
		t.Fatalf("empty status = %+v, want produced=0 and gaps=[]", got)
	}
}
