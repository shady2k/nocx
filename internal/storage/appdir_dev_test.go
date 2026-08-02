//go:build !release

package storage

import "testing"

// The guard this file exists for: a build that is not the shipped one must not
// resolve the shipped profile. It needs no flag, no environment variable and
// nothing for a test runner to remember, which is the point — the e2e isolation
// that already existed (XDG_CONFIG_HOME in e2e/harness.ts) was correct and was
// simply never applied to the default path, and no amount of care catches that
// class of mistake. A default that is safe does (nocx-ti8w).
//
// The direction matters as much as the split. `-tags release` selects the
// shipped profile; forgetting it yields the development one. The reverse would
// mean a forgotten tag silently writes the user's real profile, which is the
// failure being fixed.
func TestAppDirName_DevelopmentBuildDoesNotResolveTheShippedProfile(t *testing.T) {
	if AppDirName == "" {
		t.Fatal("AppDirName is empty: every build must name a profile directory")
	}
	if AppDirName == shippedAppDirName {
		t.Fatalf("a build without -tags release resolves the shipped profile %q; "+
			"the unsafe direction must be the one that needs the explicit tag", AppDirName)
	}
}

// NewAppPaths is what the composition root calls, so the guarantee has to hold
// through it and not only through the constant. Asserted separately because a
// correct AppDirName reached by nobody is precisely the shape of defect the two
// beads behind this change are about.
func TestNewAppPaths_ResolvesTheDevelopmentProfile(t *testing.T) {
	app, err := NewAppPaths()
	if err != nil {
		t.Fatalf("NewAppPaths(): %v", err)
	}
	shipped, err := newOSPaths(shippedAppDirName)
	if err != nil {
		t.Fatalf("newOSPaths(%q): %v", shippedAppDirName, err)
	}
	if app.ConfigDir() == shipped.ConfigDir() {
		t.Errorf("NewAppPaths() resolves the shipped config dir %s in a development build", app.ConfigDir())
	}
}

// The property a developer actually cares about: running the dev stand or the
// e2e suite cannot read or write the documents the installed app owns. Asserted
// on the resolved paths rather than on the name, because the name is only a
// means — what must differ is the directory the DocumentStore is pointed at.
func TestNewOSPaths_DevelopmentProfileIsDisjointFromTheShippedOne(t *testing.T) {
	dev, err := newOSPaths(AppDirName)
	if err != nil {
		t.Fatalf("newOSPaths(%q): %v", AppDirName, err)
	}
	shipped, err := newOSPaths(shippedAppDirName)
	if err != nil {
		t.Fatalf("newOSPaths(%q): %v", shippedAppDirName, err)
	}

	for _, c := range []struct {
		role       string
		dev, shipp string
	}{
		{"config", dev.ConfigDir(), shipped.ConfigDir()},
		{"data", dev.DataDir(), shipped.DataDir()},
		{"cache", dev.CacheDir(), shipped.CacheDir()},
	} {
		if c.dev == c.shipp {
			t.Errorf("%s dir is shared between the development and shipped profiles: %s", c.role, c.dev)
		}
	}
}
