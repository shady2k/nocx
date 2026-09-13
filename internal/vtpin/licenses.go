package vtpin

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// THIRD_PARTY_LICENSES is the other half of the licencing rule in
// components.go: the classifier says WHICH components an archive links, and
// this file turns that into the document a binary has to carry.
//
// The document is generated — by third_party/libghostty-vt/scripts/licenses.sh,
// from the source at the pin plus the archives the recipe just built — and it is
// a pinned asset like any other: MANIFEST.json records its sha256, the fetch
// refuses to install a copy that is not those bytes, and CheckLicenseCoverage
// makes the pinned archives judge the pinned document. It is not committed to
// the repository, because the pin is the release and the release is where a
// consumer gets it.

// Separators. Both are longer than anything in a license text and are the only
// structure the parser relies on, so a body that contained one is refused
// rather than silently split (see writeLicenseBody).
var (
	sepMajor = strings.Repeat("=", 80)
	sepText  = strings.Repeat("-", 80)
)

// DepProvenance is where one fetched Zig dependency's source came from: the
// `zig-pkg` directory Zig imported it into, the build.zig.zon that named it,
// and the content hash that names the directory. It is DERIVED from the pinned
// source rather than stored in the manifest — a hash written down twice is a
// hash that can disagree with itself — and it is what turns a `zig-pkg/<dir>`
// path embedded in an object into the name of a component.
type DepProvenance struct {
	Component  string
	Dependency string
	Dir        string
	Zone       string
	Hash       string
}

// LicenseText is one license file's text, with the line that says where it came
// from. It is a file and not a string because the label and the provenance are
// as much of the answer as the text: a reader has to be able to check that the
// text is the license of the component it sits under.
type LicenseText struct {
	Label      string
	Provenance string
	Body       string
}

// LicenseEntry is one component's block in the document.
type LicenseEntry struct {
	Component string
	License   string
	Members   []string
	Evidence  []string
	Texts     []LicenseText
}

// LicensesDoc is the whole of THIRD_PARTY_LICENSES.
type LicensesDoc struct {
	Preamble string
	Entries  []LicenseEntry
}

