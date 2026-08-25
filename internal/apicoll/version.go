package apicoll

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/storage"
)

// ManifestName is the file that makes a folder a collection. It sits at the
// root; requests are the other JSON files, and `environments/` sits beside
// them (§6.1, §6.2).
const ManifestName = "nocx-collection.json"

// Module declares the persisted format's own monotonic schema version
// (ADR-0011 §6). One version, no migrations: the format is new, and a chain
// grows when a format changes rather than in anticipation.
//
// This is NOT the same boundary as a JSON Schema under `contracts/`. A
// contract makes an RPC result exact on the wire; it provides no migrations,
// no refusal of a document from a newer build, and no answer at all for a
// file that has been sitting on disk across three releases. A collection
// folder is shared through git and may reach this build from a newer one, so
// it needs the document protocol as well, not instead.
var Module = storage.Module{Name: "apicoll", Current: 1}

// manifest is the root document. It carries the name and the version and
// nothing else: the list of requests is the folder, not a field, so two
// people adding two requests conflict in two files rather than in one (§6.2).
type manifest struct {
	SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	Name          string                `json:"name"`
}

// newManifest is the ONE place a manifest is built, so the version a file
// is written at cannot drift from the version readManifest accepts.
func newManifest(name string) manifest {
	return manifest{SchemaVersion: Module.Current, Name: name}
}

// MarshalManifest renders the manifest for a collection called name, in the
// form readManifest reads it: this build's schemaVersion, the name, and
// nothing else.
//
// It exists because a collection folder is not always assembled by this
// package. internal/apiimport builds a whole folder in a staging directory
// and moves it into place as ONE atomic arrival (design §12.2), so it cannot
// hand the manifest to a storage.DocumentStore, which writes one document,
// in place, now — the manifest has to be one of the bytes-and-a-path files
// that arrive with the rename. What the importer can do, and what this is,
// is ask the reader for those bytes instead of spelling the format a second
// time. The field names, the "name and the version and nothing else" rule
// and the version itself stay here, next to the code that reads them back.
//
// The bytes are the document store's own: json.MarshalIndent with two
// spaces and no trailing newline, so a manifest is the same file whoever
// wrote it — NewDefaultCollection through the store, or an importer through
// this. Indented because a collection is shared through a pull request
// (§6.1) and a one-line file has no diff worth reading.
func MarshalManifest(name string) ([]byte, error) {
	raw, err := json.MarshalIndent(newManifest(name), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("apicoll: marshal manifest for %q: %w", name, err)
	}
	return raw, nil
}

// readManifest applies the document protocol in the order the protocol
// requires: probe the version, refuse anything newer than this build, and
// only then decode. The order is the point — a manifest from a newer build
// may contain fields whose types this build does not know, so decoding first
// would report a type error for what is really "this file is from the
// future", and the message a user needs would be lost.
//
// The manifest is read without following a symlink, for the same reason a
// request file is (§13.1): the file that decides whether a folder opens at
// all is the first one an attacker would point elsewhere.
func readManifest(root string) (manifest, error) {
	p := filepath.Join(root, ManifestName)

	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return manifest{}, fmt.Errorf("%w: %s", ErrNoManifest, p)
	case err != nil:
		return manifest{}, fmt.Errorf("apicoll: stat manifest %s: %w", p, err)
	case fi.Mode()&os.ModeSymlink != 0:
		return manifest{}, fmt.Errorf("%w: the manifest %s is a symlink, which is not followed", ErrPathOutsideCollection, p)
	case !fi.Mode().IsRegular():
		return manifest{}, fmt.Errorf("apicoll: manifest %s is not a regular file", p)
	}

	raw, err := os.ReadFile(p) //nolint:gosec // p is root/ManifestName and root is resolved
	if err != nil {
		return manifest{}, fmt.Errorf("apicoll: read manifest %s: %w", p, err)
	}

	var probe struct {
		SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	}
	if err = json.Unmarshal(raw, &probe); err != nil {
		return manifest{}, fmt.Errorf("apicoll: manifest %s is not a JSON object: %w", p, err)
	}

	migrated, err := Module.Migrate(raw, probe.SchemaVersion)
	if err != nil {
		return manifest{}, fmt.Errorf("apicoll: manifest %s: %w", p, err)
	}

	var m manifest
	if err = decodeStrict(migrated, &m); err != nil {
		return manifest{}, fmt.Errorf("apicoll: manifest %s: %w", p, err)
	}
	return m, nil
}

// decodeStrict rejects a field the format does not declare. That is safe
// precisely because the manifest owns the version: a file from a newer build
// is refused by readManifest before any request file is opened, so an unknown
// field here can only be a typo — and a typo silently ignored is a header the
// user believes they set.
func decodeStrict(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	// Trailing content is not a second document; it is a broken one.
	if dec.More() {
		return errors.New("trailing content after the JSON document")
	}
	return nil
}
