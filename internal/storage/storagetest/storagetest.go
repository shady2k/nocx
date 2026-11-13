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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
//
// THIS HOME IS NOT A t.TempDir BECAUSE A SHELL WRITES ON ITS WAY OUT, and
// that is a race rather than a tidiness. A pane's shell is forked by this
// machine's helper with HOME set to this directory, so a shell that is still
// dying while the removal walks the tree lands one more file in it — bash
// appends ~/.bash_history from its exit path, and zsh's launcher defaults
// HISTFILE to ${HOME}/.zsh_history and saves at exit
// (internal/shellintegration/launcher_zsh.go). t.TempDir's cleanup fails the
// test when that happens, with a message about the filesystem, and its path
// carries the TEST's name rather than the straggler's.
//
// So the home has its own owner — [removeUnderTempDir] — and that owner does
// two things t.TempDir's cleanup does not (nocx-xn63t.6.54). It still FAILS
// the test, because a leftover is a real leak rather than weather, and it
// NAMES WHAT WAS LEFT: the paths, and the processes still holding them. A test
// that owns its home is also the only one that can say who was writing in it.
func IsolateWithHome(t *testing.T) string {
	t.Helper()
	Isolate(t)
	home, err := os.MkdirTemp(DisposableRoot(), "nocx-home-")
	if err != nil {
		t.Fatalf("create the disposable home: %v", err)
	}
	t.Cleanup(func() { removeUnderTempDir(t, home) })
	SetHome(t, home)
	return home
}

// SetHome points this test's HOME at home and keeps the go command's own
// directories where the caller had them: GOPATH, GOMODCACHE and GOCACHE.
//
// A test home is a directory the test owns. A module cache is not, and by
// default the two are the same place: `go build` under a moved HOME writes
// GOPATH=$HOME/go and GOMODCACHE=$HOME/go/pkg/mod into the test's home, and
// the go command makes every file in a module cache read-only — so the
// removal cannot unlink them (`unlinkat ...: permission denied`) and a test
// fails over a cache it never asked for. CI took that as a cleanup failure of
// a live-ssh test that builds the helper (nocx-oalo1.3.2).
//
// The three are ASKED OF THE go COMMAND rather than derived here, and once:
// each has a default that depends on HOME and on the OS — GOCACHE is
// ~/Library/Caches/go-build on macOS and ~/.cache/go-build elsewhere — so a
// copy of those rules in this package would drift from the toolchain. Asked
// lazily, so a test binary that never moves HOME pays nothing, and before
// HOME moves, which is what makes the answer the caller's.
func SetHome(t *testing.T, home string) {
	t.Helper()
	dirs, err := goToolchainDirs()
	if err != nil {
		t.Fatalf("resolve the go command's directories, so this test's home %q cannot hold a module cache: %v", home, err)
	}
	for name, dir := range dirs {
		t.Setenv(name, dir)
	}
	t.Setenv("HOME", home)
}

// goToolchainDirs answers GOPATH, GOMODCACHE and GOCACHE by asking the go
// command itself, once per test binary.
var goToolchainDirs = sync.OnceValues(func() (map[string]string, error) {
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		return nil, fmt.Errorf("go env GOPATH GOMODCACHE GOCACHE: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 3 {
		return nil, fmt.Errorf("go env answered %d lines for three directories: %q", len(lines), out)
	}
	return map[string]string{
		"GOPATH":     lines[0],
		"GOMODCACHE": lines[1],
		"GOCACHE":    lines[2],
	}, nil
})

