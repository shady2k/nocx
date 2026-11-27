package helper_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
)

func TestZeroLeaseRefusesOpen(t *testing.T) {
	var lease farhelper.Lease
	repo, _, err := helpergit.NewFactory(lease).Open(context.Background(), "/repo")
	var unavailable *farhelper.Unavailable
	if repo != nil || !errors.As(err, &unavailable) {
		t.Fatalf("zero lease must refuse before RPC: repo=%v err=%v", repo, err)
	}
}

// These compile as an app consumer, not as farhelper itself: an exported
// test-only constructor or a structural client interface would reopen this seam.
func TestAppCannotForgeGitLease(t *testing.T) {
	for _, tc := range []struct{ file, diagnostic string }{
		{"shell_client.go", "cannot use shellClient"},
		{"forge_lease.go", "cannot assign to lease.Client"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			fixture := filepath.Join("..", "..", "app", "testdata", "leaseauthority", tc.file)
			cmd := exec.Command("go", "test", fixture) // #nosec G204 — fixed compiler and app testdata fixture names
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.diagnostic) {
				t.Fatalf("app must not manufacture Git authority: err=%v\n%s", err, out)
			}
		})
	}
}
