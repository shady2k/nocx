package app

import (
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
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
// The local helper install is dropped for the same class of reason, and it is
// the same class of state the keystore is NOT: it is a directory, so it would
// be isolatable — but storagetest.Isolate deliberately leaves HOME where it
// is, and ~/.nocx/helper is resolved from HOME. So a test that called Start
// would write four megabytes into the developer's real home, once per
// generation, with nothing to remove it (nothing retires a generation yet).
// A test that wants the install exercised says so — withLocalHelperArtifacts
// below, and local_helper_install_test.go is what says it.
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
		withoutLocalHelperInstall(),
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

// withoutLocalHelperInstall drops the local helper install from Start.
//
// In the TEST BINARY for the same reason withoutSystemKeystore is: nothing in
// production says it, and a production Option nobody calls is dead code the
// ratchet is right to report. A shipped binary installs the generation it is
// going to reach for; a composition root that opted out would be a product
// with no local helper and no way to say so.
func withoutLocalHelperInstall() Option {
	return func(o *optionSet) { o.noLocalHelper = true }
}

// withLocalHelperArtifacts puts the local install back and points it at bytes
// the caller owns. It is what "a test that wants the install exercised says so"
// means in code: newTestApp's defaults run first and the caller's options
// after, so this clears the blanket opt-out above.
//
// The artifact is the caller's rather than the embedded one for two reasons,
// and neither is convenience. `make helpers` has not necessarily run — the
// embedded source answers ErrArtifactsNotBuilt on a fresh checkout, and a
// wiring test that passes only on a machine that built the helpers is not a
// gate. And the wiring is what is under test, not the artifact: a few bytes
// prove the same call reached the same installer as four megabytes do.
func withLocalHelperArtifacts(src deploy.ArtifactSource) Option {
	return func(o *optionSet) {
		o.noLocalHelper = false
		o.helperArtifacts = src
	}
}
