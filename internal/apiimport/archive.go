package apiimport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/pathname"
)

// ErrInvalidArchive identifies bytes that are not a readable ZIP archive.
// The transport maps this caller-supplied input refusal to JSON-RPC -32602.
var ErrInvalidArchive = errors.New("invalid Postman archive")

// ArchiveDocumentKind identifies the Postman document represented by an
// archive member. The manifest has the kind, while the document supplies its
// display name.
type ArchiveDocumentKind string

const (
	ArchiveCollection  ArchiveDocumentKind = "collection"
	ArchiveEnvironment ArchiveDocumentKind = "environment"
)

// ArchiveDocument is one existing Postman document extracted from an archive.
// Document retains the original JSON bytes so the caller can hand it to the
// ordinary single-document importer without inventing a second converter.
type ArchiveDocument struct {
	Kind     ArchiveDocumentKind
	Name     string
	Path     string
	Document []byte
}

// manifestName is the one member every Postman archive has, and the member
// whose position decides what every other path is relative to.
const manifestName = "archive.json"

type postmanArchiveManifest struct {
	Environment map[string]bool `json:"environment"`
	Collection  map[string]bool `json:"collection"`
}

// ReadPostmanArchive reads a Postman workspace export and returns its named
// documents in stable archive-path order. The archive is hostile input: both
// its encoded size and the bytes expanded from its members are bounded by the
// same MaxDocumentBytes used by the single-document importer.
func ReadPostmanArchive(r io.Reader) ([]ArchiveDocument, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("apiimport: read Postman archive: %w", err)
	}
	if len(raw) > MaxDocumentBytes {
		return nil, fmt.Errorf("apiimport: Postman archive is over the %d-byte limit", MaxDocumentBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("apiimport: read Postman archive: %w: %v", ErrInvalidArchive, err)
	}

	for _, member := range zr.File {
		if pathErr := validateArchiveMember(member.Name); pathErr != nil {
			return nil, pathErr
		}
	}
	prefix, err := archiveRoot(zr.File)
	if err != nil {
		return nil, err
	}

	// KEYED BY WHERE THE MANIFEST PUT THEM, not by where the ZIP did. Every
	// path from here down — the ones the manifest implies, the ones reported
	// back, the ones a refusal names — is relative to the export directory,
	// so the reader below is the same reader whether Postman wrapped the
	// export or somebody handed us its contents (nocx-bvxf2.4).
	members := make(map[string]*zip.File, len(zr.File))
	for _, member := range zr.File {
		if member.FileInfo().IsDir() {
			continue
		}
		rel, under := strings.CutPrefix(member.Name, prefix)
		if !under {
			return nil, fmt.Errorf("apiimport: archive member %q lies outside the export directory %q", member.Name, prefix)
		}
		if _, exists := members[rel]; exists {
			return nil, fmt.Errorf("apiimport: archive contains duplicate member %q", member.Name)
		}
		members[rel] = member
	}

	manifestMember := members[manifestName]
	remaining := int64(MaxDocumentBytes)
	manifestBytes, err := readArchiveMember(manifestMember, &remaining)
	if err != nil {
		return nil, err
	}
	var manifest postmanArchiveManifest
	dec := json.NewDecoder(bytes.NewReader(manifestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("apiimport: parse archive.json: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("apiimport: trailing data after archive.json")
	}

	expected := make(map[string]ArchiveDocumentKind)
	for id, listed := range manifest.Collection {
		if !listed {
			return nil, fmt.Errorf("apiimport: archive manifest collection %q is not true", id)
		}
		memberPath, err := archiveDocumentPath(ArchiveCollection, id)
		if err != nil {
			return nil, err
		}
		if _, exists := expected[memberPath]; exists {
			return nil, fmt.Errorf("apiimport: archive manifest names %q more than once", id)
		}
		expected[memberPath] = ArchiveCollection
	}
	for id, listed := range manifest.Environment {
		if !listed {
			return nil, fmt.Errorf("apiimport: archive manifest environment %q is not true", id)
		}
		memberPath, err := archiveDocumentPath(ArchiveEnvironment, id)
		if err != nil {
			return nil, err
		}
		if _, exists := expected[memberPath]; exists {
			return nil, fmt.Errorf("apiimport: archive manifest names %q more than once", id)
		}
		expected[memberPath] = ArchiveEnvironment
	}

	for memberPath := range expected {
		if _, ok := members[memberPath]; !ok {
			return nil, fmt.Errorf("apiimport: archive manifest names %s, but that member is missing", memberPath)
		}
	}
	for memberPath := range members {
		if memberPath == manifestName {
			continue
		}
		if _, ok := expected[memberPath]; !ok {
			return nil, fmt.Errorf("apiimport: archive member %q is not named by archive.json", memberPath)
		}
	}
	if len(expected) == 0 {
		return nil, errors.New("apiimport: Postman archive contains no documents")
	}

	paths := make([]string, 0, len(expected))
	for memberPath := range expected {
		paths = append(paths, memberPath)
	}
	sort.Strings(paths)
	documents := make([]ArchiveDocument, 0, len(paths))
	for _, memberPath := range paths {
		member := members[memberPath]
		contents, err := readArchiveMember(member, &remaining)
		if err != nil {
			return nil, err
		}
		converted, err := parsePostman(bytes.NewReader(contents), routeDirect())
		if err != nil {
			return nil, fmt.Errorf("apiimport: parse %s: %w", memberPath, err)
		}
		kind := expected[memberPath]
		name, ok := archiveDocumentName(contents, converted, kind)
		if !ok {
			return nil, fmt.Errorf("apiimport: %s is not a Postman %s document", memberPath, kind)
		}
		documents = append(documents, ArchiveDocument{
			Kind:     kind,
			Name:     name,
			Path:     memberPath,
			Document: contents,
		})
	}
	return documents, nil
}

// routeDirect keeps the archive reader independent from transport routes:
// archive members are documents, and route is applied when they are written.
func routeDirect() apicoll.Route { return apicoll.Route{Kind: apicoll.RouteDirect} }

func archiveDocumentPath(kind ArchiveDocumentKind, id string) (string, error) {
	if err := pathname.CheckComponent(id); err != nil {
		return "", fmt.Errorf("apiimport: archive %s id %q is invalid: %w", kind, id, err)
	}
	return string(kind) + "/" + id + ".json", nil
}

func archiveDocumentName(contents []byte, res postmanResult, kind ArchiveDocumentKind) (string, bool) {
	var source pmDoc
	if err := json.Unmarshal(contents, &source); err != nil {
		return "", false
	}
	switch kind {
	case ArchiveCollection:
		if source.Info == nil {
			return "", false
		}
		name := strings.TrimSpace(source.Info.Name.String())
		if name == "" {
			return "", false
		}
		return name, true
	case ArchiveEnvironment:
		name := strings.TrimSpace(source.Name.String())
		if source.Info != nil || source.Item != nil || source.Values == nil ||
			len(res.Environments) != 1 || len(res.Requests) != 0 || name == "" {
			return "", false
		}
		return name, true
	default:
		return "", false
	}
}

// archiveRoot answers WHERE THE MANIFEST LIES, and therefore what every other
// member is named relative to.
//
// A Postman export nests the whole export under one directory named for it —
// `<export-uuid>/archive.json`, measured on a real one in nocx-r1uk0 — while
// an archive assembled by hand has no such directory. They are one archive
// with a prefix of a different length, so the prefix is a value here rather
// than a second reader, and it is taken from the MANIFEST rather than from
// whatever the first member happened to be: an archive with no manifest is
// then refused for having no manifest, which is what is wrong with it.
//
// Depth one, and no deeper. `a/b/archive.json` is not an export directory
// with a manifest in it, it is an export directory holding something we
// cannot name, and treating `a/b/` as the root would silently accept an
// archive whose shape nobody has read.
func archiveRoot(files []*zip.File) (string, error) {
	prefixes := make([]string, 0, 1)
	for _, member := range files {
		if member.FileInfo().IsDir() {
			continue
		}
		switch name := member.Name; {
		case name == manifestName:
			prefixes = append(prefixes, "")
		case strings.HasSuffix(name, "/"+manifestName) && strings.Count(name, "/") == 1:
			prefixes = append(prefixes, strings.TrimSuffix(name, manifestName))
		}
	}
	switch len(prefixes) {
	case 0:
		return "", errors.New("apiimport: Postman archive has no archive.json manifest")
	case 1:
		return prefixes[0], nil
	default:
		return "", fmt.Errorf(
			"apiimport: Postman archive holds %d archive.json manifests, and only one export can be imported at a time",
			len(prefixes))
	}
}

func validateArchiveMember(name string) error {
	// A ZIP DIRECTORY ENTRY IS A NAME WITH A TRAILING SLASH and no contents.
	// It is skipped by the reader rather than refused: a real export carries
	// them, and refusing one here rejected the whole archive before anything
	// had looked at what it held. What is checked is the path it names.
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.HasPrefix(name, "/") || path.IsAbs(name) || path.Clean(name) != name {
		return fmt.Errorf("apiimport: refusing archive member path %q", name)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("apiimport: refusing archive member path %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return fmt.Errorf("apiimport: refusing archive member path %q", name)
		}
	}
	return nil
}

func readArchiveMember(member *zip.File, remaining *int64) ([]byte, error) {
	rc, err := member.Open()
	if err != nil {
		return nil, fmt.Errorf("apiimport: open archive member %q: %w", member.Name, err)
	}
	defer func() { _ = rc.Close() }()
	contents, err := io.ReadAll(io.LimitReader(rc, *remaining+1))
	if err != nil {
		return nil, fmt.Errorf("apiimport: read archive member %q: %w", member.Name, err)
	}
	if int64(len(contents)) > *remaining {
		return nil, fmt.Errorf("apiimport: expanded archive data exceeds the %d-byte limit", MaxDocumentBytes)
	}
	*remaining -= int64(len(contents))
	return contents, nil
}
