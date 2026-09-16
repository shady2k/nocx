// Package storagetest isolates the profile directories for a test.
//
// It is the other half of the refusal in [storage.NewAppPaths]: under test that
// function resolves nothing until a root is named here, so isolation is not
// something a test author can forget — only something they have not done yet,
// and the error says so.
//
// It lives in its own package rather than as an exported helper on storage so
// that nothing outside a test can reach it. A production binary that imported
// this would not compile against anything useful; there is no exported way to
// name a root from ordinary code.
package storagetest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// Isolate points this test's profile directories at a temporary root and
// returns it. Every role — config, data and cache — moves together and stays
// distinct, and the root is this test's own, so two tests in one run cannot
// meet in it.
//
// The root is removed by t.TempDir's own cleanup, and the environment is
// restored by t.Setenv's, which is also why a test that calls this cannot call
// t.Parallel: the setting is process-wide for as long as it is in force.
//
// It does NOT move HOME — see [IsolateWithHome] for the tests that need that
// and for why it is not what everybody gets.
//
// And neither of them moves a per-user OS SERVICE. Both of these are
// directories; the OS keystore is reached through the Keychain service or the
// session bus, so no environment variable this package sets can keep a test
// off the developer's real login keychain (nocx-o4hg). That one is declared
// instead of isolated: app.New refuses to build for a test that has not said
// whether it may reach it.
func Isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(storage.TestAppDirEnv, root)
	return root
}

// IsolateWithHome is [Isolate] plus a disposable HOME, for a test that spawns
// a real shell. It returns the home directory, not the profile root.
//
// The profile directories are only half the developer's machine. A shell
// reads rc files, and rc files are not inert: on this repo's owner ~/.bashrc
// loads a second prompt integration that wraps PROMPT_COMMAND and emits its
// own OSC 133, so TestLocalEnhancedSessionEstablishesThroughProductionWiring
// never reached prompt_ready on that machine while passing on a runner whose
// home is empty (nocx-58gq). Neither side was lying; they were running
// different things, and a gate nobody can reproduce is not a gate. This is
// the boundary the e2e suite draws with NOCX_E2E_HOME_DIR, drawn for the Go
// suite.
//
// Deliberately NOT folded into Isolate, though every test would be more
// hermetic for it. On macOS the login keychain is found through the home
// directory, so a fake HOME leaves `security` with no keychain to open and
// every call to the system vault provider waits out its 5-second bound —
// vault.setup went from 132 ms to 5.1 s and TestCapture_SaveNowAndSaveLater-
// OverTheRealSocket was refused by its own domain gate. Isolating HOME buys
// determinism for shells and loses it for the keystore, so the test that
// spawns a shell asks for it by name.
//
// A test that must prove something about a user's rc files writes them into
// this root; it may never read the ones the machine running the suite has.
// The home directory is NOT a t.TempDir. A shell writes on its way out —
// bash appends ~/.bash_history from its EXIT trap — and t.TempDir's cleanup
// FAILS THE TEST when a file appears while it is removing the tree. That is a
// race between a dying process and a directory walk, which would land as a
// flake with a message about the filesystem rather than about the test. Its
// own directory, removed on a best-effort basis, keeps the straggler a
// straggler.
func IsolateWithHome(t *testing.T) string {
	t.Helper()
	Isolate(t)
	home, err := os.MkdirTemp(disposableRoot(), "nocx-home-")
	if err != nil {
		t.Fatalf("create the disposable home: %v", err)
	}
	t.Cleanup(func() { removeUnderTempDir(t, home) })
	t.Setenv("HOME", home)
	return home
}

// disposableRoot is where a disposable home is made, and the only root
// removeUnderTempDir will delete under.
//
// On macOS it is /tmp, not os.TempDir(). TMPDIR there is
// /var/folders/<two random components>/T/, 49 bytes before the home's own
// name, and a home is where this machine's helper binds its endpoint socket
// (<home>/.nocx/run/<generation>.sock). That came to 108-109 bytes against
// darwin's 103-byte limit (endpoint.maxSocketPath), so every internal/app test
// that opens a pane was refused on the macOS runner with ErrPathTooLong, and
// passed on Linux, whose TMPDIR is /tmp. A real user's home is short; only
// the test's was not. nocx-lvdj3 met the same limit for the coordinator's
// socket and shortened a prefix; a prefix still leaves the home at the mercy
// of the runner's TMPDIR, so the root is chosen instead.
func disposableRoot() string {
	if runtime.GOOS == "darwin" {
		return "/tmp"
	}
	return os.TempDir()
}

// removeUnderTempDir deletes a tree only after proving it is inside the
// disposable root (disposableRoot), and fails the test loudly rather than deleting
// anything else.
//
// The check is here because of what this helper is for: it hands a test a
// path and then sets HOME to it, so from that moment every piece of code
// under test resolves "the user's home" to this string. A helper that both
// chooses a path and later removes it recursively is one refactor away from
// removing a real home directory, and the failure mode is not a red test —
// it is a developer's files. Cheap to check, catastrophic to skip.
//
// Symlinks are resolved on both sides before comparing: a temporary root
// reached through a symlink is the normal case on macOS, where /tmp is a link
// to /private/tmp, and comparing the unresolved strings would reject every
// legitimate path on the platform this ships to first.
func removeUnderTempDir(t *testing.T, dir string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(disposableRoot())
	if err != nil {
		t.Errorf("resolve the temporary root, so %q was NOT removed: %v", dir, err)
		return
	}
	target, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// Already gone is not a failure; anything else leaves it in place.
		if !os.IsNotExist(err) {
			t.Errorf("resolve %q, so it was NOT removed: %v", dir, err)
		}
		return
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("refusing to remove %q: it resolves to %q, which is outside the temporary root %q",
			dir, target, root)
		return
	}
	if err := os.RemoveAll(target); err != nil {
		t.Errorf("remove the disposable home %q: %v", target, err)
	}
}
