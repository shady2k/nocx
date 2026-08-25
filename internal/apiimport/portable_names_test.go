package apiimport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/pathname"
)

// The minting side of the one rule. Whatever a Postman export calls a folder
// or a request, what this package turns it into is a name the store accepts —
// which is the property that used to be maintained by hand, in two packages,
// with two lists.
func TestSlug_MintsOnlyNamesTheStoreAccepts(t *testing.T) {
	for _, name := range []string{
		"CON", "con", "com1", "LPT9", "aux", "nul", "prn",
		"docs.", "docs ", "...", "..", ".", "", "   ",
		"../../etc/passwd", `..\..\windows`, "a:b", "a<b>c", "a\x00b", "a\x07b",
		strings.Repeat("あ", 400), strings.Repeat("x", 4000), "console", "com10",
		"Create user", "Список пользователей", "🙂",
	} {
		got := slug(name, pathname.MaxComponentBytes)
		if got == "" {
			continue // nothing usable; take() falls back to its own name
		}
		if err := pathname.CheckComponent(got); err != nil {
			t.Errorf("slug(%q) minted %q, which the store refuses: %v", name, got, err)
		}
	}
}

// The whole component is base + extension + a collision suffix, and it is
// the WHOLE component the filesystem has to take. A base minted right up to
// the limit and then extended is the shape that made a minter and a
// validator disagree.
func TestPathAllocator_MintsPathsTheStoreAccepts(t *testing.T) {
	p := newPathAllocator()
	long := strings.Repeat("x", 4000)
	dir := ""
	for i := 0; i < 60; i++ {
		rel := p.take(dir, long, fallbackRequest, ".json")
		if err := pathname.CheckRelPath(rel); err != nil {
			t.Fatalf("take minted %q on collision %d, which the store refuses: %v", rel, i, err)
		}
	}
	// The same for a folder with no extension, and for a device name.
	for _, name := range []string{"CON", "com1", "docs.", strings.Repeat("あ", 200)} {
		rel := p.take("users", name, fallbackFolder, "")
		if err := pathname.CheckRelPath(rel); err != nil {
			t.Errorf("take(%q) minted %q, which the store refuses: %v", name, rel, err)
		}
	}
}

// The seam, and the acceptance criterion stated as a test: a name the
// importer MINTS is a name the store ACCEPTS. Not asserted against a copy of
// the rule — asserted by importing an export whose every name is hostile and
// then reading each file back through apicoll's own reader, the one a user
// reaches.
func TestImportedHostileNamesOpenAndRead(t *testing.T) {
	svc, h, coll := mustImportAndOpen(t, hostileNameFixture)

	if len(coll.Malformed) != 0 {
		t.Errorf("the reader called %d of the importer's own files malformed: %+v", len(coll.Malformed), coll.Malformed)
	}
	if len(coll.Requests) != 6 {
		t.Fatalf("the collection lists %d requests, want 6: %+v", len(coll.Requests), coll.Requests)
	}
	for _, ref := range coll.Requests {
		if err := pathname.CheckRelPath(ref.RelPath); err != nil {
			t.Errorf("the import wrote %q, which is not a path every platform takes: %v", ref.RelPath, err)
		}
		if _, err := svc.ReadRequest(h, ref.RelPath); err != nil {
			t.Errorf("the import wrote %q and the store refuses to read it: %v", ref.RelPath, err)
		}
	}

	envs, _, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	for _, e := range envs {
		if err := pathname.CheckRelPath(e.RelPath); err != nil {
			t.Errorf("the import wrote the environment %q: %v", e.RelPath, err)
		}
		if _, err := svc.ReadEnvironment(h, e.RelPath); err != nil {
			t.Errorf("the store refuses to read the environment the import wrote at %q: %v", e.RelPath, err)
		}
	}
}

// Every name here is one Windows refuses outright, so before this the import
// wrote a collection that could not be checked out there at all.
const hostileNameFixture = `{
  "info": { "name": "CON", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
  "item": [
    {
      "name": "CON",
      "item": [
        { "name": "PRN", "request": { "method": "GET", "url": "https://api.test/a" } },
        { "name": "docs.", "request": { "method": "GET", "url": "https://api.test/b" } }
      ]
    },
    { "name": "COM1", "request": { "method": "GET", "url": "https://api.test/c" } },
    { "name": "nul", "request": { "method": "GET", "url": "https://api.test/d" } },
    { "name": "trailing ", "request": { "method": "GET", "url": "https://api.test/e" } },
    { "name": "a:b<c>d|e?f*g", "request": { "method": "GET", "url": "https://api.test/f" } }
  ]
}`

// A path is bounded as a whole, not only per name: an export may nest as
// deep as it likes, and each level costs a directory on the machine the
// collection is checked out on. The bound is refused rather than clamped,
// and it names its number.
func TestImport_RefusesFoldersNestedPastTheBound(t *testing.T) {
	// The deepest chain that is still a path the store takes: 31 folders and
	// the request in the bottom one is a 32-component path.
	res, err := parsePostman(strings.NewReader(nestedFixture(pathname.MaxDepth-1)), apicoll.Route{})
	if err != nil {
		t.Fatalf("an export nested %d deep was refused: %v", pathname.MaxDepth-1, err)
	}
	if len(res.Collection.Requests) != 1 {
		t.Fatalf("the deep export produced %d requests, want 1", len(res.Collection.Requests))
	}
	rel := res.Collection.Requests[0].RelPath
	if pathErr := safeRelPath(rel); pathErr != nil {
		t.Errorf("the deepest allowed export minted %q, which the store refuses: %v", rel, pathErr)
	}

	// One folder more is one component more than the path bound.
	_, err = parsePostman(strings.NewReader(nestedFixture(pathname.MaxDepth)), apicoll.Route{})
	if err == nil {
		t.Fatalf("an export nested %d deep was accepted, want a refusal", pathname.MaxDepth)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(pathname.MaxDepth-1)) {
		t.Errorf("the refusal is %q and does not name the bound", err)
	}
}

// nestedFixture is one request at the bottom of depth folders.
func nestedFixture(depth int) string {
	inner := `{ "name": "r", "request": { "method": "GET", "url": "https://api.test/x" } }`
	for i := 0; i < depth; i++ {
		inner = fmt.Sprintf(`{ "name": "f%d", "item": [ %s ] }`, i, inner)
	}
	return `{ "info": { "name": "Deep", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
	  "item": [ ` + inner + ` ] }`
}
