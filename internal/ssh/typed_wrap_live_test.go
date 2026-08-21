package ssh

// The wrapper against the real oracle (ADR-0015: `ssh -G` is the authority on
// the user's configuration, and there is no subset of it we parse ourselves).
//
// The unit tests above drive a stand-in oracle, which proves the wrapper's
// decisions and nothing about the answers they are made from. This is the
// paired case AGENTS.md's third rule asks for: with a real `ssh` on the
// machine, each refusal class is produced by a real configuration, and the
// accepted case really does expand %C — the 40-character hash that is the
// whole reason the socket path is bounded and per-destination.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func realOracleOrSkip(t *testing.T) ConfigResolver {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client on PATH: the real-oracle half of the wrapper cannot run here")
	}
	return NewSSHConfigResolver(log.NewSlogAdapter(nil), "/dev/null", "")
}

// liveWrapInv writes a fixture ssh_config and returns an invocation that
// names it with a typed -F, which is how the oracle is pointed at a
// configuration without touching the developer's own.
func liveWrapInv(t *testing.T, config string) TypedInvocation {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatalf("write ssh_config: %v", err)
	}
	return TypedInvocation{Host: "fixture-host", User: "alice", Port: 2222, Opts: []string{"-F", cfg}}
}

var liveWrapSocketRE = regexp.MustCompile(`/m-[0-9a-f]{40}$`)

func TestTypedWrapLive_TheOracleExpandsTheSocketPathPerDestination(t *testing.T) {
	res := realOracleOrSkip(t)
	// Under the base DefaultControlRoot chose, never under $TMPDIR
	// directly: on macOS $TMPDIR is a 48-character per-user confinement
	// directory and a socket under it expands past the bound, so a test
	// that mints its own root there measures the runner's temp directory
	// rather than the wrapper.
	root, err := os.MkdirTemp(filepath.Dir(DefaultControlRoot()), "nx")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	w := NewTypedWrapper(log.NewSlogAdapter(nil), res, root)

	inv := liveWrapInv(t, "Host *\n  StrictHostKeyChecking yes\n")
	got, reason, ok := w.Wrap(context.Background(), inv)
	if !ok {
		t.Fatalf("the real oracle refused an ordinary line: %s", reason)
	}
	if !strings.HasPrefix(got.ControlPath, root) {
		t.Fatalf("the expanded socket %q is not under our own root %q", got.ControlPath, root)
	}
	if !liveWrapSocketRE.MatchString(got.ControlPath) {
		t.Fatalf("the expanded socket %q does not end in the %%C hash; the path is then unbounded and shared", got.ControlPath)
	}
	if len(got.ControlPath) > maxControlPathLen {
		t.Fatalf("the expanded socket is %d bytes, past the %d-byte bound", len(got.ControlPath), maxControlPathLen)
	}

	// Per destination, which is what makes a collision impossible by
	// construction: the mux protocol does not isolate destinations, so two
	// destinations sharing one socket is a security property, not a tidiness
	// one.
	other := inv
	other.Host = "another-fixture-host"
	got2, _, ok := w.Wrap(context.Background(), other)
	if !ok {
		t.Fatal("the real oracle refused the second destination")
	}
	if got2.ControlPath == got.ControlPath {
		t.Fatalf("two destinations resolved to the same socket %q", got.ControlPath)
	}
}

func TestTypedWrapLive_EachRefusalClassIsProducedByARealConfiguration(t *testing.T) {
	res := realOracleOrSkip(t)
	for _, tc := range []struct {
		name   string
		config string
		root   string
		want   RefusalReason
	}{
		{
			name:   "a configured RemoteCommand",
			config: "Host *\n  RemoteCommand tmux attach -t work\n  RequestTTY yes\n",
			want:   ReasonRemoteCommand,
		},
		{
			name:   "the user's own ControlMaster",
			config: "Host *\n  ControlMaster auto\n  ControlPath /tmp/mine-%C\n",
			want:   ReasonUserMultiplexPolicy,
		},
		{
			name:   "the user's own ControlPersist",
			config: "Host *\n  ControlPersist 10m\n",
			want:   ReasonUserMultiplexPolicy,
		},
		{
			name:   "a socket root too long to bind",
			config: "Host *\n",
			root:   filepath.Join(os.TempDir(), "nocx-"+strings.Repeat("d", 90)),
			want:   ReasonNoControlPath,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root
			if root == "" {
				var err error
				root, err = os.MkdirTemp(filepath.Dir(DefaultControlRoot()), "nx")
				if err != nil {
					t.Fatalf("socket root: %v", err)
				}
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			w := NewTypedWrapper(log.NewSlogAdapter(nil), res, root)
			_, reason, ok := w.Wrap(context.Background(), liveWrapInv(t, tc.config))
			if ok {
				t.Fatalf("the real oracle accepted a line that must be refused; want %s", tc.want)
			}
			if reason != tc.want {
				t.Fatalf("reason %q, want %q", reason, tc.want)
			}
		})
	}
}
