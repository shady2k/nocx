package vtpin

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file answers one question about a pinned archive: WHICH third-party
// components does it link? That is a licencing question before it is anything
// else — every binary nocx distributes has to carry the licenses of the code
// inside it — and the answer has to come from the archive, because the source
// tree contains far more than any one target links.
//
// EVERY RULE BELOW WAS MEASURED, not copied from a list somebody wrote down.
// `ar t` names an archive's object members and `nm`/`strings` on each member
// says which component it is. The archive at the pin has twelve members
// (measured 2026-09-13, linux-amd64):
//
//   - base64.o, codepoint_width.o, index_of.o, vt.o — ghostty's own
//     src/simd/*.cpp, which is why they define ghostty_simd_* symbols.
//   - libghostty-vt-static_zcu.o — the Zig compilation unit of libghostty-vt
//     itself, and the reason a component can be linked without an object of its
//     own: a Zig DEPENDENCY is compiled into this unit, so uucode's grapheme
//     tables live here rather than in a member named after them.
//   - simdutf.o, wuffs-v0.4.o — the two C libraries ghostty vendors.
//   - libhighway_zcu.o, abort.o, per_target.o, targets.o — Google Highway
//     (hwy/abort.cc, hwy/per_target.cc, hwy/targets.cc and its Zig unit).
//   - compiler_rt.o — from the ZIG INSTALLATION, not from the source tree: the
//     archive's member path is Zig's own global cache.
//
// Two consequences are load-bearing, and both are why this is a table of rules
// rather than a list of names:
//
//   - A member that matches NO rule is an error, never a shrug. It is the one
//     signal that a pin bump added a component nobody has licensed yet, and it
//     fails the coverage test in this package rather than shipping quietly.
//   - A component can be linked WITHOUT a member of its own, so an archive is
//     also scanned for the `zig-pkg/<dir>` paths its objects embed. A path
//     nobody has declared is the same failure by the other door.

// component is one third-party component: how to recognise it in an archive,
// and where its license text comes from.
type component struct {
	// ID names the component in THIRD_PARTY_LICENSES.
	ID string
	// Members are the basenames of the archive's object members that belong to
	// it. Empty is legitimate: uucode is compiled into another member.
	Members []string
	// Dep is the build.zig.zon dependency it is fetched as, when the pinned
	// build fetches it. Its `zig-pkg/<dir>` directory is resolved from the
	// dependency's content hash rather than written down, so a dependency whose
	// version moves is a directory this stops finding — loudly.
	Dep string
	// Licenses are globs, relative to the pinned source root, holding the
	// component's license text; {dep} stands for the dependency's zig-pkg
	// directory. A glob matching nothing is an error: a license file the
	// generator cannot read is a license the binary does not carry.
	Licenses []string
	// License summarises the entry as one line for a reader, and every value
	// here is the statement the license FILES make rather than a guess about
	// them: highway ships Apache-2.0 and BSD-3 texts, Wuffs' LICENSE opens
	// "distributed under the terms of both the MIT license and the Apache
	// License (Version 2.0)", and simdutf publishes the same pair.
	License string
	// Supplied are license texts that are NOT in the pinned inputs at all,
	// relative to the repository root. Only a component whose upstream tree
	// genuinely ships no license text has one, and the document says so in the
	// entry's provenance line — See licenses.go and README.md, "Third-party
	// licenses".
	Supplied []string
}

// components are the third-party components the pin links, in the order the
// document lists them. It is deliberately a slice and not a map: a reader
// reads it top to bottom, and the document's order is this order.
var components = []component{
	{
		ID:       "ghostty",
		Members:  []string{"base64.o", "codepoint_width.o", "index_of.o", "libghostty-vt-static_zcu.o", "vt.o"},
		Licenses: []string{"LICENSE"},
		License:  "MIT",
	},
	{
		ID:       "highway",
		Members:  []string{"abort.o", "libhighway_zcu.o", "per_target.o", "targets.o"},
		Dep:      "highway",
		Licenses: []string{"{dep}/LICENSE*"},
		License:  "Apache-2.0 OR BSD-3-Clause",
	},
	{
		ID:       "simdutf",
		Members:  []string{"simdutf.o"},
		Supplied: []string{"licenses/simdutf/LICENSE-MIT.txt", "licenses/simdutf/LICENSE-APACHE.txt", "licenses/simdutf/isadetection-BSD3.txt"},
		License:  "MIT OR Apache-2.0 (and a BSD-3 notice for the isadetection code it embeds)",
	},
	{
		ID:       "uucode",
		Dep:      "uucode",
		Licenses: []string{"{dep}/LICENSE*"},
		License:  "MIT",
	},
	{
		ID:       "wuffs",
		Members:  []string{"wuffs-v0.4.o"},
		Dep:      "wuffs",
		Licenses: []string{"{dep}/LICENSE*"},
		License:  "MIT OR Apache-2.0",
	},
	{
		ID:       "compiler_rt",
		Members:  []string{"compiler_rt.o"},
		Supplied: []string{"licenses/zig/compiler_rt-LICENSE.txt"},
		License:  "MIT (Zig's compiler runtime)",
	},
}

