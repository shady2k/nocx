package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// The tier a local session gets follows the user's LOGIN SHELL, through the
// production composition root (nocx-wwz0).
//
// Before this, internal/app pinned `cfg.Command = "bash"` for every local
// enhanced session, so the branch that reads the user's shell was unreachable
// for the only kind of session the product opens. On macOS — zsh by default
// since Catalina — that meant every user of the shipping platform was handed
// Apple's frozen bash 3.2.57, their aliases, functions, prompt and history all
// somewhere else. The resolver is injected here rather than left to the
// machine: the assertion is about which shell nocx STARTS given an answer, and
// a test that depended on the developer's own account could only report the
// developer's own account.

// fixedShell is a Resolver that answers one way, so a test can ask what nocx
// does with a login shell this machine may not have.
type fixedShell struct{ path string }

func (f fixedShell) Resolve() loginshell.Shell {
	return loginshell.Shell{Path: f.path, Source: loginshell.SourceAccount}
}

// requireShellBinary resolves a real shell on THIS host and fails when there
// is none. It is the companion to fixedShell: the fixture says which shell
// nocx is told to start, and this says which path on this machine that answer
// can point at.
//
// Resolved, never written down. /bin/bash is macOS's and Debian's answer and
// is not NixOS's or Guix's, where the account's bash lives under
// /run/current-system/sw/bin — internal/loginshell, which owns this question
// in the PRODUCT, already answers it that way and carries the NixOS path as a
// named case in its table. A fixture that hard-codes /bin/bash therefore
// asserts the developer's filesystem layout rather than nocx's behaviour, and
// on a host without it the test reports `fork/exec /bin/bash: no such file or
// directory` about a product that never assumed the path (nocx-65v6).
//
// WHY THIS DOES NOT MAKE THE ASSERTIONS TAUTOLOGICAL. The resolved path is
// INJECTED through fixedShell, and the assertions compare what the product
// reports against that injected value — not against whatever the machine
// would have answered on its own. The product is told to start this shell and
// must say so; reporting a name, a basename, the account's real login shell,
// or nothing at all still fails, on every host. The machine supplies only a
// path that exists, so that fork/exec can succeed at all.
//
// It FAILS rather than skips, for the reason nocx-gd84 gave: a skip here is
// how the tier these tests exist to prove reports green on a machine that
// never ran it.
func requireShellBinary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s is not installed: %v — the shell this test starts must be present, "+
			"or the suite reports a product it never ran as a pass (nocx-gd84)", name, err)
	}
	return path
}

// TestLocalSession_TierFollowsTheLoginShell is the bead's first acceptance
// criterion at the seam a person reaches: open a local tab, and the shell that
// comes up is the account's own — integrated, not substituted. Both integrated
// tiers are asserted by the same table, because "zsh works now" and "bash still
// works" are one property and regressing either is the same defect.
func TestLocalSession_TierFollowsTheLoginShell(t *testing.T) {
	tests := []struct {
		name string
		bin  string
		// probe asks the running shell to identify itself in its own
		// vocabulary — a variable only that shell defines, so a substituted
		// shell cannot answer it.
		probe string
		want  string
	}{
		{name: "a zsh login shell gets the zsh tier", bin: "zsh", probe: `echo "IAM=[${ZSH_NAME-none}]"`, want: "IAM=[zsh]"},
		{name: "a bash login shell still gets the bash tier", bin: "bash", probe: `echo "IAM=[${BASH-none}]"`, want: "IAM=[/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := requireShellBinary(t, tt.bin)
			storagetest.IsolateWithHome(t)
			f := localFactory(t)
			f.shells = fixedShell{path: path}

			pt, err := f.NewPTY(context.Background(), pty.Config{
				Cols: 80, Rows: 24, Enhanced: true, SessionID: "sid-tier",
			})
			if err != nil {
				t.Fatalf("NewPTY: %v", err)
			}
			defer closePTY(t, pt)

			if _, ok := pt.(*lifecyclePTY); !ok {
				t.Fatalf("a %s login shell must get an INTEGRATED session; got %T", tt.bin, pt)
			}
			out := ptyEcho(t, pt, tt.probe, tt.want)
			if !strings.Contains(out, tt.want) {
				t.Errorf("the running shell is not the account's %s: %q", tt.bin, out)
			}
		})
	}
}

