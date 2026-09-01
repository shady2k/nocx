package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
)

func TestSkillsList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.list.schema.json")
	raw, err := json.Marshal(skill.ListResult{Skills: []skill.ListedSkill{{Name: "deploy", Description: "d", Provenance: skill.ProvenanceAuthored, Path: "/skills/deploy/SKILL.md", Enabled: true}}, DocumentPath: "/skills.json"})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "skills.list DTO")
}

func TestSkillsSetEnabled_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.setEnabled.schema.json")
	validateJSON(t, schema, mustMarshal(map[string]any{"name": "deploy", "enabled": false}), "skills.setEnabled DTO")
}

func TestSkillsRemove_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.remove.schema.json")
	validateJSON(t, schema, mustMarshal(map[string]string{"name": "deploy"}), "skills.remove DTO")
}

func TestSkillsList_OverTheWireConformsToContract(t *testing.T) {
	conn, cleanup := skillsContractConnection(t)
	defer cleanup()
	resp := jsonrpcCall(t, conn, "skills.list", map[string]any{})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "skills.list.schema.json"), env.Result, "skills.list wire")
}

func TestSkillsSetEnabled_OverTheWireConformsToContract(t *testing.T) {
	conn, cleanup := skillsContractConnection(t)
	defer cleanup()
	resp := jsonrpcCall(t, conn, "skills.setEnabled", map[string]any{"name": "deploy", "enabled": false})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "skills.setEnabled.schema.json"), env.Result, "skills.setEnabled wire")
}

func TestSkillsRemove_OverTheWireConformsToContract(t *testing.T) {
	conn, cleanup := skillsContractConnection(t)
	defer cleanup()
	resp := jsonrpcCall(t, conn, "skills.remove", map[string]any{"name": "deploy"})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "skills.remove.schema.json"), env.Result, "skills.remove wire")
}

func skillsContractConnection(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	configDir := t.TempDir()
	skillDir := filepath.Join(configDir, "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: deploy\ndescription: deploy\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := skill.NewStoreWithDocumentStore(skill.OSFileSystem{}, []skill.Root{{Dir: filepath.Dir(skillDir), Provenance: skill.ProvenanceAuthored}, {Dir: filepath.Join(configDir, "managed-skills"), Provenance: skill.ProvenanceManaged}}, storage.NewDocumentStore(configDir))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithSkillSource(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	conn := connectWS(t, ws)
	return conn, func() { _ = conn.Close(); _ = ws.Stop(ctx) }
}
