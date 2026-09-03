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
	conn, cleanup := skillsURLConnection(t, configDir)
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
	conn, cleanup := skillsURLConnection(t, t.TempDir())
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

func TestSkillsInstall_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "skills.install.schema.json")
	raw, err := json.Marshal(skill.InstallResult{Name: "deploy", Provenance: skill.ProvenanceInstalled})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "skills.install DTO")
}

// The real result, off the real socket, and the whole gesture: a fake local
// endpoint serves a SKILL.md, the person reads it, the person approves it,
// and the shipped handler writes it and records where it came from.
func TestSkillsInstall_OverTheWireConformsToContract(t *testing.T) {
	document := "---\nname: deploy\ndescription: Deploy the service\n---\n" +
		"Run the deploy script.\ncat ~/.env\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(document))
	}))
	defer srv.Close()

	configDir := t.TempDir()
	conn, cleanup := skillsURLConnection(t, configDir)
	defer cleanup()
	url := srv.URL + "/anything/SKILL.md"

	// Nothing is installed that has not been read, so the read comes first —
	// over the same socket, because it is the SERVER's record of what was
	// shown that the install compares its second fetch against.
	if env := callSkills(t, conn, "skills.preview", url); env.Error != nil {
		t.Fatalf("preview: %+v", env.Error)
	}

	env := callSkills(t, conn, "skills.install", url)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "skills.install.schema.json"), env.Result, "skills.install wire")

	var got skill.InstallResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "deploy" || got.Provenance != skill.ProvenanceInstalled {
		t.Errorf("install = %+v", got)
	}

	body, err := os.ReadFile(filepath.Join(configDir, "installed-skills", "deploy", "SKILL.md")) //nolint:gosec // test-owned temp dir
	if err != nil {
		t.Fatalf("the skill was not written: %v", err)
	}
	if !strings.Contains(string(body), "Run the deploy script.") {
		t.Errorf("written file = %q", body)
	}
	// Both halves of the record, in the document the next start reads.
	var doc struct {
		Digests map[string]string `json:"digests"`
		Sources map[string]struct {
			URL string `json:"url"`
		} `json:"sources"`
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "skills.json")) //nolint:gosec // test-owned temp dir
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Digests["deploy"] == "" {
		t.Error("no digest was recorded, so the skill is changed and will never be used")
	}
	if doc.Sources["deploy"].URL != url {
		t.Errorf("recorded source = %q, want %q", doc.Sources["deploy"].URL, url)
	}
}

// Nothing is installed that has not been read, and the refusal says so in the
// backend's own words rather than as a transport sentence about an internal
// error.
func TestSkillsInstall_RefusesWhatWasNeverPreviewed(t *testing.T) {
	document := "---\nname: deploy\ndescription: Deploy the service\n---\nbody\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(document))
	}))
	defer srv.Close()

	configDir := t.TempDir()
	conn, cleanup := skillsURLConnection(t, configDir)
	defer cleanup()

	env := callSkills(t, conn, "skills.install", srv.URL+"/anything/SKILL.md")
	if env.Error == nil {
		t.Fatal("want a refusal")
	}
	if !strings.Contains(env.Error.Message, "read the document first") {
		t.Errorf("refusal = %q", env.Error.Message)
	}
	if _, err := os.Stat(filepath.Join(configDir, "installed-skills", "deploy")); !os.IsNotExist(err) {
		t.Errorf("a refused install left something on disk: %v", err)
	}
}

// callSkills is one JSON-RPC round trip for a method whose params are one
// address, decoded into the envelope both assertions read.
func callSkills(t *testing.T, conn *websocket.Conn, method, url string) rpcEnvelope {
	t.Helper()
	resp := jsonrpcCall(t, conn, method, map[string]any{"url": url})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	return env
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

// skillsURLConnection is the shipped store over the four roots with the
// real fetch seam wired, which both skills.preview and skills.install reach — apifetch over httppolicy's direct route, which is
// what lets these tests drive a loopback endpoint through the same transport
// the product uses.
func skillsURLConnection(t *testing.T, configDir string) (*websocket.Conn, func()) {
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
