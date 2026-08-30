package transport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE OFFER, OFF THE REAL SOCKET (nocx-zn386).
//
// The preview names the variables the export marked `type: secret` and does
// not carry their values, which is what lets the ask offer without the
// renderer ever holding a credential. The import then takes the person's
// answer as one boolean.

//nolint:gosec // a synthetic export whose whole point is that it carries a credential-shaped value
const secretsExport = `{
  "info": {"name": "acme", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
  "variable": [
    {"key": "baseUrl", "value": "https://api.acme.test", "type": "default"},
    {"key": "apiToken", "value": "sk-secret-value", "type": "secret"}
  ],
  "item": [{"name": "ping", "request": {"method": "GET", "url": "{{baseUrl}}/ping"}}]
}`

func TestAPIImportPostman_PreviewNamesSecretVariablesAndCarriesNoValue(t *testing.T) {
	_, conn := newAPIWSServer(t)
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"document": secretsExport,
		"dest":     filepath.Join(t.TempDir(), "acme"),
		"preview":  true,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.postman preview: %+v", resp.Error)
	}
	validateJSON(t, loadSchema(t, "api.import.postman.schema.json"), resp.Result, "preview result")

	// THE VALUE IS NOT ON THIS SIDE. Asserted on the raw result rather than
	// on a decoded field: a value could only leak through a field nobody
	// thought to decode, which is exactly what a struct-shaped assertion
	// cannot see (the defect contracts/ exists for).
	if strings.Contains(string(resp.Result), "sk-secret-value") {
		t.Fatalf("the preview carried the value: %s", resp.Result)
	}

	var result struct {
		Documents []struct {
			Kind    string   `json:"kind"`
			Name    string   `json:"name"`
			Secrets []string `json:"secrets"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode preview result: %v", err)
	}
	if len(result.Documents) != 1 {
		t.Fatalf("documents = %+v, want the one document", result.Documents)
	}
	doc := result.Documents[0]
	if doc.Kind != "collection" || doc.Name != "acme" {
		t.Errorf("document = %+v, want the collection named from inside itself", doc)
	}
	if len(doc.Secrets) != 1 || doc.Secrets[0] != "apiToken" {
		t.Errorf("secrets = %v, want apiToken named", doc.Secrets)
	}
}

// A URL is refused BY NAME rather than answered with nothing: reading one
// means fetching it, and a fetch is a call to somebody's server that nobody
// asked for.
func TestAPIImportPostman_PreviewRefusesAURL(t *testing.T) {
	_, conn := newAPIWSServer(t)
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"url":     "https://example.test/export.json",
		"dest":    filepath.Join(t.TempDir(), "acme"),
		"preview": true,
	}, 1)
	if resp.Error == nil {
		t.Fatal("a URL preview was accepted")
	}
	if resp.Error.Code != -32602 || !strings.Contains(resp.Error.Message, "fetch") {
		t.Fatalf("error = %+v, want a -32602 naming the fetch", resp.Error)
	}
}

// AND THE ANSWER TRAVELS. Without a vault wired the offer cannot be taken,
// so what this asserts of the import is the half that is this side's: the
// parameter is accepted, and the value is not destroyed by asking.
func TestAPIImportPostman_StoreSecretsIsAcceptedAndLosesNothing(t *testing.T) {
	_, conn := newAPIWSServer(t)
	dest := filepath.Join(t.TempDir(), "acme")
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"document":     secretsExport,
		"dest":         dest,
		"storeSecrets": true,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.postman: %+v", resp.Error)
	}
	body, err := os.ReadFile(filepath.Join(dest, "environments", "default.json")) //nolint:gosec // a path this test made under t.TempDir()
	if err != nil {
		t.Fatalf("read the environment: %v", err)
	}
	if !strings.Contains(string(body), "sk-secret-value") {
		t.Fatalf("the value was lost where no vault could take it: %s", body)
	}
}