// Write renders the document.
func (d *LicensesDoc) Write(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s\n\n", strings.TrimRight(d.Preamble, "\n")); err != nil {
		return err
	}
	for _, e := range d.Entries {
		if _, err := fmt.Fprintf(w, "%s\nCOMPONENT: %s\nLICENSE: %s\n", sepMajor, e.Component, e.License); err != nil {
			return err
		}
		if len(e.Members) > 0 {
			if _, err := fmt.Fprintf(w, "LINKED AS: %s\n", strings.Join(e.Members, " ")); err != nil {
				return err
			}
		}
		if len(e.Evidence) > 0 {
			if _, err := fmt.Fprintf(w, "EVIDENCE: %s\n", strings.Join(e.Evidence, " ")); err != nil {
				return err
			}
		}
		for _, t := range e.Texts {
			if err := checkBodyIsSerialisable(e.Component, t); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%s\nTEXT: %s\nORIGIN: %s\n%s\n", sepText, t.Label, t.Provenance, sepText); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%s\n", strings.TrimRight(t.Body, "\n")); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkBodyIsSerialisable refuses a license text that would break the
// document's own structure. It is here rather than trusted because a silently
// split body is a body that verifies as something it is not.
func checkBodyIsSerialisable(componentName string, t LicenseText) error {
	for _, line := range strings.Split(t.Body, "\n") {
		if strings.TrimRight(line, " \t\r") == sepText || strings.TrimRight(line, " \t\r") == sepMajor {
			return fmt.Errorf("component %s: the text of %s contains the document's own separator, "+
				"which cannot be represented", componentName, t.Label)
		}
	}
	if strings.TrimSpace(t.Label) == "" || strings.TrimSpace(t.Provenance) == "" {
		return fmt.Errorf("component %s: a license text without a TEXT or ORIGIN line", componentName)
	}
	return nil
}

// CheckLicenseCoverage makes the pinned archives judge the pinned document, and
// it is the check the brief for nocx-ygxjv.14 asks for: a component added to
// the build without a license entry fails here.
//
// HOW THIS IS HONEST, since a coverage check is easy to write so that it cannot
// fail. Nothing in it compares the document with itself:
//
//   - The archive side is EVIDENCE, read out of the pinned bytes: object member
//     names, and the `zig-pkg/<directory>` paths the objects embed. Neither is
//     supplied by the document, and neither needs the source tree to read —
//     which is why this runs wherever the archives have been fetched, and not
//     only where a recipe has built them.
//   - Every member of every archive must be attributed to a component by
//     components.go's rules, and must be named by exactly the entry that
//     component has. A member nobody has a rule for is a FAILURE naming it, so
//     a new dependency cannot be linked in and licensed by silence.
//   - Every `zig-pkg/<directory>` an object embeds must be claimed by some
//     entry, which is how a component compiled into another component's
//     compilation unit is caught at all.
//   - The comparison runs BOTH ways: an entry whose members or evidence no
//     archive contains is a failure, so a document generated from an earlier
//     pin fails against a later one instead of quietly covering it.
//   - The one thing it cannot see is a component that is linked AND invisible
//     in the archive — no member of its own, no path string, and therefore
//     nothing to compare. That limit is stated rather than papered over: the
//     rules in components.go fail closed on everything they can see, and a
//     component nobody can see is a component nobody's automated check can find.
func CheckLicenseCoverage(archives []ArchiveEvidence, doc *LicensesDoc) error {
	observedMembers := map[string]string{}
	observedDirs := map[string]bool{}
	for _, ev := range archives {
		for _, m := range ev.Members {
			observedMembers[m.Name] = m.Component
		}
		for _, dir := range ev.PkgDirs {
			observedDirs["zig-pkg/"+dir] = true
		}
	}

	claimedMembers := map[string]bool{}
	claimedDirs := map[string]bool{}
	for _, e := range doc.Entries {
		if e.Component == "" {
			return fmt.Errorf("an entry in the document names no component")
		}
		if strings.TrimSpace(e.License) == "" {
			return fmt.Errorf("component %q has no license", e.Component)
		}
		if len(e.Texts) == 0 {
			return fmt.Errorf("component %q has no license text", e.Component)
		}
		for _, t := range e.Texts {
			if len(strings.TrimSpace(t.Body)) < 100 {
				return fmt.Errorf("component %q: the text of %s is %d bytes, which is not a license",
					e.Component, t.Label, len(strings.TrimSpace(t.Body)))
			}
			if strings.TrimSpace(t.Provenance) == "" {
				return fmt.Errorf("component %q: the text of %s does not say where it came from", e.Component, t.Label)
			}
		}
		if len(e.Members) == 0 && !anyObserved(e.Evidence, observedDirs) {
			return fmt.Errorf("the document licenses %q, which no pinned archive links: it has no member of its own "+
				"and no dependency directory an object embeds", e.Component)
		}
		for _, name := range e.Members {
			component, seen := observedMembers[name]
			if !seen {
				return fmt.Errorf("component %q claims member %s, which no pinned archive contains: "+
					"the document was generated from a different pin", e.Component, name)
			}
			if component != e.Component {
				return fmt.Errorf("member %s is attributed to %s in the archives and filed under %s in the document",
					name, component, e.Component)
			}
			claimedMembers[name] = true
		}
		for _, dir := range e.Evidence {
			if !observedDirs[dir] {
				return fmt.Errorf("component %q claims evidence %s, which no pinned archive embeds", e.Component, dir)
			}
			claimedDirs[dir] = true
		}
	}

	for name, component := range observedMembers {
		if !claimedMembers[name] {
			return fmt.Errorf("member %s belongs to component %s and no entry in the document names it: "+
				"a dependency is linked without a license", name, component)
		}
	}
	for dir := range observedDirs {
		if !claimedDirs[dir] {
			return fmt.Errorf("the archives link %s and no entry in the document claims it: "+
				"a dependency was added to the build and its license is missing", dir)
		}
	}
	return nil
}

func anyObserved(evidence []string, observed map[string]bool) bool {
	for _, e := range evidence {
		if observed[e] {
			return true
		}
	}
	return false
}

// pkgDirToDep maps a component's dependency directory back to the component, so
// an archive's embedded paths can be attributed.
func depProvenanceFor(dir string, prov []DepProvenance) (DepProvenance, bool) {
	for _, p := range prov {
		if p.Dir == dir {
			return p, true
		}
	}
	return DepProvenance{}, false
}

// GenerateLicenses builds the document from the pinned source and the archives
// the recipe built. Everything it writes is read from one of those two places:
//
//   - the component list and the evidence, from the archives (components.go);
//   - each license text, from the pinned source tree — a dependency's own
//     license files, or ghostty's — except for the components whose upstream
//     tree carries none, whose texts are the committed copies under
//     third_party/libghostty-vt/licenses and whose ORIGIN line says so.
//
// It refuses to write a document it could not complete: a missing license file,
// an archive it cannot classify or a dependency it cannot resolve is an error
// naming the file, never a document with a hole in it.
func GenerateLicenses(m *Manifest, srcRoot, repoRoot, dist string) (*LicensesDoc, error) {
	archives, err := inspectArchives(dist, m)
	if err != nil {
		return nil, err
	}
	prov, err := DepProvenances(srcRoot)
	if err != nil {
		return nil, err
	}
	linked := map[string]bool{}
	for _, ev := range archives {
		ids, err := ev.Link(prov)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			linked[id] = true
		}
	}

	doc := &LicensesDoc{Preamble: licensesPreamble(m, len(archives), len(linked))}
	for _, c := range components {
		if !linked[c.ID] {
			continue
		}
		entry := LicenseEntry{
			Component: c.ID,
			License:   c.License,
			Members:   membersFor(archives, c.ID),
			Evidence:  evidenceFor(archives, prov, c.ID),
		}
		for _, pattern := range c.Licenses {
			texts, err := readLicensePattern(m, srcRoot, prov, c, pattern)
			if err != nil {
				return nil, err
			}
			entry.Texts = append(entry.Texts, texts...)
		}
		for _, supplied := range c.Supplied {
			text, err := readSuppliedLicense(repoRoot, supplied)
			if err != nil {
				return nil, err
			}
			entry.Texts = append(entry.Texts, text)
		}
		if len(entry.Texts) == 0 {
			return nil, fmt.Errorf("component %s: no license text found; a binary may not ship without one", c.ID)
		}
		doc.Entries = append(doc.Entries, entry)
	}
	if err := CheckLicenseCoverage(archives, doc); err != nil {
		return nil, fmt.Errorf("the document it just wrote does not cover the archives: %w", err)
	}
	return doc, nil
}

