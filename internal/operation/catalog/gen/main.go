// Command gen projects the declaration table into the two generated
// artifacts: the typed handles internal/transport registers with, and
// contracts/operations.json, which the schema validates and the renderer's
// deadline table is generated from.
//
// Run from the repository root: go run ./internal/operation/catalog/gen
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	missing, err := catalog.CheckHandleReferences(root)
	if err != nil {
		fail(err)
	}
	if len(missing) != 0 {
		fail(fmt.Errorf("%d declaration(s) have no production reference: %v", len(missing), missing))
	}
	if writeErr := os.WriteFile(filepath.Join(root, "internal/operation/catalog/handles_gen.go"), catalog.HandleSource(), 0o644); writeErr != nil { //nolint:gosec // generated Go source, 0644 is the committed mode
		fail(writeErr)
	}
	manifest, err := catalog.ManifestJSON()
	if err != nil {
		fail(err)
	}
	if writeErr := os.WriteFile(filepath.Join(root, "contracts/operations.json"), manifest, 0o644); writeErr != nil { //nolint:gosec // generated JSON manifest, 0644 is the committed mode
		fail(writeErr)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen:", err)
	os.Exit(1)
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