// SocketDir is a short directory under the same disposable root as
// [IsolateWithHome], for a test that binds a unix socket directly — a
// coordinator discovery socket, a worker tool endpoint, a bare helper
// endpoint dialled by generation — and has no profile layout of its own to
// isolate.
//
// t.TempDir() embeds the test's own name, and on macOS a TMPDIR of
// /var/folders/<two random components>/T/ sits ahead of that (49 bytes
// before the test's own name), so this had already grown three near-copies
// of "make the directory short" before this one: internal/coordinator's
// shortTempDir and shortDir, and internal/app's shortWorkerSocketDir. Each
// shortened only the directory's own prefix — os.MkdirTemp("", "nocxbind"),
// its siblings — which still resolves under TMPDIR and so still loses
// whatever TMPDIR grows to next; it happened to leave enough room on this
// runner and is exactly the fragility DisposableRoot was chosen over for
// IsolateWithHome. This is the one answer, sharing that already-fixed root
// instead of shortening a second, independent prefix under the same
// vulnerable parent.
func SocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(DisposableRoot(), "nocx-sock-")
	if err != nil {
		t.Fatalf("create a short socket directory: %v", err)
	}
	t.Cleanup(func() { removeUnderTempDir(t, dir) })
	return dir
}

// DisposableRoot is where a disposable home and a socket directory are made,
// the only root removeUnderTempDir will delete under, and the answer a fixture
// checks before writing into what it believes is a disposable home.
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
func DisposableRoot() string {
	if runtime.GOOS == "darwin" {
		return "/tmp"
	}
	return os.TempDir()
}

// removeUnderTempDir deletes a tree only after proving it is inside the
// disposable root (DisposableRoot), and fails the test loudly rather than deleting
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
	root, err := filepath.EvalSymlinks(DisposableRoot())
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
	if err := removeAll(target); err != nil {
		// The message is an ARGUMENT and not the format: it carries paths and
		// command lines, either of which may hold a percent verb.
		t.Errorf("%s", removalFailure(dir, target, err))
	}
}

// removeAll is [os.RemoveAll] behind a variable so that a test can reach the
// failure a CI run actually took — a file appearing in the walk's own window —
// without racing a real walk for it (nocx-xn63t.6.54;
// storagetest_removal_test.go). That race is about a millisecond wide on this
// machine (measured: 0.88-1.21 ms to remove a home holding a 14 MB installed
// helper), so a test that aimed at the real one would be a flake itself.
//
// A variable is enough because this package is a TEST HELPER and nothing but
// tests import it: storagetest's own doc says the same thing about why it is a
// separate package at all. A test that swaps it must not run in parallel with
// another, which Isolate's own doc already refuses for every test in this
// package: t.Setenv is process-wide.
var removeAll = os.RemoveAll

// leftoversMax and holdersMax bound the failure message: a home filled with a
// thousand files must fail with a message somebody can read, not with a wall
// of paths.
const (
	leftoversMax = 20
	holdersMax   = 10
)

// removalFailure is the whole of what a failed removal reports: the error, the
// paths it could not take, and the processes that were holding them.
//
// IT IS HANDED BOTH SPELLINGS OF THE HOME, and that is not decoration. `dir` is
// the path the caller named and `target` is what it resolves to, which are the
// same string on this machine and different on macOS, where a temporary
// directory is created through /tmp and removed through /private/tmp. A
// process's own environment carries whichever spelling its HOME was set to —
// the /tmp one — while the removal works on the resolved one, so a search that
// knew only one of them would report "no process is holding it" about the
// process that had just written there.
func removalFailure(dir, target string, err error) string {
	return fmt.Sprintf("remove the disposable home %q: %v\n%s", target, err, whatWasLeft(target, dir))
}

