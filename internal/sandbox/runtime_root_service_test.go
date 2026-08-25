package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestServiceNewRuntimeRootRedactsPrivatePath(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "private-cache-file")
	if err := os.WriteFile(privatePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}
	svc := New(log.NewSlogAdapter(nil), privatePath)
	_, err := svc.NewRuntimeRoot()
	var setupErr *SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("err = %v, want SetupError", err)
	}
	if strings.Contains(err.Error(), privatePath) {
		t.Fatalf("runtime-root error leaked private path: %v", err)
	}
}
