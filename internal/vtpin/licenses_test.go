package vtpin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeArchive builds a static archive holding the members it is given, with the
// members' bodies carrying the strings the caller wants an inspector to find.
// It writes GNU long names (a `//` string table plus `/N` references) because
// the real archives do — Zig records the object's build directory in the member
// name — so the reader is exercised on the shape a pin actually has.
//
// It is a fixture rather than a real archive because the point of these tests is
// the CHECK — that it fails on a component nobody licensed — and a real archive
// can only be the archive it is.
func fakeArchive(t *testing.T, members map[string]string) string {
	t.Helper()
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names) // a map has no order, and a fixture that varies hides flakes

	var table bytes.Buffer
	offsets := map[string]int{}
	for _, name := range names {
		if len(name) > 15 {
			offsets[name] = table.Len()
			fmt.Fprintf(&table, "%s/\n", name)
		}
	}

	var body bytes.Buffer
	for _, name := range names {
		field := name + "/"
		if off, long := offsets[name]; long {
			field = fmt.Sprintf("/%d", off)
		}
		writeMember(&body, field, members[name])
	}

	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	writeMember(&buf, "//", table.String())
	buf.Write(body.Bytes())

	path := filepath.Join(t.TempDir(), "libfixture.a")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeMember writes one ar member: a 60-byte header, then the body padded to an
// even offset.
func writeMember(buf *bytes.Buffer, field, data string) {
	header := fmt.Sprintf("%-16s%-12s%-6s%-6s%-8s%-10d`\n", field, "0", "0", "0", "100644", len(data))
	if len(header) != 60 {
		panic(fmt.Sprintf("ar header is %d bytes, want 60: %q", len(header), header))
	}
	buf.WriteString(header)
	buf.WriteString(data)
	if len(data)%2 == 1 {
		buf.WriteByte('\n')
	}
}

// TestInspectArchiveAttributesMembersAndFindsDependencyPaths is the classifier's
// own contract: an object member names its component, and a `zig-pkg/<dir>` path
// an object embeds is evidence of a dependency that has no member of its own.
func TestInspectArchiveAttributesMembersAndFindsDependencyPaths(t *testing.T) {
	path := fakeArchive(t, map[string]string{
		".zig-cache/o/abc/libghostty-vt-static_zcu.o": "… /build/ghostty/zig-pkg/uucode-0.2.0-ZZjBPlK5/src/uucode.zig …",
		".zig-cache/o/def/wuffs-v0.4.o":               "wuffs",
	})
	ev, err := InspectArchive(path)
	if err != nil {
		t.Fatalf("InspectArchive: %v", err)
	}
	if got, want := strings.Join(ev.Components, ","), "ghostty,wuffs"; got != want {
		t.Fatalf("members attributed %q, want %q", got, want)
	}
	if len(ev.PkgDirs) != 1 || ev.PkgDirs[0] != "uucode-0.2.0-ZZjBPlK5" {
		t.Fatalf("embedded dependency paths %v, want the uucode directory", ev.PkgDirs)
	}
	// The member name is recorded by its basename: Zig puts the object's
	// build-time directory in the name, and that directory is not stable.
	for _, m := range ev.Members {
		if strings.Contains(m.Name, "/") {
			t.Fatalf("member %q was recorded with a path, want a basename", m.Name)
		}
	}
}

// TestInspectArchiveRefusesAnUnlicensedMember is the discrimination proof for
// the whole document: the archive is the evidence, and a member nobody has a
// rule for is a component that is linked and unlicensed. Without this failing,
// CheckLicenseCoverage could not fail either.
func TestInspectArchiveRefusesAnUnlicensedMember(t *testing.T) {
	path := fakeArchive(t, map[string]string{
		".zig-cache/o/abc/libghostty-vt-static_zcu.o": "ghostty",
		".zig-cache/o/def/newcomer-v1.0.o":            "a dependency nobody has licensed",
	})
	_, err := InspectArchive(path)
	if err == nil {
		t.Fatal("an archive member belonging to no known component was accepted; " +
			"a dependency could be linked in and shipped without its license")
	}
	if !strings.Contains(err.Error(), "newcomer-v1.0.o") {
		t.Fatalf("the failure does not name the member: %v", err)
	}
}

