package app

import (
	"path/filepath"
	"testing"
)

// newTestApp builds the composition root the way a test must build it: with
// the OS keystore out of reach, and the backend log inside the test's own
// temporary directory.
//
// It exists because saying nothing used to mean "reach the developer's login
// keychain". app.New probes the vault's system provider at every start, and a
// probe is a real keyring.Set — on macOS that is a modal dialog belonging to
// the OS rather than to the suite, once per backend start, continuously while
// a suite runs (nocx-o4hg). $HOME isolation cannot cover it: go-keyring talks
// to the Keychain service, not to a directory, so a per-user OS service is a
// class of state the storagetest boundary can never move.
//
// The defaults go first and the caller's options after, so any of them can be
// overridden: a test that pins its own log path passes WithLogFilePath, and
// one that genuinely needs the real store passes WithRealSystemKeystore with
// its reason.
//
// Profile isolation is deliberately NOT done here. storagetest.Isolate owns
// that boundary and a second owner would be a second truth (AD-8); a test
// that forgets it is already refused by storage.NewAppPaths, by name.
//
// It lives in a _test.go file rather than in a package of its own, unlike
// storagetest and vaulttest. Those serve other packages; every caller of this
// one is inside internal/app, whose test files are in package app, so an
// apptest package importing app and imported back would be an import cycle.
// What makes the default safe is not this helper anyway — it is the refusal
// in New, which catches a construction that never came through here.
func newTestApp(t *testing.T, opts ...Option) (*App, error) {
	t.Helper()
	defaults := []Option{
		withoutSystemKeystore(),
		WithLogFilePath(filepath.Join(t.TempDir(), "nocx.log")),
	}
	return New(append(defaults, opts...)...)
}

// withoutSystemKeystore builds the vault's system provider over a keyring
// that fails every operation, so the app behaves exactly like one on a host
// with no OS secret store — which is the stance nearly every test wants.
//
// IN THE TEST BINARY, not beside WithRealSystemKeystore in app.go, because
// nothing in production says it any more. It used to be exported for
// cmd/devharness, which said it by environment variable; devharness is gone
// with the cutover (design D11) and the stance it stated is now the BUILD's
// (keystore_build_headless.go), so a build with no login session declares the
// same thing without anybody passing anything. What is left is the test's own
// need to declare a stance at all — decideKeystore refuses an undeclared one
// under `go test` — and a declaration only tests make belongs where only
// tests can reach it.
func withoutSystemKeystore() Option {
	return func(o *optionSet) { o.keystore = keystoreAbsent }
}
