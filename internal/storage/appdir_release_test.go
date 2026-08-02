//go:build release

package storage

import "testing"

// The other half of the split, and the reason `make test` runs the suite a
// second time under `-tags release`: without this the release side of a
// build-tagged constant is never compiled, let alone asserted, and a typo in
// appdir_release.go would ship an app whose profile directory is wrong for
// every existing installation.
func TestAppDirName_ShippedBuildResolvesTheShippedProfile(t *testing.T) {
	if AppDirName != shippedAppDirName {
		t.Fatalf("a build with -tags release must resolve %q, got %q", shippedAppDirName, AppDirName)
	}
}