// TestCheckLicenseCoverageIsSymmetric walks the failures the check exists for,
// each from a document that is otherwise complete: a missing component, a
// component the archives do not link, a stale member list, and a dependency
// directory no entry claims. The passing case is asserted too, so a check that
// failed on everything would not look like this one.
func TestCheckLicenseCoverageIsSymmetric(t *testing.T) {
	archives := []ArchiveEvidence{
		{
			Path: "linux-amd64.a",
			Members: []MemberEvidence{
				{Name: "libghostty-vt-static_zcu.o", Component: "ghostty"},
				{Name: "simdutf.o", Component: "simdutf"},
			},
			Components: []string{"ghostty", "simdutf"},
			PkgDirs:    []string{"uucode-0.2.0-hash"},
		},
	}
	complete := &LicensesDoc{
		Preamble: "generated",
		Entries: []LicenseEntry{
			{
				Component: "ghostty", License: "MIT", Members: []string{"libghostty-vt-static_zcu.o"},
				Texts: []LicenseText{{Label: "LICENSE", Provenance: "the pin", Body: strings.Repeat("permission is hereby granted ", 8)}},
			},
			{
				Component: "simdutf", License: "MIT OR Apache-2.0", Members: []string{"simdutf.o"},
				Texts: []LicenseText{{Label: "LICENSE-MIT", Provenance: "supplied", Body: strings.Repeat("permission is hereby granted ", 8)}},
			},
			{
				Component: "uucode", License: "MIT", Evidence: []string{"zig-pkg/uucode-0.2.0-hash"},
				Texts: []LicenseText{{Label: "LICENSE.md", Provenance: "the pin", Body: strings.Repeat("permission is hereby granted ", 8)}},
			},
		},
	}
	if err := CheckLicenseCoverage(archives, complete); err != nil {
		t.Fatalf("a document covering the archives failed: %v", err)
	}

	cases := []struct {
		name   string
		change func(*LicensesDoc)
		wants  string
	}{
		{
			name:   "a member with no entry",
			change: func(d *LicensesDoc) { d.Entries = d.Entries[:2] }, // drops uucode
			wants:  "uucode-0.2.0-hash",
		},
		{
			name:   "a member filed under the wrong component",
			change: func(d *LicensesDoc) { d.Entries[1].Members = []string{"libghostty-vt-static_zcu.o"} },
			wants:  "filed under",
		},
		{
			name: "an entry the archives do not link",
			change: func(d *LicensesDoc) {
				d.Entries = append(d.Entries, LicenseEntry{Component: "openssl", License: "Apache-2.0", Members: []string{"libcrypto.o"}, Texts: []LicenseText{{Label: "LICENSE", Provenance: "nowhere", Body: strings.Repeat("x", 200)}}})
			},
			wants: "claims member libcrypto.o",
		},
		{
			name: "a member the archives no longer contain",
			change: func(d *LicensesDoc) {
				d.Entries[0].Members = append(d.Entries[0].Members, "old_object.o")
			},
			wants: "which no pinned archive contains",
		},
		{
			name:   "a license text too short to be one",
			change: func(d *LicensesDoc) { d.Entries[0].Texts[0].Body = "MIT" },
			wants:  "which is not a license",
		},
		{
			name:   "a license text with no origin",
			change: func(d *LicensesDoc) { d.Entries[0].Texts[0].Provenance = "" },
			wants:  "does not say where it came from",
		},
		{
			name:   "an entry with no license text at all",
			change: func(d *LicensesDoc) { d.Entries[0].Texts = nil },
			wants:  "has no license text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := *complete
			doc.Entries = append([]LicenseEntry(nil), complete.Entries...)
			for i := range doc.Entries {
				doc.Entries[i].Texts = append([]LicenseText(nil), complete.Entries[i].Texts...)
				doc.Entries[i].Members = append([]string(nil), complete.Entries[i].Members...)
				doc.Entries[i].Evidence = append([]string(nil), complete.Entries[i].Evidence...)
			}
			tc.change(&doc)
			err := CheckLicenseCoverage(archives, &doc)
			if err == nil {
				t.Fatalf("%s: the check accepted the document", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("%s: the failure %q does not name %q", tc.name, err, tc.wants)
			}
		})
	}
}

