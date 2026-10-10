//go:build !nocx_framecheck

package content

import "context"

// The shipped build's no-op twin of framecheck_on.go: no goroutine is
// claimed and no store call is refused.

func (s *sqliteContent) claimGoroutine(*lifecycleFrame) func() { return func() {} }

func (s *sqliteContent) checkFrameContext(context.Context) error { return nil }
