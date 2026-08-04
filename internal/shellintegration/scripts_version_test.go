package shellintegration

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The scripts are installed into ~/.nocx once and then never rewritten while
// the installed VERSION file still matches the `version` constant — that
// short-circuit is the whole point of the marker, and it is checked before
// anything reads the script's content. So a script edited WITHOUT bumping
// `version` reaches nobody: every existing shell keeps sourcing the copy
// installed the last time the number changed, and the change is invisible in
// the product while every test that reads the embedded string stays green.
//
// Measured on 2026-08-01: the OSC 636 command-existence hook shipped, the
// backend binary carried the new script, `go test ./internal/shellintegration`
// passed, and the feature did nothing in the app — ~/.nocx/shell-integration.bash
// was still the 6504-byte copy from 2026-07-26 because `version` was left at
// "7". The defect is structural: nothing connected the script bytes to the
// number that governs whether they are delivered.
//
// This test connects them. Change a script and the digest below stops matching,
// which is a failure that can only be resolved by deciding what the version
// should be — which is exactly the decision that was skipped.
func TestScriptVersionTracksScriptContent(t *testing.T) {
	// sha256 over each script's name and bytes, in a fixed order.
	//
	// WHEN THIS FAILS: you changed a shell integration script. Bump `version`
	// in scripts.go, then add an entry here for the new version with the digest
	// the failure prints. Do not edit an existing entry — a released version
	// number describes one exact pair of scripts, and rewriting it in place is
	// the same as not having the check.
	digests := map[string]string{
		"8":  "ca89bf20e58c0a4669ecfb0754173ce721e436273b0b06549c7e0162e9b06dc8",
		"9":  "26ee0a75cf83df3a773c97ee39265c96912629c4bcdb629edea51ba5bcc5529d",
		"10": "2f8035f9e87404c0079560663ee931cceb1920d151073502f692aa6d3d22fdff",
	}

	h := sha256.New()
	for _, name := range []string{"scripts/nocx.bash", "scripts/nocx.zsh"} {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(scriptFor(t, name)))
		h.Write([]byte{0})
	}
	got := hex.EncodeToString(h.Sum(nil))

	want, ok := digests[version]
	if !ok {
		t.Fatalf("version %q has no recorded script digest.\n"+
			"Add this entry to the digests map above:\n\n\t%q: %q,\n",
			version, version, got)
	}
	if got != want {
		t.Fatalf("the shell integration scripts changed but version is still %q.\n"+
			"An installed ~/.nocx carrying VERSION=%s will never be rewritten, so the\n"+
			"change reaches no shell. Bump `version` in scripts.go and add:\n\n\t%q: %q,\n",
			version, version, "<new version>", got)
	}
}

// scriptFor returns the embedded content for a script path, so the test hashes
// exactly the bytes that get installed rather than re-reading the file from
// disk (which would pass even if the go:embed directive pointed elsewhere).
func scriptFor(t *testing.T, path string) string {
	t.Helper()
	switch path {
	case "scripts/nocx.bash":
		return bashScript
	case "scripts/nocx.zsh":
		return zshScript
	default:
		t.Fatalf("no embedded script for %q", path)
		return ""
	}
}
