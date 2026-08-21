package ssh

// The typed-`ssh` wrapper (design §4.3 and §4.4, assertions 21 and 22).
//
// Every case here asks the same two questions of one decision: does the
// wrapped line still do everything the user's line did, and does a refusal
// leave the user's line ALONE — no socket, no directory, no oracle-driven
// rewrite, nothing.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// typedOracle is a stand-in for `ssh -G` that answers PER ARGV, because the
// two questions the wrapper asks are different questions: one about the
// user's own line and one about the line the wrapper proposes. A stub that
// answered the same thing to both could not tell them apart, and neither
// could the test.
type typedOracle struct {
	// user is what the oracle says about the line as the user typed it.
	user HostConfig
	// wrappedPath is the control path the oracle expands OUR template to.
	wrappedPath string
	// err makes every resolution fail.
	err error
	// argvs records every call, in order.
	argvs [][]string
}

func (o *typedOracle) ResolveArgv(_ context.Context, argv []string) (*HostConfig, error) {
	o.argvs = append(o.argvs, append([]string(nil), argv...))
	if o.err != nil {
		return &HostConfig{}, o.err
	}
	wrapped := false
	for _, w := range argv {
		if strings.HasPrefix(w, "ControlPath=") {
			wrapped = true
		}
	}
	cfg := o.user
	if wrapped {
		cfg.ControlPath = o.wrappedPath
		cfg.ControlMaster = "auto"
	}
	return &cfg, nil
}

func (o *typedOracle) ResolveHost(_ context.Context, host string) (string, error) {
	return host, o.err
}

func (o *typedOracle) ResolveConfig(_ context.Context, _ string) (*HostConfig, error) {
	cfg := o.user
	return &cfg, o.err
}

var _ ConfigResolver = (*typedOracle)(nil)

func typedTestWrapper(t *testing.T, res ConfigResolver) (*TypedWrapper, string) {
	t.Helper()
	// A short root, as production builds one: a long ControlPath does not
	// degrade to no-multiplexing, it kills the connection.
	root, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return NewTypedWrapper(log.NewSlogAdapter(nil), res, root), root
}

func typedInv() TypedInvocation {
	return TypedInvocation{
		Host: "far.example", User: "alice", Port: 2222,
		Opts: []string{"-i", "~/.ssh/prod", "-o", "StrictHostKeyChecking=yes", "-A"},
	}
}

// ---------------------------------------------------------------------------
// Assertion 22: every option the user gave survives, in order and with its
// value; the wrapper adds its own multiplex options and nothing else.

// The decision itself: an ordinary line is accepted, our own two options are
// what we add, and the socket we will prove ownership against is the one the
// oracle expanded. The LINE is composed at the composition root, and that is
// where the option-preservation and exit-status assertions live
// (internal/app/typed_line_test.go).
func TestTypedWrap_AcceptsAnOrdinaryLineAndNamesItsSocket(t *testing.T) {
	res := &typedOracle{wrappedPath: "/nx/m-abc"}
	w, _ := typedTestWrapper(t, res)

	got, reason, ok := w.Wrap(context.Background(), typedInv())
	if !ok {
		t.Fatalf("the wrapper refused an ordinary line: %s", reason)
	}
	if got.ControlPath != "/nx/m-abc" {
		t.Fatalf("the proven-against path is %q, want the oracle's expansion", got.ControlPath)
	}
	joined := strings.Join(got.MuxOptions, " ")
	for _, want := range []string{"ControlMaster=auto", "ControlPath=", "ControlPersist=no"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the added options %q do not carry %q", joined, want)
		}
	}
	if strings.Contains(joined, "-M") || strings.Contains(joined, "-S ") {
		t.Fatalf("the wrapper used the short forms: %q — the -o spelling is what the oracle can answer about", joined)
	}
}

// ---------------------------------------------------------------------------
// Assertion 21: each pre-authentication refusal class runs the user's line
// with ZERO nocx effect. The observable is negative and is taken at three
// seams at once: no oracle rewrite is offered, no socket directory entry is
// created, and the wrapper reports the class by name.

func TestTypedWrap_RefusesAConfiguredRemoteCommand(t *testing.T) {
	res := &typedOracle{user: HostConfig{RemoteCommand: "tmux attach -t work"}, wrappedPath: "/nx/m-abc"}
	w, root := typedTestWrapper(t, res)

	_, reason, ok := w.Wrap(context.Background(), typedInv())
	if ok {
		t.Fatal("the wrapper accepted a destination whose RemoteCommand OpenSSH refuses to run beside ours")
	}
	if reason != ReasonRemoteCommand {
		t.Fatalf("reason %q, want %q", reason, ReasonRemoteCommand)
	}
	assertNoSocketResidue(t, root)
}

