package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestSessionToolSurfaceChanged_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.toolSurfaceChanged.schema.json")
	for name, params := range map[string]toolSurfaceChangedParams{
		"available": {
			SessionID: "session-1", InstanceID: "instance-1", SessionEpoch: 1, Status: "available",
		},
		"unavailable": {
			SessionID: "session-1", InstanceID: "instance-1", SessionEpoch: 1,
			Status: "unavailable", Reason: "tools.catalogue did not arrive before the launch deadline",
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, name+" DTO")
		})
	}
}

func TestToolSurfaceDoesNotExtendIntegrationReasonVocabulary(t *testing.T) {
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	raw, err := json.Marshal(integrationChangedParams{
		SessionID: "session-1", InstanceID: "instance-1", SessionEpoch: 1,
		Status: IntegrationConventional, Reason: "tool-surface-unavailable", Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateJSONErr(schema, raw); err == nil {
		t.Fatal("integration contract accepted a tool-surface reason outside ssh.RefusalReason")
	}
}

func TestSessionToolSurfaceChanged_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.toolSurfaceChanged.schema.json")
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	sid := openLocalSession(t, conn)

	ws.BroadcastToolSurface(sid, "unavailable", "session already has a worker caller")
	raw, err := awaitNotification(conn, "session.toolSurfaceChanged", wantWithin)
	if err != nil {
		t.Fatalf("waiting for tool-surface notification: %v", err)
	}
	validateJSON(t, schema, raw, "session.toolSurfaceChanged wire")
	var got toolSurfaceChangedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SessionID != sid || got.Status != "unavailable" || got.Reason != "session already has a worker caller" {
		t.Fatalf("wire fact = %+v, want session %q and the published refusal", got, sid)
	}
}