// TestLocalSession_UnsupportedLoginShellRunsItAndSaysWhy is criterion 3. A
// login shell with no local tier — fish, csh, tcsh, anything — must be STARTED,
// not replaced by a bash the user never chose, and the missing integration must
// reach the product rather than a log line: the factory reports it on the
// integration axis, which the renderer turns into a persistent mark and a
// reason the user can act on (nocx-dvql, nocx-5uu5).
//
// The shell here is a fixture rather than a real fish, because what is under
// test is nocx's decision, not fish's behaviour, and a machine without fish
// would otherwise report a passing skip for a decision it never made.
func TestLocalSession_UnsupportedLoginShellRunsItAndSaysWhy(t *testing.T) {
	storagetest.IsolateWithHome(t)
	shell := fakeUnsupportedShell(t)
	f := localFactory(t)
	f.shells = fixedShell{path: shell}
	rec := &integrationRecorder{}
	f.reportIntegration = rec.report

	pt, err := f.NewPTY(context.Background(), pty.Config{
		Cols: 80, Rows: 24, Enhanced: true, SessionID: "sid-unsupported",
	})
	if err != nil {
		t.Fatalf("NewPTY: %v", err)
	}
	defer closePTY(t, pt)

	if _, ok := pt.(*lifecyclePTY); ok {
		t.Error("a shell with no local tier must not be handed a lifecycle channel it cannot use")
	}
	// Reported through the factory, not carried on the pty. The optional-method
	// seam this test originally asserted was the remote path's; nocx-dvql made
	// the notification the single owner and registerRemoteIntegration returns
	// early for local sessions, so a reason on the pty would be a write nothing
	// reads — the fish user's tab would degrade exactly as silently as before.
	seen := rec.all()
	if len(seen) != 1 {
		t.Fatalf("reports = %+v, want exactly one — the degrade would otherwise be log-only", seen)
	}
	if got := seen[0]; got.status != transport.IntegrationConventional || got.reason != ssh.ReasonUnsupportedShell {
		t.Errorf("report = %+v, want status=conventional with reason %q", got, ssh.ReasonUnsupportedShell)
	}
	if got := seen[0].shell; got != shell {
		t.Errorf("reported shell = %q, want the fixture the resolver named (%q)", got, shell)
	}

	// And it is the USER'S shell that came up, as a login shell, told nothing
	// about an integration it is not getting.
	out := waitForPTY(t, pt, "FAKE_SHELL_STARTED")
	if !strings.Contains(out, "argv=[-l]") {
		t.Errorf("the shell was not started as a login shell: %q", out)
	}
	if !strings.Contains(out, "MODE=[unset]") {
		t.Errorf("a shell that will not be integrated must not be told it is: %q", out)
	}
}

// localFactory boots the real composition root and hands back its local PTY
// factory — the object the product uses, not one assembled here.
func localFactory(t *testing.T) *localPTYFactory {
	t.Helper()
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	f, ok := a.Pty.(*localPTYFactory)
	if !ok {
		t.Fatalf("Pty factory is %T, want *localPTYFactory", a.Pty)
	}
	if f.shells == nil {
		t.Fatal("the composition root wired no login-shell resolver")
	}
	return f
}

// fakeUnsupportedShell writes an executable whose NAME maps to no local tier
// and which reports what it was started with.
func fakeUnsupportedShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fish")
	body := "#!/bin/sh\n" +
		"echo \"FAKE_SHELL_STARTED argv=[$*] MODE=[${NOCX_PROMPT_MODE-unset}] GATE=[${NOCX_SHELL_INTEGRATION-unset}]\"\n" +
		"while IFS= read -r line; do :; done\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // a fixture shell must be executable
		t.Fatalf("write fixture shell: %v", err)
	}
	return path
}

func closePTY(t *testing.T, pt pty.Pty) {
	t.Helper()
	_ = pt.Close()
	select {
	case <-pt.Done():
	case <-time.After(20 * time.Second):
		t.Error("the shell did not exit after Close — a closed tab must not leak a shell")
	}
}

// ptyEcho types one line and waits for the answer it names. Waiting on the
// shell's own output rather than on a duration is what keeps the assertion
// from passing before the shell has said anything (AGENTS.md: never wait on a
// duration).
func ptyEcho(t *testing.T, pt pty.Pty, line, want string) string {
	t.Helper()
	if _, err := pt.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write to pty: %v", err)
	}
	return waitForPTY(t, pt, want)
}

// waitForPTY drains the pty until want appears, and returns everything read.
func waitForPTY(t *testing.T, pt pty.Pty, want string) string {
	t.Helper()
	var mu sync.Mutex
	var buf strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 4096)
		for {
			n, err := pt.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := buf.String()
		mu.Unlock()
		if strings.Contains(got, want) {
			return got
		}
		select {
		case <-done:
			return got
		case <-time.After(25 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("timed out waiting for %q on the pty; read: %q", want, buf.String())
	return ""
}
