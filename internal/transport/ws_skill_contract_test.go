package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
)

func TestSkillsList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.list.schema.json")
	raw, err := json.Marshal(skill.ListResult{Skills: []skill.ListedSkill{{Name: "deploy", Description: "d", Provenance: skill.ProvenanceAuthored, Path: "/skills/deploy/SKILL.md", Enabled: true, Status: skill.StatusApproved}}, DocumentPath: "/skills.json"})
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

func TestSkillsApprove_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.approve.schema.json")
	validateJSON(t, schema, mustMarshal(map[string]string{"name": "deploy", "status": "approved"}), "skills.approve DTO")
}

func TestSkillsPreview_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.preview.schema.json")
	for _, tc := range []struct {
		name   string
		result skill.PreviewResult
	}{
		{
			name: "with findings",
			result: skill.PreviewResult{
				Name: "deploy", Description: "Deploy the service", Body: "cat ~/.env\n",
				URL:      "https://example.com/SKILL.md",
				Findings: []skill.Finding{{PatternID: "read_secrets", Line: "cat ~/.env", LineNumber: 1}},
			},
		},
		{
			// The empty case is the one a hand-built fixture gets wrong: a
			// nil slice marshals as null, and the renderer's first .map on
			// it throws.
			name: "no findings",
			result: skill.PreviewResult{
				Name: "deploy", Description: "Deploy the service", Body: "body\n",
				URL: "https://example.com/SKILL.md", Findings: []skill.Finding{},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatal(err)
			}
			validateJSON(t, schema, raw, "skills.preview DTO")
		})
	}
}

// The real result, off the real socket: a fake local endpoint serves a
// SKILL.md and the shipped handler answers with what a person would read.
func TestSkillsPreview_OverTheWireConformsToContract(t *testing.T) {
	document := "---\nname: deploy\ndescription: Deploy the service\n---\n" +
		"Run the deploy script.\ncat ~/.env\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(document))
	}))
	defer srv.Close()

	configDir := t.TempDir()
	conn, cleanup := skillsPreviewConnection(t, configDir)
	defer cleanup()

	resp := jsonrpcCall(t, conn, "skills.preview", map[string]any{"url": srv.URL + "/anything/SKILL.md"})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "skills.preview.schema.json"), env.Result, "skills.preview wire")

	var got skill.PreviewResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "deploy" || got.Description != "Deploy the service" {
		t.Errorf("preview = %+v", got)
	}
	if !strings.Contains(got.Body, "Run the deploy script.") {
		t.Errorf("body = %q, want the whole body", got.Body)
	}
	if len(got.Findings) != 1 || got.Findings[0].PatternID != "read_secrets" {
		t.Errorf("findings = %+v", got.Findings)
	}
	// Nothing was written: preview is the half that lets a person read
	// before deciding, so the library is untouched by it.
	entries, err := os.ReadDir(filepath.Join(configDir, "installed-skills"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("skills.preview wrote %d entries into the installed root", len(entries))
	}
}

// A refusal reaches the person as the backend's own sentence, naming the
// step that refused, rather than a transport sentence about an internal
// error.
func TestSkillsPreview_RefusalTravelsAsItsOwnSentence(t *testing.T) {
	conn, cleanup := skillsPreviewConnection(t, t.TempDir())
	defer cleanup()
	resp := jsonrpcCall(t, conn, "skills.preview", map[string]any{"url": "not a url"})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatal("want a refusal")
	}
	if !strings.Contains(env.Error.Message, "address") {
		t.Errorf("refusal = %q", env.Error.Message)
	}
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

func TestSkillsApprove_OverTheWireConformsToContract(t *testing.T) {
	configDir := t.TempDir()
	managedRoot := filepath.Join(configDir, "managed-skills")
	store := skill.NewStore(skill.OSFileSystem{}, []skill.Root{{Dir: managedRoot, Provenance: skill.ProvenanceManaged}}, storage.NewDocumentStore(configDir))
	if err := store.Create("deploy", "deploy", "body"); err != nil {
		t.Fatalf("create managed skill: %v", err)
	}
	path := filepath.Join(managedRoot, "deploy", "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: deploy\ndescription: deploy\n---\nchanged\n"), 0o600); err != nil {
		t.Fatalf("change managed skill: %v", err)
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithSkillSource(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "skills.approve", map[string]any{"name": "deploy"})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "skills.approve.schema.json"), env.Result, "skills.approve wire")
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
	store := skill.NewStore(skill.OSFileSystem{}, []skill.Root{{Dir: filepath.Dir(skillDir), Provenance: skill.ProvenanceAuthored}, {Dir: filepath.Join(configDir, "managed-skills"), Provenance: skill.ProvenanceManaged}}, storage.NewDocumentStore(configDir))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithSkillSource(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	conn := connectWS(t, ws)
	return conn, func() { _ = conn.Close(); _ = ws.Stop(ctx) }
}

// skillsPreviewConnection is the shipped store over the four roots with the
// real fetch seam wired — apifetch over httppolicy's direct route, which is
// what lets these tests drive a loopback endpoint through the same transport
// the product uses.
func skillsPreviewConnection(t *testing.T, configDir string) (*websocket.Conn, func()) {
	t.Helper()
	routes := func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, fmt.Errorf("unexpected route %q", routeID)
		}
		return httppolicy.Local(), nil
	}
	store := skill.NewStore(skill.OSFileSystem{}, []skill.Root{
		{Dir: filepath.Join(configDir, "skills"), Provenance: skill.ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: skill.ProvenanceManaged},
		{Dir: filepath.Join(configDir, "installed-skills"), Provenance: skill.ProvenanceInstalled},
	}, storage.NewDocumentStore(configDir), skill.WithFetcher(apifetch.New(routes, nil)))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithSkillSource(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, ws)
	return conn, func() { _ = conn.Close(); _ = ws.Stop(ctx) }
}
