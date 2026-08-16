package sandbox

import (
	"context"
	"io"
	"os"
	"strings"
)

// readStatus reads the readiness byte with a bounded deadline. A zero byte
// means enforcement succeeded; anything else is a typed setup failure with
// the child's reason. The parent's own copy of the write end is closed first
// — WaitReady is only called after the sandboxed process is started, so the
// child already holds its duplicate, and a child that exits without reporting
// now EOFs the read instead of blocking until the deadline. On timeout the
// goroutine stays blocked until cleanup closes the read end.
func readStatus(ctx context.Context, r, w *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = w.Close()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := r.Read(buf)
		if err != nil {
			done <- NewSetupErrorf("readiness: %v", err)
			return
		}
		if n != 1 {
			done <- NewSetupErrorf("readiness: short read")
			return
		}
		if buf[0] != 0 {
			rest, _ := io.ReadAll(r)
			detail := strings.TrimSpace(string(rest))
			if detail == "" {
				detail = "unknown sandbox failure"
			}
			done <- NewSetupErrorf("sandbox setup failed: %s", detail)
			return
		}
		done <- nil
	}()

	select {
	case <-ctx.Done():
		return NewSetupErrorf("readiness timeout: %v", ctx.Err())
	case err := <-done:
		return err
	}
}
