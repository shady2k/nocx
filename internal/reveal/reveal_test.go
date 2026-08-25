package reveal

import (
	"errors"
	"strings"
	"testing"
)

// fakeRunner is the injected command runner for tests. It is in the
// base file because both platform test files use it.
type fakeRunner struct {
	calls  [][]string
	output []byte
	runErr error
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.output, f.runErr
}

// ── platform selection ───────────────────────────────────────────────────

// New returns a non-nil revealer on supported platforms (macOS, Linux)
// and ErrRevealUnavailable on others. The constructor is the composition
// root's single decision point.
func TestNew_ReturnsRevealerOnSupportedPlatform(t *testing.T) {
	r, err := New()
	if err != nil {
		if !errors.Is(err, ErrRevealUnavailable) {
			t.Fatalf("unexpected error: %v", err)
		}
		if r != nil {
			t.Fatal("expected nil revealer on unsupported platform")
		}
		return
	}
	if r == nil {
		t.Fatal("expected non-nil revealer on supported platform")
	}
}

// wrapRunErr includes the command name, the runner error, and any output
// in its message, so a surface rendering the error says which program
// failed and what it printed.
func TestWrapRunErr_IncludesCommandAndOutput(t *testing.T) {
	err := wrapRunErr("xdg-open", []byte("file not found\n"), errors.New("exit 1"))
	if !strings.Contains(err.Error(), "xdg-open") {
		t.Fatalf("missing command name in %q", err.Error())
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Fatalf("missing error text in %q", err.Error())
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("missing output in %q", err.Error())
	}
}