// componentForMember returns the component a member basename belongs to. The
// second result is false for a member nobody has licensed, which every caller
// treats as a failure.
func componentForMember(base string) (string, bool) {
	for _, c := range components {
		for _, m := range c.Members {
			if m == base {
				return c.ID, true
			}
		}
	}
	return "", false
}

// pkgDirRE finds the `zig-pkg/<directory>` paths an archive's objects embed.
// Zig imports the pinned dependencies into that directory inside the build
// root, and a source path from it reaches the objects as a string.
var pkgDirRE = regexp.MustCompile(`zig-pkg/([A-Za-z0-9._+-]+)`)

// ArchiveEvidence is what one archive says about what it links.
type ArchiveEvidence struct {
	// Path is the archive this was read from.
	Path string
	// Components are the component IDs the archive's MEMBERS attribute, sorted.
	Components []string
	// Members are the object members by basename, sorted, each named with the
	// component it belongs to.
	Members []MemberEvidence
	// PkgDirs are the `zig-pkg/<dir>` directories the archive's objects embed,
	// sorted. They are how a component linked into another component's
	// compilation unit is seen at all.
	PkgDirs []string
}

// MemberEvidence is one archive member and the component it belongs to.
type MemberEvidence struct {
	Name      string
	Component string
}

// Link resolves the components an archive links: what its members attribute,
// plus what its embedded dependency paths attribute once they are resolved
// against the provenance of the source they were built from.
func (e ArchiveEvidence) Link(prov []DepProvenance) ([]string, error) {
	seen := map[string]bool{}
	for _, m := range e.Members {
		seen[m.Component] = true
	}
	for _, dir := range e.PkgDirs {
		id, err := componentForDepDir(dir, prov)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Path, err)
		}
		seen[id] = true
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// componentForDepDir names the component a `zig-pkg/<dir>` directory belongs
// to. The directory is a content hash, so the answer comes from the provenance
// the generator read out of the pinned build graph — and a directory nobody
// declared is an error naming the directory, because that is a dependency
// linked without a license.
func componentForDepDir(dir string, prov []DepProvenance) (string, error) {
	if p, ok := depProvenanceFor(dir, prov); ok {
		return p.Component, nil
	}
	return "", fmt.Errorf("zig-pkg/%s is linked but no component in the pin claims it: "+
		"a dependency was added to the build and its license is missing (internal/vtpin/components.go)", dir)
}

