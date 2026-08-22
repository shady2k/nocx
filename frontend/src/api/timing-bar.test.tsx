// @vitest-environment jsdom
//
// The bar is a reading of five numbers, and every test here is about the
// arithmetic a person would otherwise have to do in their head.
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { TimingBar, phasesOf } from './timing-bar'
import type { ApiTimings } from './api-model'

afterEach(() => cleanup())

/** What the component said this span's share of the bar is. */
const share = (el: HTMLElement): string => el.style.getPropertyValue('--api-timing-share')

const timings = (over: Partial<ApiTimings> = {}): ApiTimings => ({
  dnsMs: 0,
  connectMs: 0,
  tlsMs: 0,
  ttfbMs: 0,
  totalMs: 0,
  ...over,
})

describe('phasesOf', () => {
  it('derives waiting from ttfb minus what happened before it', () => {
    // ttfb is measured from the start of the request, so the far side's own
    // thinking time is the part of it the three phases do not account for.
    const p = phasesOf(timings({ dnsMs: 10, connectMs: 20, tlsMs: 30, ttfbMs: 100, totalMs: 120 }))
    expect(p.map((x) => [x.id, x.ms])).toEqual([
      ['resolve', 10],
      ['connect', 20],
      ['tls', 30],
      ['waiting', 40],
      ['download', 20],
    ])
  })

  it('never draws a negative span when the clocks disagree', () => {
    // Four sets of hooks feed these numbers and one of them can be missing:
    // a handshake that never finished leaves tls at 0 while ttfb is already
    // past. A negative width is a rendering bug; "that phase did not happen"
    // is the honest answer.
    const p = phasesOf(timings({ dnsMs: 90, connectMs: 90, ttfbMs: 10, totalMs: 100 }))
    expect(p.find((x) => x.id === 'waiting')).toBeUndefined()
    expect(p.find((x) => x.id === 'download')).toBeUndefined()
  })

  it('leaves out a phase that did not happen', () => {
    // A plain http:// request does no handshake. A hairline of colour for it
    // would read as a handshake that was instant.
    const p = phasesOf(timings({ connectMs: 5, ttfbMs: 25, totalMs: 30 }))
    expect(p.map((x) => x.id)).toEqual(['connect', 'waiting', 'download'])
  })

  it('a run that never left the machine has no phases at all', () => {
    expect(phasesOf(timings())).toEqual([])
  })
})

describe('TimingBar', () => {
  it('draws one span per phase, to scale', () => {
    const { container } = render(() => (
      <TimingBar timings={timings({ dnsMs: 25, connectMs: 25, ttfbMs: 50, totalMs: 100 })} />
    ))
    const spans = container.querySelectorAll<HTMLElement>('.api-timing__span')
    expect([...spans].map((s) => s.dataset.phase)).toEqual(['resolve', 'connect', 'download'])
    // resolve and connect are a quarter each of the 100ms drawn. The share
    // is a custom property, not a width: the stylesheet reads it (the surface
    // rules forbid an inline style prop, and the paint is the stylesheet's).
    expect(Number.parseFloat(share(spans[0]))).toBeCloseTo(25, 3)
    expect(Number.parseFloat(share(spans[2]))).toBeCloseTo(50, 3)
  })

  it('the spans add up to the whole bar even when a clamp removed time', () => {
    const { container } = render(() => (
      <TimingBar timings={timings({ dnsMs: 60, connectMs: 60, ttfbMs: 10, totalMs: 200 })} />
    ))
    const widths = [...container.querySelectorAll<HTMLElement>('.api-timing__span')].map((s) =>
      Number.parseFloat(share(s)),
    )
    expect(widths.reduce((a, b) => a + b, 0)).toBeCloseTo(100, 2)
  })

  it('names every drawn phase and the measured total for a screen reader', () => {
    const { container } = render(() => (
      <TimingBar timings={timings({ dnsMs: 10, ttfbMs: 10, totalMs: 40 })} />
    ))
    expect(container.querySelector('.api-timing__bar')?.getAttribute('aria-label')).toBe(
      'resolve 10ms, download 30ms, total 40ms',
    )
  })

  it('draws nothing at all when no phase took any time', () => {
    const { container } = render(() => <TimingBar timings={timings()} />)
    expect(container.querySelector('.api-timing')).toBeNull()
  })
})
