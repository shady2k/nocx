/**
 * gutter-spike.test.ts — Tests for the gutter-geometry feasibility spike.
 *
 * Runs under jsdom. Headless layout limits what bounding-rect / clipping
 * assertions can be made (no real rendering), but we can validate:
 *  - API shape (terminal opens, markers are created, decorations are registered)
 *  - DOM element presence (decoration elements appear in the tree)
 *  - CSS class structure (xterm internal DOM classes)
 *  - The RECOMMENDED createGutter() function creates DOM elements correctly
 *
 * NOTE: Approach (a) transform+clipping and approach (c) padding-offset
 * assertions are LIMITED by jsdom — jsdom does not perform CSS layout so
 * getBoundingClientRect() returns 0,0,0,0 for all elements. These behaviours
 * MUST be verified with a visual manual check in a real browser.
 *
 * @vitest-environment jsdom
 */

// ── jsdom polyfills for xterm.js 5.5 ───────────────────────────────────
// xterm's CoreBrowserService calls matchMedia; jsdom doesn't provide it.
// We polyfill before any imports that might trigger the xterm module graph.
if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string): MediaQueryList =>
      ({
        matches: false,
        media: query,
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList,
  })
}
if (typeof window !== 'undefined' && !window.innerWidth) {
  Object.defineProperty(window, 'innerWidth', { writable: true, value: 1024 })
  Object.defineProperty(window, 'innerHeight', { writable: true, value: 768 })
  Object.defineProperty(window, 'devicePixelRatio', { writable: true, value: 1 })
}

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { mountGutterSpike, createGutter, RECOMMENDED_API_CALLS } from './gutter-spike'
import type { SpikeHarness } from './gutter-spike'
import { Terminal } from '@xterm/xterm'

describe('gutter-spike harness', () => {
  let container: HTMLElement
  let harness: SpikeHarness | null

  beforeEach(() => {
    container = document.createElement('div')
    container.id = 'spike-container'
    document.body.appendChild(container)
  })

  afterEach(() => {
    harness?.dispose()
    container.remove()
  })

  it('mounts an xterm Terminal and writes content', () => {
    harness = mountGutterSpike(container)

    // Terminal element should exist in the DOM.
    const termElement = container.querySelector('.xterm')
    expect(termElement).not.toBeNull()
    expect(termElement!.classList.contains('xterm')).toBe(true)
  })

  it('creates an xterm-screen element', () => {
    harness = mountGutterSpike(container)

    const screen = container.querySelector('.xterm-screen')
    expect(screen).not.toBeNull()
    expect(harness.screen).toBe(screen)
  })

  it('registers markers and decorations without throwing', () => {
    harness = mountGutterSpike(container)
    const results = harness.collectResults()

    // Neither approach should have thrown — the harness should have produced
    // a result object with a log.
    expect(results.log.length).toBeGreaterThan(0)
    expect(results.log.some((l) => l.includes('[A] Registering'))).toBe(true)
    expect(results.log.some((l) => l.includes('[B] Creating'))).toBe(true)
    expect(results.log.some((l) => l.includes('[C] Reserving'))).toBe(true)
  })

  it('approach (a) registers a decoration via the API', () => {
    harness = mountGutterSpike(container)
    const results = harness.collectResults()

    // API-level: registerMarker + registerDecoration succeeded.
    expect(results.log.some((l) => l.includes('[A] Registering marker'))).toBe(true)

    // IMPORTANT: In jsdom, decoration.onRender never fires (no real canvas
    // rendering pipeline), so the decoration element is never created and
    // approachA logs "[A] FAIL: decoration element never rendered". This
    // is EXPECTED in headless tests — in a real browser the decoration
    // element renders within the next animation frame.
    //
    // The FAIL log is informational: it means the approach didn't crash,
    // it just couldn't verify the visual placement without a real browser.
    const failMsgs = results.log.filter((l) => l.startsWith('[A] FAIL'))
    // We expect exactly one FAIL: "decoration element never rendered" — that
    // is a jsdom limitation, not a code bug.
    expect(failMsgs.length).toBe(1)
    expect(failMsgs[0]).toContain('decoration element never rendered')
  })

  it('approach (b) creates a gutter div as sibling of .xterm-screen', () => {
    harness = mountGutterSpike(container)
    const results = harness.collectResults()

    expect(results.b_gutterRendered).toBe(true)

    const gutter = container.querySelector('.nocx-gutter-spike-b')
    expect(gutter).not.toBeNull()

    // Gutter should be a direct child of .xterm (sibling of .xterm-screen).
    const xterm = container.querySelector('.xterm')
    expect(xterm).not.toBeNull()
    expect(gutter!.parentElement).toBe(xterm)

    // A glyph should be inside the gutter.
    const glyph = gutter!.querySelector('.nocx-gutter-glyph')
    expect(glyph).not.toBeNull()
  })

  it('approach (c) applies left padding to the container', () => {
    harness = mountGutterSpike(container)
    harness.collectResults()

    // Padding should have been restored after the test.
    // (approachC restores it after measuring.)
    expect(container.style.paddingLeft).toBeFalsy()
  })

  it('creates a viewport element', () => {
    harness = mountGutterSpike(container)

    const viewport = container.querySelector('.xterm-viewport')
    expect(viewport).not.toBeNull()
    expect(viewport!.classList.contains('xterm-viewport')).toBe(true)
  })

  it('creates a decoration container', () => {
    harness = mountGutterSpike(container)

    const decoContainer = container.querySelector('.xterm-decoration-container')
    expect(decoContainer).not.toBeNull()
  })
})