// InspectArchive reads an archive and reports what it links. It is pure Go on
// purpose: a license check that needed `ar` and `nm` would be a check that
// silently knows nothing on a machine without binutils, which is exactly the
// machine a release gets cut on.
func InspectArchive(path string) (ArchiveEvidence, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the archive the caller named: a fetched pinned file or a fixture
	if err != nil {
		return ArchiveEvidence{}, err
	}
	ev := ArchiveEvidence{Path: path}
	members, err := archiveMembers(data)
	if err != nil {
		return ArchiveEvidence{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, name := range members {
		base := arBaseName(name)
		comp, ok := componentForMember(base)
		if !ok {
			return ArchiveEvidence{}, fmt.Errorf("%s: member %q belongs to no component this pin knows: "+
				"a new dependency is linked and unlicensed (internal/vtpin/components.go)", path, name)
		}
		ev.Members = append(ev.Members, MemberEvidence{Name: base, Component: comp})
	}
	seenMember := map[string]bool{}
	for _, m := range ev.Members {
		if !seenMember[m.Component] {
			seenMember[m.Component] = true
			ev.Components = append(ev.Components, m.Component)
		}
	}
	sort.Strings(ev.Components)
	sort.Slice(ev.Members, func(i, j int) bool { return ev.Members[i].Name < ev.Members[j].Name })

	seenDir := map[string]bool{}
	for _, match := range pkgDirRE.FindAllSubmatch(data, -1) {
		dir := string(match[1])
		if !seenDir[dir] {
			seenDir[dir] = true
			ev.PkgDirs = append(ev.PkgDirs, dir)
		}
	}
	sort.Strings(ev.PkgDirs)
	return ev, nil
}

// inspectArchives reads every archive in a dist directory, in the manifest's
// target order.
func inspectArchives(dist string, m *Manifest) ([]ArchiveEvidence, error) {
	out := make([]ArchiveEvidence, 0, len(m.Targets))
	for _, t := range m.Targets {
		ev, err := InspectArchive(filepath.Join(dist, m.ArchiveAssetName(t)))
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// arBaseName is the file name a member path ends with. Zig records the object's
// build-time directory in the member name, so the name that classifies an
// object is its last path element.
func arBaseName(name string) string {
	name = strings.TrimSuffix(name, "/")
	return path.Base(name)
}

// archiveMembers lists the object members of a static archive. It implements
// the ar format by hand — 60-byte headers, a `//` long-name table and `/N`
// references — because the alternative is an external `ar`, and this has to
// work wherever the repository does.
func archiveMembers(data []byte) ([]string, error) {
	const magic = "!<arch>\n"
	if !bytes.HasPrefix(data, []byte(magic)) {
		return nil, fmt.Errorf("not an ar archive (no %q signature)", strings.TrimRight(magic, "\n"))
	}
	var names []string
	var longNames []byte
	for off := len(magic); off+60 <= len(data); {
		header := data[off : off+60]
		if string(header[58:60]) != "`\n" {
			return nil, fmt.Errorf("malformed ar header at offset %d", off)
		}
		size, err := arDecimal(header[48:58])
		if err != nil {
			return nil, fmt.Errorf("member size at offset %d: %w", off, err)
		}
		body := off + 60
		if body+int(size) > len(data) {
			return nil, fmt.Errorf("member at offset %d claims %d bytes, past the end of the file", off, size)
		}
		name := strings.TrimRight(string(header[0:16]), " ")
		var resolved string
		switch {
		case name == "//":
			longNames = data[body : body+int(size)]
		case isArchiveSymbolTable(name):
			// A symbol table whose name fits the 16-byte field.
		case strings.HasPrefix(name, "/") && arDecimalOnly(name[1:]):
			index, _ := arDecimal([]byte(name[1:]))
			entry, err := arLongName(longNames, int(index))
			if err != nil {
				return nil, err
			}
			resolved = entry
		case strings.HasPrefix(name, "#1/"):
			// BSD-style extended name: the name is the first bytes of the body.
			n, err := arDecimal([]byte(name[3:]))
			if err != nil || int(n) > int(size) {
				return nil, fmt.Errorf("malformed BSD extended name at offset %d", off)
			}
			resolved = strings.TrimRight(string(data[body:body+int(n)]), "\x00")
		default:
			resolved = strings.TrimRight(name, "/")
		}
		if resolved != "" && !isArchiveSymbolTable(resolved) {
			names = append(names, resolved)
		}
		off = body + int(size)
		if size%2 == 1 {
			off++ // members are padded to an even offset
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("an ar archive with no members")
	}
	return names, nil
}

// isArchiveSymbolTable reports whether an ar member is the archive's own symbol
// table rather than a compiled object. The names are the ones the formats use:
// `/` and `/SYM64/` for GNU, and the `__.SYMDEF` family for Mach-O, which is
// what the pinned darwin archives actually carry (measured when the coverage
// test first read one).
func isArchiveSymbolTable(name string) bool {
	switch name {
	case "/", "/SYM64/", "__.SYMDEF", "__.SYMDEF SORTED", "__.SYMDEF_64", "__.SYMDEF_64 SORTED":
		return true
	}
	return false
}

// arLongName reads entry N out of the `//` string table: a name terminated by
// "/\n", or by "\n" alone when the name itself ends in "/".
func arLongName(table []byte, index int) (string, error) {
	if index < 0 || index >= len(table) {
		return "", fmt.Errorf("long name index %d is outside the name table", index)
	}
	rest := table[index:]
	end := bytes.IndexByte(rest, '\n')
	if end < 0 {
		return "", fmt.Errorf("long name at %d is not terminated", index)
	}
	return strings.TrimSuffix(string(rest[:end]), "/"), nil
}

// arDecimal parses the space-padded decimal fields of an ar header.
func arDecimal(field []byte) (int64, error) {
	text := strings.TrimSpace(string(field))
	if text == "" {
		return 0, nil
	}
	var n int64
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a decimal field", string(field))
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

func arDecimalOnly(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