func TestTypedWrap_RefusesTheUsersOwnMultiplexPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  HostConfig
	}{
		{"controlmaster", HostConfig{ControlMaster: "auto", ControlPath: "/home/u/.ssh/cm"}},
		{"controlpath alone", HostConfig{ControlPath: "/home/u/.ssh/cm"}},
		{"controlpersist", HostConfig{ControlPersist: "600"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &typedOracle{user: tc.cfg, wrappedPath: "/nx/m-abc"}
			w, root := typedTestWrapper(t, res)
			_, reason, ok := w.Wrap(context.Background(), typedInv())
			if ok {
				t.Fatal("the wrapper overrode a multiplex policy the user expressed")
			}
			if reason != ReasonUserMultiplexPolicy {
				t.Fatalf("reason %q, want %q", reason, ReasonUserMultiplexPolicy)
			}
			assertNoSocketResidue(t, root)
		})
	}
}

// The command-line forms are refused on the typed words alone, without asking
// the oracle at all: -M and -S are the user saying it in the shortest way
// there is.
func TestTypedWrap_RefusesTheCommandLineMultiplexFlags(t *testing.T) {
	for _, opts := range [][]string{
		{"-M"},
		{"-S", "/tmp/mine"},
		{"-o", "ControlPath=/tmp/mine"},
		{"-o", "controlmaster=yes"},
		{"-o", "ControlPersist=yes"},
	} {
		res := &typedOracle{wrappedPath: "/nx/m-abc"}
		w, root := typedTestWrapper(t, res)
		inv := typedInv()
		inv.Opts = opts
		_, reason, ok := w.Wrap(context.Background(), inv)
		if ok {
			t.Fatalf("%v was accepted; the user expressed their own multiplex policy", opts)
		}
		if reason != ReasonUserMultiplexPolicy {
			t.Fatalf("%v: reason %q, want %q", opts, reason, ReasonUserMultiplexPolicy)
		}
		if len(res.argvs) != 0 {
			t.Fatalf("%v: the oracle was consulted for a line refused on its own words: %v", opts, res.argvs)
		}
		assertNoSocketResidue(t, root)
	}
}

// The spike's one fail-closed class: an over-long ControlPath does not
// degrade to no-multiplexing, it kills the connection. So it must be decided
// BEFORE anything is attempted, and the decision is on the EXPANDED path the
// oracle reports, because %C is what makes the length knowable at all.
func TestTypedWrap_RefusesWhenNoSafeShortSocketPathCanBeBuilt(t *testing.T) {
	res := &typedOracle{wrappedPath: "/" + strings.Repeat("x", 200) + "/m"}
	w, root := typedTestWrapper(t, res)
	_, reason, ok := w.Wrap(context.Background(), typedInv())
	if ok {
		t.Fatal("a socket path past the platform bound was accepted; ssh would refuse to start and the user would lose the connection")
	}
	if reason != ReasonNoControlPath {
		t.Fatalf("reason %q, want %q", reason, ReasonNoControlPath)
	}
	assertNoSocketResidue(t, root)
}

// And the paired assertion the refusal above never made: on an ORDINARY
// machine the root we ship actually holds a socket. Every test around it
// hands the wrapper a root of its own, so all of them passed on a platform
// where DefaultControlRoot could not be used at all — macOS, where $TMPDIR
// is a 48-character per-user confinement directory and the expansion put
// the socket 4 bytes past the bound. Five internal/app tests failed on the
// macOS runner and none of them named the cause, because the product's
// answer to "where do our sockets live" was the one thing under test that
// nothing tested.
//
// The macOS shape is exercised BY LENGTH rather than by GOOS: what breaks a
// socket path is how long $TMPDIR is, and that is reproducible anywhere.
func TestDefaultControlRoot_LeavesRoomForTheExpansion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tmpdir string
	}{
		{"linux /tmp", "/tmp"},
		// /var/folders/<2>/<30>/T, character for character.
		{"macOS per-user confinement dir", "/var/folders/df/" + strings.Repeat("z", 30) + "/T"},
		// A base with no room at all: the fallback is what keeps the
		// product working, not the length of the base it was handed.
		{"a base far past the budget", "/" + strings.Repeat("d", 120)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", tc.tmpdir)
			root := DefaultControlRoot()
			if got := len(root) + expandedSocketNameLen; got > maxControlPathLen {
				t.Errorf("a socket under %s expands to %d bytes, past the %d-byte bound: "+
					"ssh refuses to start rather than degrade, so the typed path is dead on this machine",
					root, got, maxControlPathLen)
			}
			if !filepath.IsAbs(root) {
				t.Errorf("control root %q is not absolute", root)
			}
			if !strings.Contains(root, "nocx-mux-") {
				t.Errorf("control root %q does not carry the per-uid name the ownership check depends on", root)
			}
		})
	}
}