// whatWasLeft is the second half of a removal failure: every path still under
// dir, and every process on this machine that holds one of them. alsoNamed is
// the other spelling the home is reached by (see removalFailure).
//
// IT IS THE DIFFERENCE BETWEEN A RERUN AND ONE LINE OF LOG. On 2026-10-18 CI
// (run 39157466971, job ci-mac) failed with
//
//	remove the disposable home "/private/tmp/nocx-home-783600994": unlinkat
//	/private/tmp/nocx-home-783600994: directory not empty
//
// which names the home and nothing else: not the file that had just appeared
// in it, not the process that wrote it. Nor can it say what the failure MEANS,
// which is the part a reader needs: os.RemoveAll reads the directory's
// entries, deletes them, and only then removes the directory itself, so
// "directory not empty" says something was CREATED in the window between the
// read and the rmdir — and the only thing that can answer "what, and who" is
// the state the failure left behind.
func whatWasLeft(dir, alsoNamed string) string {
	var b strings.Builder
	left := leftoversUnder(dir)
	b.WriteString("what was left under it:")
	if len(left) == 0 {
		b.WriteString("\n\tnothing — it is empty now, so whatever the walk tripped over\n\tappeared while it ran and was gone again by the time it failed")
	}
	for _, l := range left {
		b.WriteString("\n\t" + l)
	}
	holding, complete := holdersOf(dir, alsoNamed)
	b.WriteString("\nthe processes still holding it:")
	switch {
	case len(holding) == 0 && complete:
		b.WriteString("\n\tnone: nothing on this machine names it in its command line, its working directory, its executable or its environment")
	case len(holding) == 0:
		b.WriteString("\n\tnone named it in its command line, and this platform cannot show another process's working directory or environment, so a holder that is invisible here is not ruled out")
	}
	for _, h := range holding {
		b.WriteString("\n\t" + h)
	}
	if len(holding) > 0 && !complete {
		b.WriteString("\n\t(this platform lists command lines only; a process holding it through its\n\tworking directory or environment alone is not listed)")
	}
	return b.String()
}

// leftoversUnder lists what is still under dir, one line per entry, bounded by
// leftoversMax.
//
// It is a walk rather than a readdir because the failure has two shapes: a
// directory that could not be emptied (its own entry is the answer) and the
// entries inside one that could not be removed. The root itself is not listed;
// the caller's message already names it.
func leftoversUnder(dir string) []string {
	var lines []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if path == dir {
			return nil
		}
		if len(lines) >= leftoversMax {
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		switch {
		case err != nil:
			lines = append(lines, fmt.Sprintf("%s (could not even be read: %v)", rel, err))
		case info.IsDir():
			lines = append(lines, fmt.Sprintf("%s (directory, modified %s)", rel, info.ModTime().Format(time.RFC3339Nano)))
		default:
			lines = append(lines, fmt.Sprintf("%s (file, %d bytes, written %s)", rel, info.Size(), info.ModTime().Format(time.RFC3339Nano)))
		}
		return nil
	})
	return lines
}

// namesAny reports whether text names the home in any of the spellings it is
// reached by. See removalFailure for why there is more than one.
func namesAny(text string, spellings ...string) bool {
	for _, s := range spellings {
		if s != "" && strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// parsePSHolders reads `ps -A -o pid=,ppid=,command=` output and names every
// process whose command line names the home.
//
// It is here rather than beside the one caller that needs it — holdersOf on a
// platform without /proc — so that the parse is exercised where it does NOT
// run: a failure path nobody can test on the machine it runs on is a failure
// path nobody reads.
//
// THE COLUMNS ARE PADDED, so the two leading fields are cut off by their
// separators rather than read by position: ps pads pid and ppid to their widest
// value in the listing, and a parser that split on a single space would read a
// run of blanks as an empty field and then hand the ppid to the command.
func parsePSHolders(out []byte, spellings ...string) []string {
	var lines []string
	for _, raw := range strings.Split(string(out), "\n") {
		pid, rest, ok := cutPSField(raw)
		if !ok {
			continue
		}
		ppid, command, ok := cutPSField(rest)
		if !ok || command == "" || !namesAny(command, spellings...) {
			// A line without three fields is ps's own header or a truncated
			// read, not a process: skipping it says nothing either way.
			continue
		}
		lines = append(lines, fmt.Sprintf("pid %s (ppid %s): %s", pid, ppid, command))
		if len(lines) >= holdersMax {
			break
		}
	}
	return lines
}

// cutPSField splits off the first whitespace-delimited field of a ps line and
// returns the rest.
func cutPSField(line string) (field, rest string, ok bool) {
	line = strings.TrimLeft(line, " \t")
	if line == "" {
		return "", "", false
	}
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, "", false
	}
	return line[:i], strings.TrimLeft(line[i:], " \t"), true
}
