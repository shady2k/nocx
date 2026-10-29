package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacOSTemplateAgreesWithReleaseIdentity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "build", "darwin", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "<key>CFBundleIdentifier</key>"
	_, after, ok := strings.Cut(string(raw), key)
	if !ok || !strings.HasPrefix(strings.TrimSpace(after), "<string>dev.nocx</string>") {
		t.Error("Info.plist must agree with the release identity dev.nocx (ADR-0103)")
	}
	if _, err := os.Stat(filepath.Join("..", "..", "build", "darwin", "Info.dev.plist")); !os.IsNotExist(err) {
		t.Error("unread Info.dev.plist must not declare a competing identity")
	}
}

// Execute the release's actual shell check. Only the platform tool is replaced:
// its arguments still have to read the identifier from the assembled app, and
// the shell must refuse both an old identity and a failed plist read.
func TestReleaseRejectsWrongAssembledBundleIdentifier(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release-build.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const step = "      - name: Verify macOS bundle identifier\n        run: |\n"
	_, rest, ok := strings.Cut(string(raw), step)
	if !ok {
		t.Fatal("release has no assembled bundle identifier check")
	}
	var script strings.Builder
	for _, line := range strings.Split(rest, "\n") {
		if line != "" && !strings.HasPrefix(line, "          ") {
			break
		}
		script.WriteString(strings.TrimPrefix(line, "          "))
		script.WriteByte('\n')
	}
	if !strings.Contains(script.String(), "/usr/libexec/PlistBuddy") {
		t.Fatal("release must read the assembled plist with PlistBuddy")
	}
	dir := t.TempDir()
	buddy := filepath.Join(dir, "PlistBuddy")
	stub := `#!/bin/sh
set -eu
[ "$1" = -c ]
[ "$2" = 'Print :CFBundleIdentifier' ]
[ "$3" = 'build/bin/nocx.app/Contents/Info.plist' ]
[ "$TEST_BUNDLE_ID" != unreadable ] || exit 1
printf '%s\n' "$TEST_BUNDLE_ID"
`
	if err := os.WriteFile(buddy, []byte(stub), 0o700); err != nil { //nolint:gosec // executable fixture in this test's private temporary directory
		t.Fatal(err)
	}
	check := strings.ReplaceAll(script.String(), "/usr/libexec/PlistBuddy", buddy)
	for _, id := range []string{"dev.nocx", "dev.nocx-dev", "com.shady2k.nocx", "io.github.shady2k.nocx", "", "unreadable"} {
		t.Run(id, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", check) //nolint:gosec // execute this repository's release check, with its platform reader replaced by a private fixture
			cmd.Env = append(os.Environ(), "TEST_BUNDLE_ID="+id)
			output, err := cmd.CombinedOutput()
			if id == "dev.nocx" && err != nil {
				t.Fatalf("correct identity refused: %v: %s", err, output)
			}
			if id != "dev.nocx" && err == nil {
				t.Fatalf("release accepted wrong/unreadable identity %q", id)
			}
		})
	}
}