// The same class, arriving as a directory we cannot own: a socket directory
// that is missing or unwritable makes ssh exit with `unix_listener: cannot
// bind`, which is worse than losing integration.
func TestTypedWrap_RefusesWhenTheSocketDirectoryCannotBeOwned(t *testing.T) {
	res := &typedOracle{wrappedPath: "/nx/m-abc"}
	root := filepath.Join(t.TempDir(), "as-a-file")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the obstruction: %v", err)
	}
	w := NewTypedWrapper(log.NewSlogAdapter(nil), res, root)
	_, reason, ok := w.Wrap(context.Background(), typedInv())
	if ok {
		t.Fatal("a socket root that is not a directory we own was accepted")
	}
	if reason != ReasonNoControlPath {
		t.Fatalf("reason %q, want %q", reason, ReasonNoControlPath)
	}
}

// The oracle failing is not a licence to guess: without an answer we do not
// know whether a RemoteCommand is configured or where the socket would land.
func TestTypedWrap_RefusesWhenTheOracleCannotAnswer(t *testing.T) {
	res := &typedOracle{err: errors.New("ssh -G is not available here")}
	w, root := typedTestWrapper(t, res)
	_, reason, ok := w.Wrap(context.Background(), typedInv())
	if ok {
		t.Fatal("the wrapper rewrote a line it could not resolve")
	}
	if reason != ReasonNoControlPath {
		t.Fatalf("reason %q, want %q", reason, ReasonNoControlPath)
	}
	assertNoSocketResidue(t, root)
}

// The directory is created BEFORE the line is submitted, 0700, and it is the
// wrapper that creates it — ssh does not, and a missing directory is one of
// the two ways to lose the user's connection outright.
func TestTypedWrap_CreatesTheSocketDirectoryItself(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nx")
	res := &typedOracle{wrappedPath: filepath.Join(root, "m-abc")}
	w := NewTypedWrapper(log.NewSlogAdapter(nil), res, root)
	if _, _, ok := w.Wrap(context.Background(), typedInv()); !ok {
		t.Fatal("the wrapper refused an ordinary line")
	}
	fi, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("the socket directory was not created: %v", err)
	}
	if !fi.IsDir() {
		t.Fatal("the socket root is not a directory")
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("the socket directory is mode %o, want 0700 — the control socket is the trust boundary", perm)
	}
}

// The oracle sees exactly what the user typed. If our own options reached it,
// the answers to "did the user express a multiplex policy" and "is a
// RemoteCommand configured" would be answers about our line, not theirs.
func TestTypedWrap_TheOracleIsAskedAboutTheUsersOwnLine(t *testing.T) {
	res := &typedOracle{wrappedPath: "/nx/m-abc"}
	w, _ := typedTestWrapper(t, res)
	if _, _, ok := w.Wrap(context.Background(), typedInv()); !ok {
		t.Fatal("the wrapper refused an ordinary line")
	}
	first := res.argvs[0]
	for _, word := range first {
		if strings.Contains(word, "ControlMaster") || strings.Contains(word, "ControlPath") {
			t.Fatalf("our own multiplex options reached the first oracle call: %v", first)
		}
	}
	if len(res.argvs) < 2 {
		t.Fatalf("the wrapper asked the oracle %d times; the expanded socket path needs its own answer", len(res.argvs))
	}
	second := strings.Join(res.argvs[1], " ")
	if !strings.Contains(second, "ControlPath=") || !strings.Contains(second, "%C") {
		t.Fatalf("the second oracle call did not ask for the %%C expansion: %v", res.argvs[1])
	}
}

func assertNoSocketResidue(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read the socket root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused line left %d entries under the socket root", len(entries))
	}
}
