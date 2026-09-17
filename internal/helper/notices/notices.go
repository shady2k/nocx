// Package notices carries the third-party licence notices the helper ships,
// so a helper installed on a host nobody here controls carries them too.
//
// The document is the PIN's own THIRD_PARTY_LICENSES: generated from the
// libghostty-vt archives it licenses, verified against the sha256 in
// MANIFEST.json by `make vt-archives`, and staged here by
// third_party/libghostty-vt/scripts/stage-notices.sh — the one script that
// owns where the notices come from. Nothing in this package composes or edits
// the text; the bytes are embedded verbatim and `nocx-helper --licenses`
// prints them.
//
// The embed names the DIRECTORY, with a committed .gitignore inside it, for
// the reason internal/helper/deploy/artifacts/bin/.gitignore states: a pattern
// naming a file that is not there fails the compile, and this tree has to
// type-check before anything has fetched the pin. So a checkout without the
// document compiles, and Document reports ErrNotBuilt — a named recovery —
// rather than a binary whose notices are silently somebody else's.
package notices

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
)

// FileName is the name the document is staged and embedded under: the
// generated asset's own title, so a reader who finds it in a bundle, in an
// AppDir, or in this output recognises what it is.
const FileName = "THIRD_PARTY_LICENSES.txt"

//go:embed all:licenses
var licensesFS embed.FS

// ErrNotBuilt is returned when no notices document is in the embed directory,
// which is what a build that ran without `make vt-archives` produces: the
// fetch that verifies the pin is also the step that stages the document.
var ErrNotBuilt = errors.New("no third-party notices are embedded (run make vt-archives)")

// Document returns the pinned third-party licence notices: every licence that
// applies to the components inside the pinned libghostty-vt archives, which
// this binary links statically.
func Document() ([]byte, error) {
	data, err := fs.ReadFile(licensesFS, "licenses/"+FileName)
	if err != nil {
		return nil, fmt.Errorf("notices: %w: %v", ErrNotBuilt, err)
	}
	return data, nil
}
