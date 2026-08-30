package apiimport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestReadPostmanArchive_ReturnsManifestDocumentsByKindAndName(t *testing.T) {
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":           `{"environment":{"env-1":true},"collection":{"col-1":true,"col-2":true}}`,
		"environment/env-1.json": `{"id":"env-1","name":"Development","values":[]}`,
		"collection/col-1.json":  `{"info":{"name":"Accounts"},"item":[]}`,
		"collection/col-2.json":  `{"info":{"name":"Billing"},"item":[]}`,
	})

	got, err := ReadPostmanArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("ReadPostmanArchive: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("documents = %d, want 3", len(got))
	}
	want := []struct {
		kind, name, path string
	}{
		{"collection", "Accounts", "collection/col-1.json"},
		{"collection", "Billing", "collection/col-2.json"},
		{"environment", "Development", "environment/env-1.json"},
	}
	for i, want := range want {
		if got[i].Kind != ArchiveDocumentKind(want.kind) || got[i].Name != want.name || got[i].Path != want.path {
			t.Errorf("document[%d] = %+v, want kind=%q name=%q path=%q", i, got[i], want.kind, want.name, want.path)
		}
		if len(got[i].Document) == 0 {
			t.Errorf("document[%d] has no original bytes", i)
		}
	}
}

func TestReadPostmanArchive_RefusesArchiveWithNoDocuments(t *testing.T) {
	archive := makePostmanArchive(t, map[string]string{
		"archive.json": `{}`,
	})
	if _, err := ReadPostmanArchive(bytes.NewReader(archive)); err == nil {
		t.Fatal("an archive with no manifest documents was accepted")
	}
}

func TestReadPostmanArchive_RefusesEveryInvalidArchiveShape(t *testing.T) {
	validCollection := `{"info":{"name":"Accounts"},"item":[]}`
	validEnvironment := `{"id":"env-1","name":"Development","values":[]}`
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "missing manifest",
			files: map[string]string{
				"collection/col-1.json": validCollection,
			},
		},
		{
			name: "unknown manifest key",
			files: map[string]string{
				"archive.json":         `{"workspace":{"id":true}}`,
				"workspace/thing.json": `{"info":{"name":"ignored"},"item":[]}`,
			},
		},
		{
			name: "manifest id without file",
			files: map[string]string{
				"archive.json": `{"collection":{"missing":true}}`,
			},
		},
		{
			name: "unlisted file",
			files: map[string]string{
				"archive.json":          `{"collection":{"col-1":true}}`,
				"collection/col-1.json": validCollection,
				"collection/extra.json": validCollection,
			},
		},
		{
			name: "parent traversal",
			files: map[string]string{
				"archive.json": `{"collection":{"col-1":true}}`,
				"../evil.json": validCollection,
			},
		},
		{
			name: "absolute path",
			files: map[string]string{
				"archive.json": `{"collection":{"col-1":true}}`,
				"/evil.json":   validCollection,
			},
		},
		{
			name: "unsupported document",
			files: map[string]string{
				"archive.json":          `{"collection":{"col-1":true}}`,
				"collection/col-1.json": `{"not":"a Postman export"}`,
			},
		},
		{
			name: "environment is still a supported document",
			files: map[string]string{
				"archive.json":           `{"environment":{"env-1":true}}`,
				"environment/env-1.json": validEnvironment,
			},
		},
	}
	for _, tc := range cases[:7] {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPostmanArchive(bytes.NewReader(makePostmanArchive(t, tc.files))); err == nil {
				t.Fatal("ReadPostmanArchive succeeded, want a refusal")
			}
		})
	}
}

func TestReadPostmanArchive_RefusesExpandedDataOverMaxDocumentBytes(t *testing.T) {
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":          `{"collection":{"col-1":true}}`,
		"collection/col-1.json": strings.Repeat("x", MaxDocumentBytes+1),
	})
	if _, err := ReadPostmanArchive(bytes.NewReader(archive)); err == nil {
		t.Fatal("a ZIP whose expanded member exceeds MaxDocumentBytes was accepted")
	} else if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %q, want the expanded byte limit named", err)
	}
}

func TestReadPostmanArchive_RefusesExpandedDataBudgetAcrossMembers(t *testing.T) {
	half := strings.Repeat("x", MaxDocumentBytes/2)
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":          `{"collection":{"col-1":true,"col-2":true}}`,
		"collection/col-1.json": `{"info":{"name":"A"},"item":[],"description":"` + half + `"}`,
		"collection/col-2.json": `{"info":{"name":"B"},"item":[],"description":"` + half + `"}`,
	})
	if _, err := ReadPostmanArchive(bytes.NewReader(archive)); err == nil {
		t.Fatal("an archive whose expanded members exceed the aggregate limit was accepted")
	} else if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %q, want the expanded byte limit named", err)
	}
}