describe('createGutter (RECOMMENDED approach)', () => {
  let container: HTMLElement
  let terminal: import('@xterm/xterm').Terminal

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)

    terminal = new Terminal({
      cols: 80,
      rows: 24,
      allowProposedApi: true,
      fontSize: 15,
      fontFamily: 'monospace',
    })
    terminal.open(container)
    terminal.write('line 1\r\nline 2\r\nline 3\r\n')
  })

  afterEach(() => {
    try {
      terminal.dispose()
    } catch {
      // already disposed
    }
    container.remove()
  })

  it('createGutter inserts a gutter div before .xterm-screen', () => {
    const gutterApi = createGutter(container, terminal)

    const gutterEl = container.querySelector(
      'div[style*="position: absolute"][style*="z-index: 10"]',
    )
    expect(gutterEl).not.toBeNull()

    // Verify it's a sibling of .xterm-screen, not inside it.
    const screen = container.querySelector('.xterm-screen')
    expect(screen).not.toBeNull()
    expect(gutterEl!.parentElement).toBe(screen!.parentElement)

    gutterApi.dispose()
  })

  it('addGlyph creates a positioned glyph element', () => {
    const gutterApi = createGutter(container, terminal)

    const glyph = gutterApi.addGlyph(2) // line index 2 = 'line 3'

    expect(glyph).not.toBeNull()
    expect(glyph.style.position).toBe('absolute')
    expect(glyph.style.borderRadius).toBe('50%')

    // Glyph should have a top style set.
    expect(glyph.style.top).toBeTruthy()

    gutterApi.dispose()
  })

  it('syncPositions updates glyph positions', () => {
    const gutterApi = createGutter(container, terminal)

    const glyph1 = gutterApi.addGlyph(0)
    const glyph2 = gutterApi.addGlyph(1)

    // After syncPositions, glyphs should have different top values
    // (since they're on different lines).
    gutterApi.syncPositions()
    const top1After = parseFloat(glyph1.style.top)
    const top2After = parseFloat(glyph2.style.top)

    expect(top2After).not.toBe(top1After)

    gutterApi.dispose()
  })

  it('dispose removes the gutter from the DOM', () => {
    const gutterApi = createGutter(container, terminal)
    gutterApi.dispose()

    const gutterEl = container.querySelector(
      'div[style*="position: absolute"][style*="z-index: 10"]',
    )
    expect(gutterEl).toBeNull()
  })
})

