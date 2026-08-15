package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// contractDir holds the wire schemas; from internal/app the repo root is two
// levels up. The loader mirrors the one in
// internal/transport/ws_contract_test.go, which this package cannot import.
const contractDir = "../../contracts"

func loadAppContractSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	path := filepath.Join(contractDir, name)
	f, openErr := os.Open(path) //nolint:gosec // test-only path under contracts/
	if openErr != nil {
		t.Fatalf("open %s: %v", path, openErr)
	}
	defer func() { _ = f.Close() }()
	doc, parseErr := jsonschema.UnmarshalJSON(f)
	if parseErr != nil {
		t.Fatalf("parse %s: %v", path, parseErr)
	}
	if addErr := c.AddResource("https://nocx.local/contracts/"+name, doc); addErr != nil {
		t.Fatalf("add %s: %v", name, addErr)
	}
	s, err := c.Compile("https://nocx.local/contracts/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

func validateAppContract(t *testing.T, s *jsonschema.Schema, raw []byte, what string) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: unmarshal: %v", what, err)
	}
	if err := s.Validate(doc); err != nil {
		t.Errorf("%s does not satisfy its contract:\n%v\n\npayload was:\n%s", what, err, raw)
	}
}

// TestSettingsDescribe_OverTheWireConformsToContract drives the REAL
// settings.describe method through the REAL composition root over a real
// WebSocket and validates the raw result against the contract schema — the
// check a DTO test cannot make: the handler actually sending what the DTO
// could have (contracts/README.md; nocx-dgsp addendum 2).
func TestSettingsDescribe_OverTheWireConformsToContract(t *testing.T) {
	schema := loadAppContractSchema(t, "settings.describe.schema.json")

	storagetest.Isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, newErr := newTestApp(t)
	if newErr != nil {
		t.Fatalf("New: %v", newErr)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	resp := callAppWS(t, conn, "settings.describe", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.describe: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	validateAppContract(t, schema, resp.Result, "settings.describe result")

	// The catalogue is present and complete on the real wire: the rail's
	// component pages name these ids, so their absence would be a silent
	// top-level page — the exact defect the schema's required lists exist
	// to stop.
	var result struct {
		Groups []struct {
			ID string `json:"id"`
		} `json:"groups"`
		SectionGroups map[string]string `json:"sectionGroups"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode settings.describe result: %v", err)
	}
	ids := make(map[string]bool, len(result.Groups))
	for _, g := range result.Groups {
		ids[g.ID] = true
	}
	for _, want := range []string{"assistant", "vault", "application", "developer"} {
		if !ids[want] {
			t.Errorf("group %q missing from the real wire catalogue", want)
		}
	}
	if got := result.SectionGroups["History"]; got != "application" {
		t.Errorf("real wire sectionGroups[History] = %q, want application", got)
	}
}
