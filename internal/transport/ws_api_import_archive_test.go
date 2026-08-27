package transport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestAPIImportPostmanArchive_ImportsNamedCollectionsAndEnvironments(t *testing.T) {
	_, conn := newAPIWSServer(t)
	archivePath := filepath.Join(t.TempDir(), "workspace.zip")
	archive := makePostmanArchiveForTransport(t, map[string]string{
		"archive.json":           `{"environment":{"env-1":true},"collection":{"col-1":true,"col-2":true}}`,
		"environment/env-1.json": `{"id":"env-1","name":"Development","values":[]}`,
		"collection/col-1.json":  `{"info":{"name":"Accounts"},"item":[{"name":"List","request":{"method":"GET","url":"https://api.test/accounts"}}]}`,
		"collection/col-2.json":  `{"info":{"name":"Billing"},"item":[]}`,
	})
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "imported")
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"path": archivePath,
		"dest": dest,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.postman archive: %+v", resp.Error)
	}
	validateJSON(t, loadSchema(t, "api.import.postman.schema.json"), resp.Result, "archive result")

	var result struct {
		Documents []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode archive result: %v", err)
	}
	if len(result.Documents) != 3 {
		t.Fatalf("documents = %d, want 3", len(result.Documents))
	}
	want := map[string]string{
		"collection:Accounts":     filepath.Join(dest, "Accounts", "nocx-collection.json"),
		"collection:Billing":      filepath.Join(dest, "Billing", "nocx-collection.json"),
		"environment:Development": filepath.Join(dest, "Development", "environments", "Development.json"),
	}
	for _, doc := range result.Documents {
		delete(want, doc.Kind+":"+doc.Name)
	}
	if len(want) != 0 {
		t.Errorf("result documents missing %v", want)
	}
	for _, file := range []string{
		filepath.Join(dest, "Accounts", "nocx-collection.json"),
		filepath.Join(dest, "Billing", "nocx-collection.json"),
		filepath.Join(dest, "Development", "environments", "Development.json"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("expected imported file %s: %v", file, err)
		}
	}
}

func makePostmanArchiveForTransport(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, contents := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create ZIP member %q: %v", name, err)
		}
		if _, err := io.WriteString(w, contents); err != nil {
			t.Fatalf("write ZIP member %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	return buf.Bytes()
}