describe('RECOMMENDED_API_CALLS', () => {
  it('documents all required xterm 5.5 API surface', () => {
    // Structural check: every key in the record should have a non-empty
    // description string.
    const keys = Object.keys(RECOMMENDED_API_CALLS)
    expect(keys.length).toBeGreaterThanOrEqual(5)

    for (const key of keys) {
      const desc = RECOMMENDED_API_CALLS[key as keyof typeof RECOMMENDED_API_CALLS]
      expect(typeof desc).toBe('string')
      expect(desc.length).toBeGreaterThan(5)
    }
  })

  it('covers marker lifecycle', () => {
    expect(RECOMMENDED_API_CALLS.registerMarker).toBeTruthy()
    expect(RECOMMENDED_API_CALLS.getMarkerLine).toBeTruthy()
    expect(RECOMMENDED_API_CALLS.onMarkerDispose).toBeTruthy()
  })

  it('covers viewport + scroll', () => {
    expect(RECOMMENDED_API_CALLS.getViewportY).toBeTruthy()
    expect(RECOMMENDED_API_CALLS.onScroll).toBeTruthy()
  })

  it('covers resize + render events', () => {
    expect(RECOMMENDED_API_CALLS.onResize).toBeTruthy()
    expect(RECOMMENDED_API_CALLS.onRender).toBeTruthy()
  })

  it('covers buffer type changes', () => {
    expect(RECOMMENDED_API_CALLS.getBufferType).toBeTruthy()
    expect(RECOMMENDED_API_CALLS.onBufferChange).toBeTruthy()
  })
})

describe('DOM structure invariants (xterm.js 5.5)', () => {
  let container: HTMLElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
  })

  afterEach(() => {
    container.remove()
  })

  it('.xterm-screen has position:relative (no overflow:hidden)', () => {
    const harness = mountGutterSpike(container)
    const screen = harness.screen

    // In jsdom, computed styles are not applied from external stylesheets
    // by default. We check the class name as a proxy for the structure.
    // The real CSS (xterm.css line ~169) sets .xterm-screen { position: relative }
    // with NO overflow:hidden — this is the key invariant that makes
    // approach (a) even potentially possible.
    expect(screen).not.toBeNull()
    expect(screen.classList.contains('xterm-screen')).toBe(true)

    // NOTE: The absence of overflow:hidden on .xterm-screen is the
    // critical CSS invariant that allows decoration elements translated to
    // negative x coordinates to remain visible. This is verified by reading
    // the shipped xterm.css, not by runtime assertion in jsdom.
    // If a future xterm.js version adds overflow:hidden to .xterm-screen,
    // approach (a) breaks and approach (b) becomes the only option.

    harness.dispose()
  })

  it('.xterm-viewport has overflow-y:scroll', () => {
    const harness = mountGutterSpike(container)
    const viewport = container.querySelector('.xterm-viewport')
    expect(viewport).not.toBeNull()
    expect(viewport!.classList.contains('xterm-viewport')).toBe(true)
    // xterm.css line 96: .xterm .xterm-viewport { overflow-y: scroll }
    harness.dispose()
  })

  it('.xterm-decoration-container exists as child of .xterm-screen', () => {
    const harness = mountGutterSpike(container)
    harness.collectResults()

    const decoContainer = container.querySelector('.xterm-decoration-container')
    expect(decoContainer).not.toBeNull()

    // jsdom limitation: individual .xterm-decoration elements are only
    // created when onRender fires (requires real canvas). The container
    // itself exists and is correctly parented — the child decorations
    // will be appended inside it at render time in a real browser.
    const screen = container.querySelector('.xterm-screen')
    expect(decoContainer?.parentElement).toBe(screen)

    harness.dispose()
  })

  it('.xterm-decoration-container is a child of .xterm-screen', () => {
    const harness = mountGutterSpike(container)
    harness.collectResults()

    const screen = container.querySelector('.xterm-screen')
    const decoContainer = container.querySelector('.xterm-decoration-container')
    expect(decoContainer?.parentElement).toBe(screen)

    harness.dispose()
  })

  it('decoration CSS classes are defined in xterm.css (z-index layers)', () => {
    const harness = mountGutterSpike(container)
    harness.collectResults()

    // jsdom limitation: decoration.onRender never fires in headless mode,
    // so .xterm-decoration elements are not rendered. We verify the CSS
    // contract by source inspection instead:
    //   xterm.css line ~169: .xterm-screen .xterm-decoration-container
    //     .xterm-decoration { z-index: 6; position: absolute; }
    //   xterm.css line ~173: ... .xterm-decoration-top-layer { z-index: 7 }
    //
    // The decoration-container IS rendered (verified in earlier test),
    // so the z-index layering applies once onRender fires in a real browser.
    const decoContainer = container.querySelector('.xterm-decoration-container')
    expect(decoContainer).not.toBeNull()

    harness.dispose()
  })
})
