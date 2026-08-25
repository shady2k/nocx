package apiimport

import (
	"sort"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// The seam. Both packages were green while an imported collection could not
// be opened at all: apiimport wrote `collection.json` holding an
// apicoll.Collection, and apicoll reads `nocx-collection.json` holding
// {schemaVersion, name}. Each half tested its own half, and a green half is
// what shipped this.
//
// So this test is deliberately ONE test rather than an import test and an
// open test: it imports, and then it opens what the import wrote, with
// apicoll's own reader — the real one a user reaches, not a fixture written
// by this file. What it asserts is what a user can do: import a Postman
// export, open the folder, and see their requests listed.

// mustImportAndOpen imports doc into a fresh folder and opens it the way the
// product does. It returns the reader, the handle and the listing.
func mustImportAndOpen(t *testing.T, doc string) (apicoll.Collections, apicoll.HandleID, apicoll.Collection) {
	t.Helper()
	dest := destUnder(t)
	if _, err := ImportInto(t.Context(), NewOSFS(), &recordingBinder{}, dest, strings.NewReader(doc), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	// nil Paths: this service reads the folder the user chose and mints
	// none, which is exactly the service the app hands an imported folder.
	svc := apicoll.NewCollections(nil)
	op, err := svc.Open(dest)
	if err != nil {
		t.Fatalf("the import wrote %s and apicoll could not open it: %v", dest, err)
	}
	h, coll := op.Handle, op.Collection
	return svc, h, coll
}

func TestImportedPostmanCollectionOpensAndListsItsRequests(t *testing.T) {
	converted, err := parsePostman(strings.NewReader(postmanFixture), apicoll.Route{})
	if err != nil {
		t.Fatalf("parsePostman: %v", err)
	}
	svc, h, coll := mustImportAndOpen(t, postmanFixture)

	if coll.Name != converted.Collection.Name {
		t.Errorf("the opened collection is called %q; the import named it %q", coll.Name, converted.Collection.Name)
	}
	if len(coll.Malformed) != 0 {
		t.Errorf("the reader called %d of the importer's own files malformed: %+v", len(coll.Malformed), coll.Malformed)
	}

	want := make([]string, 0, len(converted.Collection.Requests))
	for _, ref := range converted.Collection.Requests {
		want = append(want, ref.RelPath)
	}
	got := make([]string, 0, len(coll.Requests))
	for _, ref := range coll.Requests {
		got = append(got, ref.RelPath)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the collection lists\n%v\nand the import wrote\n%v", got, want)
	}
	if len(got) == 0 {
		t.Fatal("this fixture has requests in it and the listing has none")
	}

	// The name and method a user reads in the list are the ones the import
	// meant, and the file behind the row opens.
	byPath := map[string]apicoll.RequestRef{}
	for _, ref := range coll.Requests {
		byPath[ref.RelPath] = ref
	}
	for i, ref := range converted.Collection.Requests {
		listed, ok := byPath[ref.RelPath]
		if !ok {
			t.Fatalf("%q is not in the listing", ref.RelPath)
		}
		if listed.Name != ref.Name || listed.Method != ref.Method {
			t.Errorf("%q lists as %q %q, want %q %q", ref.RelPath, listed.Method, listed.Name, ref.Method, ref.Name)
		}
		r, err := svc.ReadRequest(h, ref.RelPath)
		if err != nil {
			t.Fatalf("read %q back: %v", ref.RelPath, err)
		}
		if r.URL != converted.Requests[i].URL {
			t.Errorf("%q reads back as %q, want %q", ref.RelPath, r.URL, converted.Requests[i].URL)
		}
	}
}

// The environments half of the same seam: the importer writes
// `environments/`, and the reader is the one that has to accept it.
func TestImportedPostmanCollectionOpensItsEnvironments(t *testing.T) {
	converted, err := parsePostman(strings.NewReader(postmanFixture), apicoll.Route{})
	if err != nil {
		t.Fatalf("parsePostman: %v", err)
	}
	if len(converted.Environments) == 0 {
		t.Fatal("this fixture is meant to carry an environment")
	}
	svc, h, _ := mustImportAndOpen(t, postmanFixture)

	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(bad) != 0 {
		t.Errorf("the reader called %d of the importer's environment files malformed: %+v", len(bad), bad)
	}
	if len(envs) != len(converted.Environments) {
		t.Fatalf("%d environments listed, the import wrote %d", len(envs), len(converted.Environments))
	}
	if envs[0].Environment.Name != converted.Environments[0].Name {
		t.Errorf("environment is called %q, want %q", envs[0].Environment.Name, converted.Environments[0].Name)
	}
	// The variable NAMES are in the file; §8 says the values never are.
	if len(envs[0].Environment.SecretVars) != len(converted.Environments[0].SecretVars) {
		t.Errorf("environment declares %v, want %v", envs[0].Environment.SecretVars, converted.Environments[0].SecretVars)
	}
}

// The other entrance through the same writer: one curl line, imported, then
// opened.
func TestImportedCurlLineOpensAndListsItsRequest(t *testing.T) {
	const line = `curl -X POST 'https://api.acme.test/users' -H 'Content-Type: application/json' -d '{"a":1}'`
	_, _, coll := mustImportAndOpen(t, line)

	if len(coll.Requests) != 1 {
		t.Fatalf("a one-request import listed %d requests: %+v", len(coll.Requests), coll.Requests)
	}
	if len(coll.Malformed) != 0 {
		t.Errorf("the reader called %d of the importer's own files malformed: %+v", len(coll.Malformed), coll.Malformed)
	}
	if coll.Requests[0].Method != "POST" {
		t.Errorf("the listed request is %q, want POST", coll.Requests[0].Method)
	}
	if coll.Name == "" {
		t.Error("the opened collection has no name")
	}
}
