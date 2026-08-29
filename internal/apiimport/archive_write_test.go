package apiimport

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

func TestImportPostmanArchive_WritesNamedDocumentsThatOpen(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "imports")
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":           `{"environment":{"env-1":true},"collection":{"col-1":true,"col-2":true}}`,
		"environment/env-1.json": `{"id":"env-1","name":"Development","values":[{"key":"base","value":"https://example.test","type":"default","enabled":true}]}`,
		"collection/col-1.json":  `{"info":{"name":"Accounts"},"item":[{"name":"List","request":{"method":"GET","url":"https://example.test/accounts"}}]}`,
		"collection/col-2.json":  `{"info":{"name":"Billing"},"item":[{"name":"List","request":{"method":"GET","url":"https://example.test/billing"}}]}`,
	})

	got, err := ImportPostmanArchive(t.Context(), NewOSFS(), dest, bytes.NewReader(archive), apicoll.Route{}, nil)
	if err != nil {
		t.Fatalf("ImportPostmanArchive: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("results = %d, want 3", len(got))
	}

	svc := apicoll.NewCollections(nil)
	for _, name := range []string{"Accounts", "Billing", "Development"} {
		opened, err := svc.Open(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("Open(%s): %v", name, err)
		}
		if opened.Collection.Name != name {
			t.Errorf("Open(%s) collection name = %q", name, opened.Collection.Name)
		}
		if err := svc.Close(opened.Handle); err != nil {
			t.Fatalf("Close(%s): %v", name, err)
		}
	}

	// The path is under t.TempDir and was created by this test.
	if env, err := os.ReadFile(filepath.Join(dest, "Development", "environments", "Development.json")); err != nil { //nolint:gosec // reads a file this test just created under t.TempDir
		t.Fatalf("read imported environment: %v", err)
	} else if !strings.Contains(string(env), `"name": "Development"`) {
		t.Fatalf("imported environment has unexpected contents: %s", env)
	}
}

func TestImportPostmanArchive_RefusesInvalidDestinationNameBeforeWriting(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "imports")
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":          `{"collection":{"col-1":true}}`,
		"collection/col-1.json": `{"info":{"name":"../escape"},"item":[]}`,
	})
	p := newProbeFS()

	if _, err := ImportPostmanArchive(t.Context(), p, dest, bytes.NewReader(archive), apicoll.Route{}, nil); err == nil {
		t.Fatal("ImportPostmanArchive accepted a traversal document name")
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after invalid-name refusal: %v", err)
	}
	if len(p.ops("MkdirAll")) != 0 {
		t.Fatalf("destination was created before invalid-name refusal: %+v", p.ops("MkdirAll"))
	}
}

func TestImportPostmanArchive_RollsBackEarlierDocumentsWhenLaterWriteFails(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "imports")
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":      `{"collection":{"a":true,"b":true}}`,
		"collection/a.json": `{"info":{"name":"Accounts"},"item":[]}`,
		"collection/b.json": `{"info":{"name":"Billing"},"item":[]}`,
	})
	p := newProbeFS()
	writeCount := 0
	p.fail = func(op, _ string, _ int) error {
		if op == "WriteFile" {
			writeCount++
			if writeCount == 2 {
				return errors.New("injected second document failure")
			}
		}
		return nil
	}

	if _, err := ImportPostmanArchive(t.Context(), p, dest, bytes.NewReader(archive), apicoll.Route{}, nil); err == nil {
		t.Fatal("ImportPostmanArchive succeeded despite second-document write failure")
	}
	assertGone(t, dest)
	assertGone(t, filepath.Join(dest, "Accounts"))
	assertNoStaging(t, filepath.Dir(dest))
}

func TestImportPostmanArchive_RefusesDuplicateAndOccupiedTargetsBeforeWriting(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		occupy    string
		wantError string
	}{
		{
			name: "duplicate names",
			files: map[string]string{
				"archive.json":      `{"collection":{"a":true,"b":true}}`,
				"collection/a.json": `{"info":{"name":"Same"},"item":[]}`,
				"collection/b.json": `{"info":{"name":"Same"},"item":[]}`,
			},
			wantError: "duplicate",
		},
		{
			name: "occupied target",
			files: map[string]string{
				"archive.json":      `{"collection":{"a":true}}`,
				"collection/a.json": `{"info":{"name":"Same"},"item":[]}`,
			},
			occupy:    "Same",
			wantError: "already exists",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "imports")
			if tc.occupy != "" {
				if err := os.MkdirAll(filepath.Join(dest, tc.occupy), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			p := newProbeFS()
			_, err := ImportPostmanArchive(t.Context(), p, dest, bytes.NewReader(makePostmanArchive(t, tc.files)), apicoll.Route{}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if len(p.ops("MkdirAll")) != 0 {
				t.Fatalf("write began during preflight: %+v", p.ops("MkdirAll"))
			}
		})
	}
}
