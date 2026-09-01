package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
)

func TestSkillsSettingsMethodsOverWire(t *testing.T) {
	configDir := t.TempDir()
	authored := filepath.Join(configDir, "skills", "deploy")
	if err := os.MkdirAll(authored, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authored, "SKILL.md"), []byte("---\nname: deploy\ndescription: deploy\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := skill.NewStoreWithDocumentStore(skill.OSFileSystem{}, []skill.Root{
		{Dir: filepath.Dir(authored), Provenance: skill.ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: skill.ProvenanceManaged},
	}, storage.NewDocumentStore(configDir))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithSkillSource(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	var list struct {
		Skills []skill.ListedSkill `json:"skills"`
	}
	decodeSkillCall(t, jsonrpcCall(t, conn, "skills.list", map[string]any{}), &list)
	if len(list.Skills) != 1 || list.Skills[0].Name != "deploy" || !list.Skills[0].Enabled {
		t.Fatalf("skills.list = %+v", list.Skills)
	}

	var changed struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	decodeSkillCall(t, jsonrpcCall(t, conn, "skills.setEnabled", map[string]any{"name": "deploy", "enabled": false}), &changed)
	if changed.Name != "deploy" || changed.Enabled {
		t.Fatalf("skills.setEnabled = %+v", changed)
	}
	if got := store.Index(); len(got) != 0 {
		t.Fatalf("disabled skill index = %+v", got)
	}

	var removed struct {
		Name string `json:"name"`
	}
	decodeSkillCall(t, jsonrpcCall(t, conn, "skills.remove", map[string]any{"name": "deploy"}), &removed)
	if removed.Name != "deploy" {
		t.Fatalf("skills.remove = %+v", removed)
	}
	if _, err := os.Stat(filepath.Join(authored, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("skill file still exists, stat error = %v", err)
	}
}

func decodeSkillCall(t *testing.T, raw []byte, result any) {
	t.Helper()
	var env rpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", env.Error)
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}
