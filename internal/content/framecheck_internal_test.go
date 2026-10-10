//go:build nocx_framecheck

package content

import (
	"context"
	"errors"
	"testing"
)

// THE TEST-TIME DETECTOR (ADR-0077). A store call on a frame's own goroutine
// that forgot the frame's context would wait for the connection the frame
// holds while the frame waits for it. In the nocx_framecheck build it fails
// at once, naming the contract, instead of hanging — for a write, as an
// error; for a read, which cannot carry the store's own error through a
// *sql.Row, as a panic with the same error.
func TestAStoreCallMissingItsFramesContextFailsAtOnce(t *testing.T) {
	s, _ := openFrameStore(t)
	ctx := context.Background()
	var writeErr error
	var readPanic any
	err := s.ApplyLifecycleFrame(ctx, frameSession, 120, func(fctx context.Context) error {
		if err := s.Ledger().EnsureEnvironment(fctx, Environment{ID: "local", Kind: EnvLocal}); err != nil {
			return err
		}
		// The frame holds the connection now. The deliberate mistake:
		writeErr = s.Ledger().EnsureEnvironment(context.Background(), Environment{ID: "other", Kind: EnvLocal})
		func() {
			defer func() { readPanic = recover() }()
			_, _ = s.Ledger().Entry(context.Background(), "s-dom-frame-0")
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleFrame: %v", err)
	}
	if !errors.Is(writeErr, ErrFrameContextMissing) {
		t.Fatalf("the write without the frame's context = %v, want ErrFrameContextMissing", writeErr)
	}
	if pe, ok := readPanic.(error); !ok || !errors.Is(pe, ErrFrameContextMissing) {
		t.Fatalf("the read without the frame's context panicked with %v, want ErrFrameContextMissing", readPanic)
	}
}
