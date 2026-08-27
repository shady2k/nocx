package transport

import (
	"encoding/base64"
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

func TestAPIImportPostmanArchiveBytes_RefusesNonZip(t *testing.T) {
	_, conn := newAPIWSServer(t)
	dest := filepath.Join(t.TempDir(), "dest")
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"archiveBytes": base64.StdEncoding.EncodeToString([]byte("not a zip")),
		"dest":         dest,
	}, 1)
	if resp.Error == nil {
		t.Fatal("archiveBytes import succeeded for non-ZIP bytes")
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602: %+v", resp.Error.Code, resp.Error)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after non-ZIP refusal: %v", err)
	}
}

func TestAPIImportPostmanArchiveBytes_RefusesInvalidBase64(t *testing.T) {
	_, conn := newAPIWSServer(t)
	dest := filepath.Join(t.TempDir(), "dest")
	resp := vaultCall(t, conn, "api.import.postman", map[string]any{
		"archiveBytes": "%",
		"dest":         dest,
	}, 1)
	if resp.Error == nil {
		t.Fatal("archiveBytes import succeeded for invalid base64")
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602: %+v", resp.Error.Code, resp.Error)
	}
	if resp.Error.Message != "Invalid params: archiveBytes must be base64" {
		t.Fatalf("error message = %q, want invalid-base64 refusal", resp.Error.Message)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after invalid-base64 refusal: %v", err)
	}
}