// TestLicensesDocRoundTrips pins the format: the document is generated, fetched
// and then parsed again by the coverage test, so a text the writer cannot
// represent has to be refused rather than silently split.
func TestLicensesDocRoundTrips(t *testing.T) {
	doc := &LicensesDoc{
		Preamble: "THIRD PARTY LICENSES — libghostty-vt\n\npin: somewhere",
		Entries: []LicenseEntry{
			{
				Component: "ghostty", License: "MIT", Members: []string{"a.o", "b.o"}, Evidence: []string{"zig-pkg/dep-1"},
				Texts: []LicenseText{
					{Label: "LICENSE", Provenance: "LICENSE (fork, abcdef)", Body: "MIT License\n\nPermission is hereby granted…\n"},
					{Label: "NOTICE", Provenance: "supplied", Body: "a second text\nwith two lines\n"},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := doc.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := ParseLicensesDoc(&buf)
	if err != nil {
		t.Fatalf("ParseLicensesDoc: %v", err)
	}
	if len(back.Entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(back.Entries))
	}
	got := back.Entries[0]
	if got.Component != "ghostty" || got.License != "MIT" {
		t.Fatalf("round trip lost the header: %+v", got)
	}
	if strings.Join(got.Members, ",") != "a.o,b.o" || strings.Join(got.Evidence, ",") != "zig-pkg/dep-1" {
		t.Fatalf("round trip lost the evidence lines: %+v", got)
	}
	if len(got.Texts) != 2 || got.Texts[1].Label != "NOTICE" || !strings.Contains(got.Texts[0].Body, "Permission is hereby granted") {
		t.Fatalf("round trip lost a license text: %+v", got.Texts)
	}

	// A body containing the document's own separator cannot be represented,
	// and saying so is the only honest answer: the alternative is a document
	// that parses as something it is not.
	broken := &LicensesDoc{Preamble: "x", Entries: []LicenseEntry{{
		Component: "ghostty", License: "MIT",
		Texts: []LicenseText{{Label: "LICENSE", Provenance: "somewhere", Body: "text\n" + sepText + "\nmore"}},
	}}}
	if err := broken.Write(&bytes.Buffer{}); err == nil {
		t.Fatal("a license text containing the document's separator was written")
	}
}

// TestPinnedArchivesAreCoveredByThePinnedLicenses is the check the whole file
// exists for, run against the REAL pin: the six archives a fetch materialises,
// and the THIRD_PARTY_LICENSES document the same fetch installs beside them.
// nocx-ygxjv.14 asks for exactly this — "a test fails if a component is added to
// the list the archive links without a licence entry" — and the honesty is in
// CheckLicenseCoverage: the archive side is read out of the pinned bytes, and
// the comparison runs both ways, so neither a component added to the build nor a
// document left behind by an earlier pin passes.
//
// IT REQUIRES `make vt-archives` TO HAVE RUN, deliberately and not as a skip: a
// test that quietly does nothing where the archives are absent cannot report
// anything about them, and the absence is a state every CI job avoids — each one
// that compiles a CGo package fetches the pin first. The failure names the
// command.
func TestPinnedArchivesAreCoveredByThePinnedLicenses(t *testing.T) {
	m, err := Load(filepath.Join("..", "..", filepath.FromSlash(ManifestPath)))
	if err != nil {
		t.Fatalf("the committed manifest is not loadable: %v", err)
	}
	root := filepath.Join("..", "..", filepath.FromSlash(DefaultRoot))

	archives := make([]ArchiveEvidence, 0, len(m.Targets))
	for _, target := range m.Targets {
		path := VendorArchivePath(root, target.Name)
		ev, inspectErr := InspectArchive(path)
		if inspectErr != nil {
			t.Fatalf("%v\nrun `make vt-archives` to fetch and verify the pin (the archives are build output and "+
				"are not in git)", inspectErr)
		}
		if len(ev.Components) == 0 {
			t.Fatalf("%s: no component was attributed to a single member; the archives are not what this "+
				"inspector reads", path)
		}
		archives = append(archives, ev)
	}

	docPath := m.LicensesPath(root)
	sum, _, err := HashFile(docPath)
	if err != nil {
		t.Fatalf("%v\nrun `make vt-archives`: the licenses document is fetched and verified like the archives", err)
	}
	if sum != m.Licenses.SHA256 {
		t.Fatalf("the licenses document on disk is %s, and the pin says %s: the document being judged has to be "+
			"the pinned one", sum, m.Licenses.SHA256)
	}
	f, err := os.Open(docPath) //nolint:gosec // the fetched pin, verified above
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := ParseLicensesDoc(f)
	if err != nil {
		t.Fatalf("%s: %v", docPath, err)
	}

	if err := CheckLicenseCoverage(archives, doc); err != nil {
		t.Fatalf("the pinned document does not cover the pinned archives: %v", err)
	}

	// And the document has to be ABOUT this pin, not merely consistent: the
	// preamble names the repository and the commit the archives were built
	// from, which is what a reader cites when they ship a binary.
	for _, want := range []string{m.Upstream.Repository, m.Upstream.Commit, m.Upstream.BaseCommit} {
		if !strings.Contains(doc.Preamble, want) {
			t.Fatalf("the document's preamble does not name %q; it has to say which pin it licenses:\n%s",
				want, doc.Preamble)
		}
	}
	if m.Upstream.Patch != "" && !strings.Contains(doc.Preamble, m.Upstream.Patch) {
		t.Fatalf("the document's preamble does not name the patch this pin carries:\n%s", doc.Preamble)
	}
}

// TestDepProvenancesResolvesHashNamedDirectories is the resolver's contract, and
// it exists because the first version of it was wrong in a way nothing else
// could see: the entries that matter are nested inside
// `.dependencies = .{ … }` in a build.zig.zon, and a whole-entry match made the
// outer entry swallow the inner ones — so `highway`, which the pinned tree
// declares in its own pkg/ wrapper, resolved to nothing and the generator
// refused to write a document. The fixture is the shape that broke it.
func TestDepProvenancesResolvesHashNamedDirectories(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// ghostty's own zon declares uucode by URL and hash; each pkg/ wrapper
	// declares the C library it fetches the same way.
	write("build.zig.zon", ".{\n    .name = .ghostty,\n    .dependencies = .{\n"+
		"        .uucode = .{\n            .url = \"https://example.invalid/uucode.tar.gz\",\n"+
		"            .hash = \"uucode-0.2.0-ZZjBPlK5VADj\",\n        },\n    },\n}\n")
	write("pkg/highway/build.zig.zon", ".{\n    .name = .highway,\n    .dependencies = .{\n"+
		"        .highway = .{\n            .url = \"https://example.invalid/highway.tar.gz\",\n"+
		"            .hash = \"N-V-__8AAGmZhABbsPJL\",\n            .lazy = true,\n        },\n"+
		"        .apple_sdk = .{ .path = \"../apple-sdk\" },\n    },\n}\n")
	for _, dir := range []string{"uucode-0.2.0-ZZjBPlK5VADj", "N-V-__8AAGmZhABbsPJL"} {
		if err := os.MkdirAll(filepath.Join(root, "zig-pkg", dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	prov, err := DepProvenances(root)
	if err != nil {
		t.Fatalf("DepProvenances: %v", err)
	}
	got := map[string]string{}
	for _, p := range prov {
		got[p.Component] = p.Dir
	}
	if got["highway"] != "N-V-__8AAGmZhABbsPJL" {
		t.Fatalf("highway resolved to %q, want its wrapper's hash directory: %v", got["highway"], prov)
	}
	if got["uucode"] != "uucode-0.2.0-ZZjBPlK5VADj" {
		t.Fatalf("uucode resolved to %q: %v", got["uucode"], prov)
	}

	// And a hash that names a directory the source does not have is an error
	// naming both, rather than a component that quietly loses its licence.
	if err := os.RemoveAll(filepath.Join(root, "zig-pkg", "N-V-__8AAGmZhABbsPJL")); err != nil {
		t.Fatal(err)
	}
	if _, err := DepProvenances(root); err == nil {
		t.Fatal("a dependency whose fetched directory is absent resolved anyway")
	}
}
