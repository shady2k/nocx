import { describe, expect, it } from 'vitest'
import { violationsInText } from '../../.githooks/check-log-context.mjs'

/**
 * The log-context ratchet (nocx-n14oo.9) refuses a NEW logging call that
 * cannot be shown to go through log.From(ctx). These fixtures exercise the
 * three provenance rules the checker recognises — `log.From`, the second
 * return of `log.Start`, and a `.With`/`.WithContext` chain off an already
 * compliant logger — plus the two shapes it deliberately does NOT clear: a
 * bare call and a *slog.Logger the checker can never prove compliant.
 */

const violations = (src) => violationsInText('fixture.go', src)

describe('log-context ratchet: what counts as going through log.From(ctx)', () => {
  it('flags a bare call with no traceable provenance', () => {
    const src = `package x
func f(l Logger) {
	l.Info("hi")
}
`
    expect(violations(src)).toHaveLength(1)
    expect(violations(src)[0].text).toContain('l.Info(')
  })

  it('clears a logger assigned from log.From(ctx)', () => {
    const src = `package x
import "github.com/shady2k/nocx/internal/log"
func f(ctx context.Context) {
	lg := log.From(ctx)
	lg.Info("hi")
}
`
    expect(violations(src)).toHaveLength(0)
  })

  it('clears a logger assigned from an aliased import', () => {
    const src = `package x
import nocxlog "github.com/shady2k/nocx/internal/log"
func f(ctx context.Context) {
	lg := nocxlog.From(ctx)
	lg.Warn("hi")
}
`
    expect(violations(src)).toHaveLength(0)
  })

  it('clears the second return of log.Start', () => {
    const src = `package x
import "github.com/shady2k/nocx/internal/log"
func f(ctx context.Context, l log.Logger) {
	ctx, lg, end := log.Start(ctx, l, "op")
	lg.Debug("started")
	_ = ctx
	_ = end
}
`
    expect(violations(src)).toHaveLength(0)
  })

  it('clears a .With chain off an already-compliant logger', () => {
    const src = `package x
import "github.com/shady2k/nocx/internal/log"
func f(ctx context.Context) {
	lg := log.From(ctx)
	bound := lg.With("op", "session.open")
	bound.Info("opened")
}
`
    expect(violations(src)).toHaveLength(0)
  })

  it('does not clear a chain off a logger with no provenance', () => {
    const src = `package x
func f(l Logger) {
	bound := l.With("op", "session.open")
	bound.Info("opened")
}
`
    expect(violations(src)).toHaveLength(1)
  })

  it('clears a parameter typed log.Logger', () => {
    const src = `package x
import "github.com/shady2k/nocx/internal/log"
func f(lg log.Logger) {
	lg.Warn("degraded")
}
`
    expect(violations(src)).toHaveLength(0)
  })

  it('never clears a *slog.Logger call, since log.From cannot produce one', () => {
    const src = `package x
import "log/slog"
func f(l *slog.Logger) {
	l.Error("boom")
}
`
    expect(violations(src)).toHaveLength(1)
  })
})
