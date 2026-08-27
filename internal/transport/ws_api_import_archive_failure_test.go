package transport

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAPIImportPostmanArchive_RefusesMissingAndNonRegularSources(t *testing.T) {
	_, conn := newAPIWSServer(t)
	root := t.TempDir()
	cases := []struct {
		name string
		path string
		code int
	}{
		{name: "missing", path: filepath.Join(root, "missing.zip"), code: -32602},
		{name: "directory", path: root, code: -32602},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(root, tc.name+"-dest")
			resp := vaultCall(t, conn, "api.import.postman", map[string]any{
				"path": tc.path,
				"dest": dest,
			}, 1)
			if resp.Error == nil {
				t.Fatal("archive import succeeded, want source refusal")
			}
			if resp.Error.Code != tc.code {
				t.Fatalf("error code = %d, want %d: %+v", resp.Error.Code, tc.code, resp.Error)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination exists after source refusal: %v", err)
			}
		})
	}
}
