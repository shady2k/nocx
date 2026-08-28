package transport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/transport/control"
)

func TestScopedParamsContractsAgreeWithRegisteredValidators(t *testing.T) {
	server := &WSServer{domainQueueDepth: 1}
	admission := control.NewSemaphore("params-contract", 1)
	specs := server.configSpecs(admission, admission, admission, nil, false)
	registered := make(map[string]methodSpec)
	for _, spec := range specs {
		if isParamsContractScope(spec.method) {
			registered[spec.method] = spec
		}
	}

	entries, entriesErr := os.ReadDir(contractDir)
	if entriesErr != nil {
		t.Fatalf("read contracts dir: %v", entriesErr)
	}
	contracts := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".params.schema.json") || !isParamsContractScope(strings.TrimSuffix(name, ".params.schema.json")) {
			continue
		}
		method := strings.TrimSuffix(name, ".params.schema.json")
		contracts[method] = struct{}{}
		if _, ok := registered[method]; !ok {
			t.Errorf("params contract %s has no registered method", name)
		}
	}

	invalid := [][]byte{
		[]byte(`[]`),
		[]byte(`"scalar"`),
		[]byte(`true`),
		[]byte(`{"unknown":true}`),
		[]byte(`{"id":null,"body":null,"title":null,"query":null,"ids":null}`),
		[]byte(`{"id":123}`),
		[]byte(`{"body":123}`),
		[]byte(`{"title":123}`),
		[]byte(`{"query":123}`),
		[]byte(`{"ids":"not-an-array"}`),
	}
	for _, probe := range []struct {
		field string
		size  int
	}{
		{field: "id", size: 129},
		{field: "body", size: 200001},
		{field: "title", size: 201},
		{field: "query", size: 1001},
	} {
		raw, marshalErr := json.Marshal(map[string]string{probe.field: strings.Repeat("x", probe.size)})
		if marshalErr != nil {
			t.Fatalf("marshal %s probe: %v", probe.field, marshalErr)
		}
		invalid = append(invalid, raw)
	}
	idsProbe, idsErr := json.Marshal(map[string][]string{"ids": {strings.Repeat("x", 129)}})
	if idsErr != nil {
		t.Fatalf("marshal ids probe: %v", idsErr)
	}
	invalid = append(invalid, idsProbe)
	valid := map[string][][]byte{
		"notes.create": {
			[]byte(`{}`),
			[]byte(`{"body":"body"}`),
		},
		"notes.delete": {
			[]byte(`{"id":"note-1"}`),
		},
		"notes.get": {
			[]byte(`{"id":"note-1"}`),
		},
		"notes.list": {
			[]byte(`{}`),
		},
		"notes.search": {
			[]byte(`{}`),
			[]byte(`{"query":"term"}`),
		},
		"notes.update": {
			[]byte(`{"id":"note-1"}`),
			[]byte(`{"id":"note-1","body":"updated"}`),
		},
		"snippets.create": {
			[]byte(`{}`),
			[]byte(`{"title":"title","body":"body"}`),
		},
		"snippets.delete": {
			[]byte(`{"id":"snippet-1"}`),
		},
		"snippets.list": {
			[]byte(`{}`),
		},
		"snippets.reorder": {
			[]byte(`{}`),
			[]byte(`{"ids":[]}`),
		},
		"snippets.update": {
			[]byte(`{"id":"snippet-1"}`),
			[]byte(`{"id":"snippet-1","title":"title","body":"body"}`),
		},
	}
	for method := range registered {
		if _, ok := valid[method]; !ok {
			t.Errorf("registered method %s has no valid params probes", method)
		}
	}
	for method := range valid {
		if _, ok := registered[method]; !ok {
			t.Errorf("valid params probes have no registered method %s", method)
		}
	}

	for method, spec := range registered {
		name := method + ".params.schema.json"
		if _, ok := contracts[method]; !ok {
			t.Errorf("registered method %s has no params contract", method)
			continue
		}
		schema := loadSchema(t, name)
		for _, raw := range invalid {
			raw := raw
			t.Run(method+"/"+string(raw), func(t *testing.T) {
				if err := validateJSONErr(schema, raw); err == nil {
					t.Fatalf("schema accepted invalid probe %s", raw)
				}
				if msg := spec.validate(raw); msg == "" {
					t.Fatalf("registered validator accepted schema-rejected probe %s", raw)
				}
			})
		}
		for _, raw := range valid[method] {
			raw := raw
			t.Run(method+"/accept/"+string(raw), func(t *testing.T) {
				if err := validateJSONErr(schema, raw); err != nil {
					t.Fatalf("schema rejected valid probe %s: %v", raw, err)
				}
				if msg := spec.validate(raw); msg != "" {
					t.Fatalf("registered validator rejected schema-accepted probe %s: %s", raw, msg)
				}
			})
		}
	}

	for method := range registered {
		if _, ok := contracts[method]; !ok {
			continue
		}
		path := filepath.Join(contractDir, method+".params.schema.json")
		raw, readErr := os.ReadFile(path) //nolint:gosec // path is registration-derived under contracts/
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		var metadata struct {
			AdditionalProperties *bool    `json:"additionalProperties"`
			Required             []string `json:"required"`
		}
		if parseErr := json.Unmarshal(raw, &metadata); parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		if metadata.AdditionalProperties == nil || *metadata.AdditionalProperties {
			t.Errorf("%s must set additionalProperties to false", path)
		}
		if metadata.Required == nil {
			t.Errorf("%s must declare required, including when it is empty", path)
		}
	}
}

func isParamsContractScope(method string) bool {
	return strings.HasPrefix(method, "notes.") || strings.HasPrefix(method, "snippets.")
}
