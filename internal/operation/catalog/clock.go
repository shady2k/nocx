package catalog

import (
	"context"
	"time"
)

// Clock supplies operation time and timers, including deterministic test time.
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}

// Timer is an operation deadline registration.
type Timer interface{ Stop() bool }

type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// BeginWithClock starts a scope using the supplied time source.
func BeginWithClock(ctx context.Context, op Method, inv Invocation, clock Clock) *Scope {
	return beginClock(ctx, op, inv, false, clock)
}
