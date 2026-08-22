package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A package-store distribution (Nix, Guix) and a Homebrew-heavy macOS give
// every library its own directory, so the backend-DERIVED read-only set is a
// function of the machine rather than of the request. The dev host that found
// this produced 339 runtime roots against a 256 bound, and every sandboxed
// launch failed -32007 with a message naming an internal constant. The user's
// own contribution is bounded separately and tightly (maxUserPaths per list),
// so the bound here must not be the thing that decides whether a whole
// distribution can run the feature.
func TestValidatePolicy_AcceptsAPackageStoreSizedRootSet(t *testing.T) {
	p := scalePolicyFixture(t)
	for i := 0; i < 512; i++ {
		p.ReadOnlyRoots = append(p.ReadOnlyRoots, fmt.Sprintf("/store/%04x-pkg/lib", i))
	}
	if err := ValidatePolicy(p); err != nil {
		t.Fatalf("ValidatePolicy rejected a %d-root machine-derived policy: %v",
			len(p.ReadOnlyRoots), err)
	}
}

// Whatever the bound is, it is still a bound: a policy nothing could have
// derived from a real machine is refused rather than handed to the kernel.
func TestValidatePolicy_StillRefusesAnAbsurdRootSet(t *testing.T) {
	p := scalePolicyFixture(t)
	for i := 0; i <= maxRoots; i++ {
		p.ReadOnlyRoots = append(p.ReadOnlyRoots, fmt.Sprintf("/store/%06x-pkg/lib", i))
	}
	if err := ValidatePolicy(p); err == nil {
		t.Fatal("expected a root-count rejection above the bound")
	}
}

// A read-only root another read-only root already contains grants nothing the
// first one does not. Dropping it is what keeps an FHS machine's derived set
// small, and it is the same subsumption the writable side already applies.
func TestPolicyNormalize_DropsReadOnlyRootsAnotherReadOnlyRootContains(t *testing.T) {
	base := t.TempDir()
	usr := filepath.Join(base, "usr")
	nested := filepath.Join(usr, "lib", "x86_64-linux-gnu")
	other := filepath.Join(base, "opt")
	for _, d := range []string{nested, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	p := scalePolicyFixture(t)
	p.ReadOnlyRoots = []string{usr, nested, other}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if inSet(nested, p.ReadOnlyRoots) {
		t.Errorf("nested read-only root survived coalescing: %v", p.ReadOnlyRoots)
	}
	for _, want := range []string{usr, other} {
		if !inSet(want, p.ReadOnlyRoots) {
			t.Errorf("coalescing dropped %q, which nothing contains: %v", want, p.ReadOnlyRoots)
		}
	}
}

// Coalescing may never turn a read-only entry into a writable one, and may
// never drop the shell the policy is about to execute.
func TestPolicyNormalize_CoalescingKeepsTheShellAndWidensNothing(t *testing.T) {
	p := scalePolicyFixture(t)
	before := append([]string(nil), p.WritableRoots...)
	p.ReadOnlyRoots = []string{"/usr", "/usr/lib", "/usr/lib/locale"}
	p.ReadOnlyFiles = []string{"/bin/sh"}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.WritableRoots) != len(before) {
		t.Errorf("writable roots changed: %v -> %v", before, p.WritableRoots)
	}
	if !inSet("/bin/sh", p.ReadOnlyFiles) && !inSet("/bin", p.ReadOnlyRoots) {
		t.Errorf("the shell lost its read-only grant: files=%v roots=%v",
			p.ReadOnlyFiles, p.ReadOnlyRoots)
	}
}

// The renderer shows the reason it is given. "sandbox setup failed" tells a
// user nothing they can act on; a policy that outgrew its bounds is a fact
// about their machine and has to survive to the wire as one.
func TestBuildPolicy_PolicyTooLargeCarriesATypedReason(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "ws")
	runtimeRoot := filepath.Join(base, "rt")
	for _, d := range []string{
		workspace,
		filepath.Join(runtimeRoot, "home"),
		filepath.Join(runtimeRoot, "tmp"),
	} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// A machine, not a request: every one of these is an ordinary absolute
	// PATH directory, which is exactly how a package store overruns the
	// bound. Nothing here is user-supplied.
	pathDirs := make([]string, 0, maxRoots+8)
	for i := 0; i <= maxRoots+4; i++ {
		d := filepath.Join(base, "store", fmt.Sprintf("%06x-pkg", i), "bin")
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		pathDirs = append(pathDirs, d)
	}
	env := []string{"PATH=" + strings.Join(pathDirs, string(os.PathListSeparator))}
	_, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, env)
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SetupError", err)
	}
	if se.Reason != ReasonPolicyTooLarge {
		t.Fatalf("reason = %q, want %q", se.Reason, ReasonPolicyTooLarge)
	}
}

// scalePolicyFixture is the smallest policy ValidatePolicy accepts.
func scalePolicyFixture(t *testing.T) *Policy {
	t.Helper()
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	home := filepath.Join(base, "home")
	tmp := filepath.Join(base, "tmp")
	for _, d := range []string{ws, home, tmp} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return &Policy{
		Workspace:       ws,
		WritableRoots:   []string{ws, home, tmp},
		ReadOnlyRoots:   []string{},
		ReadOnlyFiles:   []string{"/bin/sh"},
		Shell:           "/bin/sh",
		Home:            home,
		Tmp:             tmp,
		HomeProjections: []HomeProjection{},
	}
}

func inSet(path string, set []string) bool {
	for _, s := range set {
		if sameDir(path, s) {
			return true
		}
	}
	return false
}