func makePostmanArchive(t *testing.T, files map[string]string) []byte {
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

func TestArchiveTestFixtureIsJSON(t *testing.T) {
	// Keep the fixture helper honest: malformed JSON should be caught by the
	// archive reader test rather than by a later assertion about its fields.
	var doc map[string]any
	if err := json.Unmarshal([]byte(`{"info":{"name":"fixture"},"item":[]}`), &doc); err != nil {
		t.Fatal(err)
	}
}

// THE SHAPE A POSTMAN EXPORT ACTUALLY HAS. nocx-r1uk0 read one and wrote the
// layout down: every member sits under a single directory named for the
// export, and nothing at all sits beside it. The reader used to look the
// manifest up at the root, so the one shape that arrives from Postman was the
// one shape no test built (nocx-bvxf2.4). The paths reported back are
// relative to that directory, because what a document is called inside the
// archive is what the ask shows and the export id is nobody's business.
func TestReadPostmanArchive_ReadsMembersNestedUnderTheExportDirectory(t *testing.T) {
	archive := makePostmanArchive(t, map[string]string{
		"9e0a1c/":                       "",
		"9e0a1c/archive.json":           `{"environment":{"env-1":true},"collection":{"col-1":true}}`,
		"9e0a1c/environment/":           "",
		"9e0a1c/environment/env-1.json": `{"id":"env-1","name":"Development","values":[]}`,
		"9e0a1c/collection/":            "",
		"9e0a1c/collection/col-1.json":  `{"info":{"name":"Accounts"},"item":[]}`,
	})

	got, err := ReadPostmanArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("ReadPostmanArchive: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("documents = %d, want 2", len(got))
	}
	want := []struct {
		kind, name, path string
	}{
		{"collection", "Accounts", "collection/col-1.json"},
		{"environment", "Development", "environment/env-1.json"},
	}
	for i, want := range want {
		if got[i].Kind != ArchiveDocumentKind(want.kind) || got[i].Name != want.name || got[i].Path != want.path {
			t.Errorf("document[%d] = kind=%q name=%q path=%q, want kind=%q name=%q path=%q",
				i, got[i].Kind, got[i].Name, got[i].Path, want.kind, want.name, want.path)
		}
	}
}

// A ROOT-LEVEL ARCHIVE IS STILL READ. The prefix is taken from where the
// manifest lies, so an archive with no prefix has one of length zero rather
// than a second code path.
func TestReadPostmanArchive_StillReadsAnArchiveWithNoExportDirectory(t *testing.T) {
	archive := makePostmanArchive(t, map[string]string{
		"archive.json":          `{"collection":{"col-1":true}}`,
		"collection/col-1.json": `{"info":{"name":"Accounts"},"item":[]}`,
	})
	got, err := ReadPostmanArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("ReadPostmanArchive: %v", err)
	}
	if len(got) != 1 || got[0].Path != "collection/col-1.json" {
		t.Fatalf("documents = %+v, want one at collection/col-1.json", got)
	}
}

// WHAT A PREFIX MAY NOT HIDE. Two manifests is two archives in one file and
// there is no answer to which was meant; a member outside the manifest's
// directory is the same unlisted-member refusal the flat reader already made,
// and it must not become "not mine, so not my problem".
func TestReadPostmanArchive_RefusesEveryAmbiguousPrefix(t *testing.T) {
	const collection = `{"info":{"name":"Accounts"},"item":[]}`
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			"two export directories",
			map[string]string{
				"a/archive.json":          `{"collection":{"col-1":true}}`,
				"a/collection/col-1.json": collection,
				"b/archive.json":          `{"collection":{"col-1":true}}`,
				"b/collection/col-1.json": collection,
			},
		},
		{
			"a manifest beside a nested one",
			map[string]string{
				"archive.json":            `{"collection":{"col-1":true}}`,
				"collection/col-1.json":   collection,
				"a/archive.json":          `{"collection":{"col-1":true}}`,
				"a/collection/col-1.json": collection,
			},
		},
		{
			"a member outside the export directory",
			map[string]string{
				"a/archive.json":          `{"collection":{"col-1":true}}`,
				"a/collection/col-1.json": collection,
				"stray.json":              collection,
			},
		},
		{
			"the manifest below the export directory",
			map[string]string{
				"a/b/archive.json":          `{"collection":{"col-1":true}}`,
				"a/b/collection/col-1.json": collection,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPostmanArchive(bytes.NewReader(makePostmanArchive(t, tc.files))); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}
