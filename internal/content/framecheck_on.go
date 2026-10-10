//go:build nocx_framecheck

package content

// THE TEST-TIME DETECTOR FOR ADR-0077'S CONTRACT. A store call made on a
// lifecycle frame's own goroutine without the frame's context waits for the
// connection the frame holds while the frame waits for the call. Telling that
// call from a legitimate one — another goroutine's, which must wait — takes
// the calling goroutine's identity, which Go deliberately does not offer; so
// this check reads it from the runtime's stack header, and exists only in the
// nocx_framecheck build that CI's Go test runs use. A shipped build has none:
// there the frame's hold bound (LifecycleFrameMaxHold) is what ends such a
// wait, and a ratchet (framecheck_absent_test.go) keeps this file out of it.

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

var (
	frameOwners     sync.Map // goroutine id -> *lifecycleFrame
	frameOwnerCount atomic.Int64
)

func currentGoroutine() uint64 {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	buf = bytes.TrimPrefix(buf, []byte("goroutine "))
	if i := bytes.IndexByte(buf, ' '); i > 0 {
		buf = buf[:i]
	}
	id, _ := strconv.ParseUint(string(buf), 10, 64)
	return id
}

// claimGoroutine records that the calling goroutine runs frame f, and answers
// the release.
func (s *sqliteContent) claimGoroutine(f *lifecycleFrame) func() {
	id := currentGoroutine()
	frameOwners.Store(id, f)
	frameOwnerCount.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			frameOwners.CompareAndDelete(id, f)
			frameOwnerCount.Add(-1)
		})
	}
}

// checkFrameContext refuses a store call that carries no frame of this
// store's while the calling goroutine is running one.
func (s *sqliteContent) checkFrameContext(ctx context.Context) error {
	if frameOwnerCount.Load() == 0 || s.frameOf(ctx) != nil {
		return nil
	}
	v, ok := frameOwners.Load(currentGoroutine())
	if !ok {
		return nil
	}
	if f, isFrame := v.(*lifecycleFrame); isFrame && f.s == s {
		return ErrFrameContextMissing
	}
	return nil
}
