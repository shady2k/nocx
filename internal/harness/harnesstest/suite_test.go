package harnesstest

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGateWithoutReaderHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f, err := openGate(ctx, path); !errors.Is(err, context.Canceled) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatalf("openGate without reader = %v, want context cancellation", err)
	}
}
