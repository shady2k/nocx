package assistant

import (
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
)

func TestCreateAndUpdateSchemasRejectAmbiguousIDShapes(t *testing.T) {
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	cases := []struct {
		name     string
		toolName string
		good     map[string]any
		bad      map[string]any
	}{
		{"notes.create with id", "notes.create", map[string]any{"body": "body"}, map[string]any{"id": "note-a", "body": "body"}},
		{"notes.update without id", "notes.update", map[string]any{"id": "note-a", "body": "body"}, map[string]any{"body": "body"}},
		{"snippets.create with id", "snippets.create", map[string]any{"title": "title", "body": "body"}, map[string]any{"id": "snippet-a", "title": "title", "body": "body"}},
		{"snippets.update without id", "snippets.update", map[string]any{"id": "snippet-a", "title": "title", "body": "body"}, map[string]any{"title": "title", "body": "body"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := tc.toolName
			tool, ok := reg.Lookup(toolName)
			if !ok {
				t.Fatalf("tool %q is not registered", toolName)
			}
			schema, err := compileToolSchema(tool)
			if err != nil {
				t.Fatalf("compileToolSchema: %v", err)
			}
			if err := schema.Validate(tc.good); err != nil {
				t.Fatalf("%s rejected valid arguments: %v", toolName, err)
			}
			if err := schema.Validate(tc.bad); err == nil {
				t.Fatalf("%s accepted ambiguous arguments", toolName)
			}
		})
	}
}
