package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/hashline"
)

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestExecuteFilesEditChangesTheScopedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	decl, ok := reg.Lookup("files.edit")
	if !ok {
		t.Fatal("files.edit not registered")
	}
	readDecl, ok := reg.Lookup("files.read")
	if !ok {
		t.Fatal("files.read not registered")
	}
	capRead, narrowErr := readDecl.Narrow(grant, []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: path}}, agenttools.RunContext{})
	if narrowErr != nil {
		t.Fatalf("Narrow read: %v", narrowErr)
	}
	readOut, readErr := executeFilesRead(toolTestContext(), capRead, json.RawMessage(`{"path":"`+path+`"}`), toolSeams{})
	if readErr != nil {
		t.Fatalf("executeFilesRead: %v", readErr)
	}
	var readResult struct {
		Revision string `json:"revision"`
	}
	if decodeErr := json.Unmarshal([]byte(readOut), &readResult); decodeErr != nil {
		t.Fatalf("decode read result: %v", decodeErr)
	}
	capEdit, editNarrowErr := decl.Narrow(grant, []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: path}}, agenttools.RunContext{})
	if editNarrowErr != nil {
		t.Fatalf("Narrow edit: %v", editNarrowErr)
	}
	args, marshalErr := json.Marshal(map[string]string{"path": path, "revision": readResult.Revision, "patch": "PUT 1.=1:\n+after"})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	out, execErr := executeFilesEdit(toolTestContext(), capEdit, args, toolSeams{})
	if execErr != nil {
		t.Fatalf("executeFilesEdit: %v", execErr)
	}
	var result struct {
		Path     string `json:"path"`
		Revision string `json:"revision"`
	}
	if decodeErr := json.Unmarshal([]byte(out), &result); decodeErr != nil {
		t.Fatalf("decode edit result: %v", decodeErr)
	}
	if result.Path != path || result.Revision == "" {
		t.Fatalf("edit result = %+v", result)
	}
	// #nosec G304 -- path is created under t.TempDir.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "after\n" {
		t.Fatalf("file = %q, want after", got)
	}
}

func TestExecuteFilesCreateCreatesScopedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	decl, ok := reg.Lookup("files.create")
	if !ok {
		t.Fatal("files.create not registered")
	}
	refs, err := decl.ResolveResources(map[string]any{"path": path}, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	capability, err := decl.Narrow(grant, refs, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("Narrow: %v", err)
	}
	args, err := json.Marshal(map[string]string{"path": path, "content": "created\n"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeFilesCreate(toolTestContext(), capability, args, toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesCreate: %v", err)
	}
	var result struct {
		Path     string `json:"path"`
		Status   string `json:"status"`
		Revision string `json:"revision"`
	}
	if decodeErr := json.Unmarshal([]byte(out), &result); decodeErr != nil {
		t.Fatalf("decode create result: %v", decodeErr)
	}
	if result.Path != path || result.Status != "created" || result.Revision == "" {
		t.Fatalf("create result = %+v", result)
	}
	// #nosec G304 -- path is created under t.TempDir.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "created\n" {
		t.Fatalf("file = %q, want created", got)
	}

	escapeTarget := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-escape.txt")
	defer func() { _ = os.Remove(escapeTarget) }()
	writeFile(t, escapeTarget, "outside\n")
	escapePath := filepath.Join(dir, "..", filepath.Base(dir)+"-escape-new.txt")
	defer func() { _ = os.Remove(filepath.Clean(escapePath)) }()

	escapeExistingPath := filepath.Join(dir, "..", filepath.Base(dir)+"-escape.txt")
	readDecl, ok := reg.Lookup("files.read")
	if !ok {
		t.Fatal("files.read not registered")
	}
	readRefs, err := readDecl.ResolveResources(map[string]any{"path": escapeExistingPath}, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("ResolveResources(read escape): %v", err)
	}
	readCapability, err := readDecl.Narrow(grant, readRefs, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("Narrow(read escape): %v", err)
	}
	if _, readErr := executeFilesRead(toolTestContext(), readCapability, mustJSON(t, map[string]string{"path": escapeExistingPath}), toolSeams{}); readErr == nil {
		t.Fatal("files.read escaped the grant")
	}

	editDecl, ok := reg.Lookup("files.edit")
	if !ok {
		t.Fatal("files.edit not registered")
	}
	snapshot, err := hashline.Read(escapeTarget, testResultMaxBytes())
	if err != nil {
		t.Fatalf("hashline.Read(escape): %v", err)
	}
	editRefs, err := editDecl.ResolveResources(map[string]any{"path": escapeExistingPath}, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("ResolveResources(edit escape): %v", err)
	}
	editCapability, err := editDecl.Narrow(grant, editRefs, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("Narrow(edit escape): %v", err)
	}
	editOut, err := executeFilesEdit(toolTestContext(), editCapability, mustJSON(t, map[string]string{
		"path": escapeExistingPath, "revision": snapshot.Revision, "patch": "PUT 1.=1:\n+changed",
	}), toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesEdit(escape): %v", err)
	}
	var editResult struct {
		Status string `json:"status"`
	}
	if decodeErr := json.Unmarshal([]byte(editOut), &editResult); decodeErr != nil {
		t.Fatalf("decode edit escape result: %v", decodeErr)
	}
	if editResult.Status != "refused" {
		t.Fatalf("edit escape result = %s, want refused", editResult.Status)
	}
	// #nosec G304 -- path is created under t.TempDir's parent and cleaned up below.
	outside, err := os.ReadFile(escapeTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(outside) != "outside\n" {
		t.Fatalf("escaped file = %q, want unchanged", outside)
	}
	escapeRefs, err := decl.ResolveResources(map[string]any{"path": escapePath}, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("ResolveResources(escape): %v", err)
	}
	escapeCapability, err := decl.Narrow(grant, escapeRefs, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("Narrow(escape): %v", err)
	}
	escapeArgs, err := json.Marshal(map[string]string{"path": escapePath, "content": "escaped\n"})
	if err != nil {
		t.Fatal(err)
	}
	escapeOut, err := executeFilesCreate(toolTestContext(), escapeCapability, escapeArgs, toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesCreate(escape): %v", err)
	}
	var escapeResult struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(escapeOut), &escapeResult); err != nil {
		t.Fatalf("decode escape result: %v", err)
	}
	if escapeResult.Status != "refused" {
		t.Fatalf("escape result = %s, want refused", escapeResult.Status)
	}
	if _, err := os.Stat(filepath.Clean(escapePath)); !os.IsNotExist(err) {
		t.Fatalf("escaped file stat error = %v, want nonexistent", err)
	}
}