func membersFor(archives []ArchiveEvidence, id string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ev := range archives {
		for _, m := range ev.Members {
			if m.Component == id && !seen[m.Name] {
				seen[m.Name] = true
				out = append(out, m.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func evidenceFor(archives []ArchiveEvidence, prov []DepProvenance, id string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ev := range archives {
		for _, dir := range ev.PkgDirs {
			p, ok := depProvenanceFor(dir, prov)
			if !ok || p.Component != id || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, "zig-pkg/"+dir)
		}
	}
	sort.Strings(out)
	return out
}

// readLicensePattern reads every file one glob matches under the pinned source
// root. A glob that matches nothing is an error: it means the pin moved and the
// license this component used to carry is somewhere else now.
func readLicensePattern(m *Manifest, srcRoot string, prov []DepProvenance, c component, pattern string) ([]LicenseText, error) {
	rel := pattern
	if strings.Contains(pattern, "{dep}") {
		dir, err := depDir(prov, c)
		if err != nil {
			return nil, err
		}
		rel = strings.ReplaceAll(pattern, "{dep}", "zig-pkg/"+dir)
	}
	matches, err := filepath.Glob(filepath.Join(srcRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("component %s: %w", c.ID, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("component %s: %s matches no license file in the pinned source "+
			"(the pin moved, or the component no longer ships its license)", c.ID, rel)
	}
	sort.Strings(matches)
	out := make([]LicenseText, 0, len(matches))
	for _, match := range matches {
		data, err := os.ReadFile(match) //nolint:gosec // a license file inside the pinned source the caller named
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(srcRoot, match)
		if err != nil {
			rel = match
		}
		out = append(out, LicenseText{
			Label:      filepath.Base(match),
			Provenance: fmt.Sprintf("%s (%s %s)", filepath.ToSlash(rel), m.Upstream.Repository, m.ShortCommit()),
			Body:       string(data),
		})
	}
	return out, nil
}

// readSuppliedLicense reads a committed copy. The provenance has to be in the
// table as well as the file: a license text with no stated origin is worse than
// none, because it looks checkable and is not.
func readSuppliedLicense(repoRoot, rel string) (LicenseText, error) {
	origin, ok := suppliedLicenses[rel]
	if !ok {
		return LicenseText{}, fmt.Errorf("%s is supplied without an origin; add it to suppliedLicenses "+
			"(internal/vtpin/licenses.go)", rel)
	}
	path := filepath.Join(repoRoot, "third_party", "libghostty-vt", filepath.FromSlash(rel))
	data, err := os.ReadFile(path) //nolint:gosec // a committed license copy the table above names
	if err != nil {
		return LicenseText{}, err
	}
	return LicenseText{Label: filepath.Base(rel), Provenance: origin, Body: string(data)}, nil
}

// depDir resolves a component's dependency directory. A component with no Dep
// has no directory, and asking for one is a bug rather than a missing file.
func depDir(prov []DepProvenance, c component) (string, error) {
	for _, p := range prov {
		if p.Component == c.ID {
			return p.Dir, nil
		}
	}
	return "", fmt.Errorf("component %s is fetched as dependency %q and no zig-pkg directory was resolved for it",
		c.ID, c.Dep)
}

func licensesPreamble(m *Manifest, archives, comps int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "THIRD PARTY LICENSES — %s\n\n", m.Dependency)
	fmt.Fprintf(&b, "Every binary that links the pinned %s archives carries the licenses of what is\n"+
		"inside them, so this file lists each component those archives statically link and\n"+
		"reproduces the text of every license that applies to it.\n\n", m.Dependency)
	fmt.Fprintf(&b, "It is GENERATED by third_party/libghostty-vt/scripts/licenses.sh from the source\n"+
		"at the pin and the archives built from it, and it is a pinned asset: MANIFEST.json\n"+
		"records its sha256, cmd/vtfetch verifies that before a build links anything, and\n"+
		"internal/vtpin's coverage test makes the pinned archives judge it.\n\n")
	fmt.Fprintf(&b, "pin:            %s\n", m.Upstream.Repository)
	fmt.Fprintf(&b, "commit:         %s\n", m.Upstream.Commit)
	fmt.Fprintf(&b, "upstream base:  %s\n", m.Upstream.BaseCommit)
	if m.Upstream.Patch != "" {
		fmt.Fprintf(&b, "patch:          %s\n", m.Upstream.Patch)
	}
	fmt.Fprintf(&b, "toolchain:      Zig %s\n", m.Toolchain.Zig)
	fmt.Fprintf(&b, "covers:         %d archives, %d components\n\n", archives, comps)
	fmt.Fprintf(&b, "The LINKED AS line of each entry names the object members of the archives that\n"+
		"belong to the component, and EVIDENCE names the zig-pkg/<directory> paths those\n"+
		"objects embed — the directories Zig fetched a dependency into, which is how a\n"+
		"component compiled into another component's compilation unit is visible at all.\n"+
		"A member or a directory that matches no entry fails internal/vtpin's coverage test,\n"+
		"which is what stops a dependency from being added to the build without a license.\n\n")
	return b.String()
}

// The suppliedLicenses table is the honest exception, and it is why the document
// has an ORIGIN line: two components the archives link ship no license file in
// the pinned inputs at all. Their texts are committed here instead, each with
// the upstream file it was taken from, so a reader can check the copy against
// the project it names rather than against nothing.
var suppliedLicenses = map[string]string{
	"licenses/simdutf/LICENSE-MIT.txt": "simdutf's own MIT license, from simdutf v9.0.0 — the version " +
		"pkg/simdutf/vendor/simdutf.h reports at the pin (https://github.com/simdutf/simdutf/blob/v9.0.0/LICENSE-MIT, " +
		"sha256 fc8dbc04e03ad4efc08a647ffe7f995b811a95bc04c0e85a56d5277c6593fa5f); the pinned tree vendors simdutf " +
		"as an amalgamated pkg/simdutf/vendor/simdutf.{h,cpp} and copies no license with it",
	"licenses/simdutf/LICENSE-APACHE.txt": "simdutf's own Apache-2.0 license, from simdutf v9.0.0 " +
		"(https://github.com/simdutf/simdutf/blob/v9.0.0/LICENSE-APACHE, " +
		"sha256 3d34610fc6b5e1b0bfe4e2f36171c2d62c28ef05cb8d704f5a0073be41a43b3d); the pinned tree copies no license",
	"licenses/simdutf/isadetection-BSD3.txt": "the BSD-3 notice for the isadetection code simdutf carries " +
		"(sha256 6c3b4437dc5ac9c49ad75c38934ceca4b4649c79d1808b4b71d2e3f3c0448284); extracted verbatim from " +
		"pkg/simdutf/vendor/simdutf.h at the pin, where it is a comment inside a file rather than a license file, " +
		"so nothing else would find it",
	"licenses/zig/compiler_rt-LICENSE.txt": "Zig's own MIT license, 'The MIT License (Expat)', from Zig 0.16.0 — " +
		"the toolchain the pin names (https://codeberg.org/ziglang/zig/raw/tag/0.16.0/LICENSE, " +
		"sha256 5c537d6853e005298a285d508cff9ac7192cea23576c840d485b2b586a7ff177; the version comes from a " +
		"source tarball of that tag, https://codeberg.org/ziglang/zig/archive/0.16.0.tar.gz). compiler_rt.o is " +
		"Zig's PORT of LLVM's compiler-rt — Zig's own tree labels the ported files 'derived work from LLVM " +
		"Compiler Infrastructure - release 8.0 (MIT)', lib/compiler_rt/emutls.zig:3 — and Zig ships exactly one " +
		"license file for the whole tree, this one, which is why the port carries the same text. A Zig " +
		"INSTALLATION ships no license file at all, so this copy is the repository's",
}

// DepProvenances resolves every component that is fetched as a Zig dependency
// to the `zig-pkg` directory it landed in. The directory name IS the content
// hash recorded in a build.zig.zon, so this is a lookup of one pinned value to
// find another rather than a second place to maintain — and a dependency whose
// hash moves is a dependency this stops resolving, which is an error.
func DepProvenances(srcRoot string) ([]DepProvenance, error) {
	zones, err := zoneFiles(srcRoot)
	if err != nil {
		return nil, err
	}
	contents := make(map[string]string, len(zones))
	for _, zone := range zones {
		data, err := os.ReadFile(zone) //nolint:gosec // a build.zig.zon inside the pinned source the caller named
		if err != nil {
			return nil, err
		}
		contents[zone] = string(data)
	}
	var out []DepProvenance
	for _, c := range components {
		if c.Dep == "" {
			continue
		}
		for _, zone := range zones {
			hash, ok := dependencyHash(contents[zone], c.Dep)
			if !ok {
				continue
			}
			if _, err := os.Stat(filepath.Join(srcRoot, "zig-pkg", hash)); err != nil {
				return nil, fmt.Errorf("component %s: %s names dependency %q at hash %s, and zig-pkg/%s is not "+
					"in the pinned source (run the build once so Zig fetches it)", c.ID, zone, c.Dep, hash, hash)
			}
			rel, relErr := filepath.Rel(srcRoot, zone)
			if relErr != nil {
				rel = zone
			}
			out = append(out, DepProvenance{
				Component:  c.ID,
				Dependency: c.Dep,
				Dir:        hash,
				Zone:       filepath.ToSlash(rel),
				Hash:       hash,
			})
			break
		}
		// A component the pin no longer fetches is skipped rather than an
		// error: a build graph that dropped a dependency cannot link it, so
		// there is no licence to carry. The two directions that DO have to
		// fail still do — a dependency the pin declares whose fetched source
		// is absent is the error above, and a component that is linked while
		// its text cannot be found fails in readLicensePattern.
	}
	return out, nil
}

// zoneFiles are the build.zig.zon files a dependency can be declared in: the
// root one (Zig libraries), a pkg/ wrapper (the C libraries ghostty vendors)
// and a fetched dependency's own (a dependency of a dependency).
func zoneFiles(srcRoot string) ([]string, error) {
	zones := []string{filepath.Join(srcRoot, "build.zig.zon")}
	for _, dir := range []string{"pkg", "zig-pkg"} {
		matches, err := filepath.Glob(filepath.Join(srcRoot, dir, "*", "build.zig.zon"))
		if err != nil {
			return nil, err
		}
		zones = append(zones, matches...)
	}
	sort.Strings(zones)
	for _, zone := range zones {
		if _, err := os.Stat(zone); err != nil {
			return nil, fmt.Errorf("%s: %w", zone, err)
		}
	}
	return zones, nil
}

var (
	depEntryRE = regexp.MustCompile(`\.([A-Za-z0-9_]+)\s*=\s*\.\{`)
	depHashRE  = regexp.MustCompile(`hash\s*=\s*"([^"]+)"`)
)

// dependencyHash reads the content hash a build.zig.zon records for one
// dependency. It is a scan of one field of one entry rather than a Zig-language
// parser, and a zon it cannot read is an error rather than an empty answer.
//
// It looks for the entry's OPENING brace and then stops at the first closing
// one, rather than matching a whole `.key = .{ … }` group: the entries that
// matter here are nested inside `.dependencies = .{ … }`, and a group match
// makes the outer entry swallow the inner ones — measured, because the first
// version of this function found `.dependencies` and reported that `highway`
// was declared nowhere.
func dependencyHash(zone, dep string) (string, bool) {
	for _, m := range depEntryRE.FindAllStringSubmatchIndex(zone, -1) {
		if zone[m[2]:m[3]] != dep {
			continue
		}
		rest := zone[m[1]:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return "", false
		}
		h := depHashRE.FindStringSubmatch(rest[:end])
		if h == nil {
			return "", false
		}
		return h[1], true
	}
	return "", false
}
