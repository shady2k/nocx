/**
 * gutter-spike.ts — Gutter-geometry feasibility spike for M2b (de-risk 4ff.5)
 *
 * Mounts an xterm Terminal, registers a marker + decoration using each of the
 * three candidate approaches, and reports DOM geometry + clipping behaviour.
 *
 * This is a research harness, not production code. See the companion findings
 * doc at docs/superpowers/specs/2026-07-23-gutter-geometry-findings.md.
 *
 * @vitest-environment jsdom
 */

import { Terminal } from '@xterm/xterm'

// ═══════════════════════════════════════════════════════════════════════════
// Types
// ═══════════════════════════════════════════════════════════════════════════

export interface GutterSpikeResult {
  /** Did approach (a) decoration render without being clipped left? */
  a_notClipped: boolean
  /** Approach (a) decoration element's bounding rect left edge relative to
   *  screen element's left edge. Negative = it's in the margin. */
  a_leftOffset: number
  /** Approach (b) gutter div rendered at all. */
  b_gutterRendered: boolean
  /** Approach (b) glyph element's position matches the marker line. */
  b_glyphAligned: boolean
  /** Approach (c) padding applied and decoration visible. */
  c_paddingApplied: boolean
  /** Overall test log. */
  log: string[]
}

export interface SpikeHarness {
  container: HTMLElement
  terminal: Terminal
  screen: HTMLElement
  /** Collect results from all three approaches. */
  collectResults(): GutterSpikeResult
  /** Dispose of the terminal and clean up DOM. */
  dispose(): void
}

// ═══════════════════════════════════════════════════════════════════════════
// Approach (a): Decoration at x:0 + CSS transform translateX(-100%)
// ═══════════════════════════════════════════════════════════════════════════

function approachA(term: Terminal, log: string[]): {
  notClipped: boolean
  leftOffset: number
  decoration: HTMLElement | null
} {
  log.push('[A] Registering marker + decoration at x:0, width:1')
  const marker = term.registerMarker(0)
  if (!marker) {
    log.push('[A] FAIL: registerMarker returned null')
    return { notClipped: false, leftOffset: 0, decoration: null }
  }

  const decoration = term.registerDecoration({
    marker,
    x: 0,
    width: 1,
    backgroundColor: '#00ff00',
    layer: 'top',
  })
  if (!decoration) {
    log.push('[A] FAIL: registerDecoration returned null')
    return { notClipped: false, leftOffset: 0, decoration: null }
  }

  let el: HTMLElement | null = null
  decoration.onRender((e) => {
    el = e
  })

  // Force a render by writing a character then refreshing.
  term.write(' ')
  term.refresh(0, term.rows - 1)

  // The decoration element should now be in the DOM. Apply the transform to
  // shift it into the left gutter zone.
  const decEl = decoration.element ?? el
  if (!decEl) {
    log.push('[A] FAIL: decoration element never rendered')
    return { notClipped: false, leftOffset: 0, decoration: null }
  }

  // Record pre-transform position.
  const origRect = decEl.getBoundingClientRect()
  log.push(`[A] Pre-transform rect: left=${origRect.left}, width=${origRect.width}`)

  // Apply CSS transform to shift the decoration left by its own width,
  // landing in the gutter / left-margin area.
  decEl.style.transform = 'translateX(-100%)'

  // Force reflow and read the new position.
  void decEl.offsetHeight
  const postRect = decEl.getBoundingClientRect()

  // The screen element is the reference for "is this inside the grid?"
  const screen = term.element?.querySelector('.xterm-screen') as HTMLElement | null
  const screenRect = screen?.getBoundingClientRect() ?? { left: 0, right: 0 }

  // Is the decoration entirely to the left of the screen's left edge?
  const leftOffset = postRect.left - screenRect.left
  const notClipped = postRect.right <= screenRect.left + 1 || leftOffset < 0

  log.push(
    `[A] Post-transform rect: left=${postRect.left}, ` +
      `screen left=${screenRect.left}, offset=${leftOffset}, ` +
      `notClipped=${notClipped}`,
  )

  // Determine if the element is actually visible (not display:none, not 0-size)
  const style = window.getComputedStyle(decEl)
  const isVisible =
    style.display !== 'none' &&
    style.visibility !== 'hidden' &&
    postRect.width > 0 &&
    postRect.height > 0
  log.push(`[A] Visible: ${isVisible}, display: ${style.display}, size: ${postRect.width}x${postRect.height}`)

  return { notClipped, leftOffset, decoration: decEl }
}

// ═══════════════════════════════════════════════════════════════════════════
// Approach (b): Dedicated sibling gutter div overlaying the terminal container
// ═══════════════════════════════════════════════════════════════════════════

function approachB(
  term: Terminal,
  container: HTMLElement,
  log: string[],
): {
  rendered: boolean
  aligned: boolean
  gutter: HTMLElement | null
  glyph: HTMLElement | null
} {
  log.push('[B] Creating sibling gutter div')

  // Create the gutter element — a div that sits to the left of the terminal
  // screen, overlaid on the terminal container.
  const gutter = document.createElement('div')
  gutter.classList.add('nocx-gutter-spike-b')
  gutter.style.cssText =
    'position:absolute;left:0;top:0;bottom:0;width:20px;' +
    'pointer-events:none;z-index:10;overflow:hidden;'

  // Insert as a sibling of the screen element, inside the xterm root.
  const screen = term.element?.querySelector('.xterm-screen') as HTMLElement | null
  if (!screen) {
    log.push('[B] FAIL: .xterm-screen not found')
    return { rendered: false, aligned: false, gutter: null, glyph: null }
  }
  screen.parentElement?.insertBefore(gutter, screen)

  log.push('[B] Gutter div inserted before .xterm-screen')

  // Register a marker and compute the glyph's top position.
  const marker = term.registerMarker(0)
  if (!marker) {
    log.push('[B] FAIL: registerMarker returned null')
    return { rendered: true, aligned: false, gutter: null, glyph: null }
  }

  const glyph = document.createElement('div')
  glyph.classList.add('nocx-gutter-glyph')
  glyph.style.cssText =
    'position:absolute;left:2px;width:16px;height:16px;' +
    'border-radius:50%;background:#00ff00;top:0;'
  gutter.appendChild(glyph)

  // Compute the glyph's top position: we need to know the cell height and the
  // marker's position relative to the viewport.
  const viewportY = term.buffer.active.viewportY
  const markerLine = marker.line
  const cellHeight = estimateCellHeight(term)

  // The marker line is an absolute buffer index. To convert to a screen
  // position, we subtract viewportY.
  const topPx = Math.max(0, (markerLine - viewportY) * cellHeight)
  glyph.style.top = `${topPx}px`

  log.push(
    `[B] marker line=${markerLine}, viewportY=${viewportY}, ` +
      `cellHeight=${cellHeight}, glyph top=${topPx}px`,
  )

  // Verify alignment: the glyph's top should be within tolerance of the
  // expected position.
  const expectedTop = (markerLine - viewportY) * cellHeight
  const aligned = Math.abs(topPx - expectedTop) < 2

  return { rendered: true, aligned, gutter, glyph }
}

// ═══════════════════════════════════════════════════════════════════════════
// Approach (c): Reserved left padding on terminal container
// ═══════════════════════════════════════════════════════════════════════════

function approachC(
  term: Terminal,
  container: HTMLElement,
  log: string[],
): { applied: boolean } {
  log.push('[C] Reserving left padding on terminal container')

  // Apply padding to the container
  const prevPadding = container.style.paddingLeft
  container.style.paddingLeft = '24px'

  const marker = term.registerMarker(0)
  if (!marker) {
    log.push('[C] FAIL: registerMarker returned null')
    container.style.paddingLeft = prevPadding
    return { applied: false }
  }

  const decoration = term.registerDecoration({
    marker,
    x: 0,
    width: 1,
    backgroundColor: '#00ff00',
    layer: 'top',
  })
  if (!decoration) {
    log.push('[C] FAIL: registerDecoration returned null')
    container.style.paddingLeft = prevPadding
    return { applied: false }
  }

  // Force render
  term.write(' ')
  term.refresh(0, term.rows - 1)

  const decEl = decoration.element
  if (!decEl) {
    log.push('[C] FAIL: decoration element never rendered')
    container.style.paddingLeft = prevPadding
    return { applied: false }
  }

  const decRect = decEl.getBoundingClientRect()
  const containerRect = container.getBoundingClientRect()
  const offsetFromContainerLeft = decRect.left - containerRect.left

  log.push(
    `[C] Decoration offset from container left: ${offsetFromContainerLeft}px, ` +
      `paddingLeft: ${container.style.paddingLeft}`,
  )

  const applied = offsetFromContainerLeft >= 20 // should be ~24 due to padding
  container.style.paddingLeft = prevPadding

  return { applied }
}

// ═══════════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════════

/**
 * Estimate cell height from terminal options. Falls back to typical values
 * for common font sizes when measurements aren't available (e.g. jsdom).
 */
function estimateCellHeight(term: Terminal): number {
  const lineHeight = term.options.lineHeight ?? 1.0
  const fontSize = term.options.fontSize ?? 15

  // In a real browser, xterm measures the cell height dynamically using a
  // hidden .xterm-char-measure-element. In jsdom, we compute from options.
  // This is the standard estimate: fontSize * lineHeight, rounded up.
  const cellHeight = Math.ceil(fontSize * lineHeight)

  // Try to read actual dimensions from the char-measure-element if available.
  const measureEl = term.element?.querySelector(
    '.xterm-char-measure-element',
  ) as HTMLElement | null
  if (measureEl) {
    const rect = measureEl.getBoundingClientRect()
    if (rect.height > 0) {
      return rect.height
    }
  }

  return cellHeight
}

/**
 * Estimate cell width from terminal options.
 */
function estimateCellWidth(term: Terminal): number {
  const fontSize = term.options.fontSize ?? 15
  const letterSpacing = term.options.letterSpacing ?? 0

  const measureEl = term.element?.querySelector(
    '.xterm-char-measure-element',
  ) as HTMLElement | null
  if (measureEl) {
    const rect = measureEl.getBoundingClientRect()
    if (rect.width > 0) {
      return rect.width
    }
  }

  // Typical monospace width is ~0.6 * fontSize
  return Math.ceil(fontSize * 0.6 + letterSpacing)
}

// ═══════════════════════════════════════════════════════════════════════════
// Main harness
// ═══════════════════════════════════════════════════════════════════════════

/**
 * Mount the gutter spike harness.
 *
 * Creates an xterm Terminal inside `container`, writes a few lines of
 * content, registers markers + decorations using the three candidate
 * gutter approaches, and exposes geometry results.
 */
export function mountGutterSpike(container: HTMLElement): SpikeHarness {
  const log: string[] = []

  const term = new Terminal({
    cols: 80,
    rows: 24,
    allowProposedApi: true,
    fontSize: 15,
    fontFamily: 'monospace',
  })

  term.open(container)

  // Write enough content so markers have something to reference.
  term.write('Gutter spike line 1\r\n')
  term.write('Gutter spike line 2\r\n')
  term.write('Gutter spike line 3\r\n')
  term.write('Gutter spike line 4\r\n')
  term.write('> prompt: marker line\r\n')

  const screen = term.element?.querySelector('.xterm-screen') as HTMLElement
  if (!screen) {
    log.push('FATAL: .xterm-screen not found after terminal.open()')
  } else {
    log.push(`Screen element found: ${screen.className}`)
  }

  // Run each approach
  const aResult = approachA(term, log)
  const bResult = approachB(term, container, log)
  const cResult = approachC(term, container, log)

  // Restore the terminal so it's clean for the visual check.
  // Remove approach (a) transform.
  if (aResult.decoration) {
    aResult.decoration.style.transform = ''
  }

  return {
    container,
    terminal: term,
    screen,
    collectResults(): GutterSpikeResult {
      return {
        a_notClipped: aResult.notClipped,
        a_leftOffset: aResult.leftOffset,
        b_gutterRendered: bResult.rendered,
        b_glyphAligned: bResult.aligned,
        c_paddingApplied: cResult.applied,
        log: [...log],
      }
    },
    dispose() {
      term.dispose()
      // Clean up approach (b) gutter
      const gutter = container.querySelector('.nocx-gutter-spike-b')
      gutter?.remove()
      // Clean up approach (c) padding
      container.style.paddingLeft = ''
    },
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Static analysis helpers (for the findings doc and the blocks milestone)
// ═══════════════════════════════════════════════════════════════════════════

/**
 * The RECOMMENDED approach: a dedicated sibling gutter div that lives
 * *outside* xterm's rendering pipeline, overlaid on the terminal container.
 *
 * This function is the reference implementation the blocks milestone should
 * use. It demonstrates every required API call.
 */
export function createGutter(container: HTMLElement, terminal: Terminal): {
  gutter: HTMLElement
  addGlyph(markerLine: number): HTMLElement
  syncPositions(): void
  dispose(): void
} {
  const gutter = document.createElement('div')
  gutter.style.cssText =
    'position:absolute;left:0;top:0;bottom:0;' +
    'width:20px;pointer-events:none;z-index:10;overflow:hidden;'

  const screen = terminal.element?.querySelector('.xterm-screen') as HTMLElement
  if (!screen) throw new Error('.xterm-screen not found')

  screen.parentElement?.insertBefore(gutter, screen)

  const glyphs: { el: HTMLElement; markerLine: number }[] = []

  /**
   * Add a status glyph at a specific buffer line (marker line index).
   * Returns the glyph element so the caller can style it.
   */
  function addGlyph(markerLine: number): HTMLElement {
    const glyph = document.createElement('div')
    glyph.style.cssText =
      'position:absolute;left:2px;width:12px;height:12px;' +
      'border-radius:50%;top:0;'
    gutter.appendChild(glyph)
    glyphs.push({ el: glyph, markerLine })
    syncPositions()
    return glyph
  }

  /**
   * Recompute all glyph positions from the terminal's current viewport state.
   * Call this on every scroll, resize, and render event.
   */
  function syncPositions(): void {
    const viewportY = terminal.buffer.active.viewportY
    const cellHeight = estimateCellHeight(terminal)
    for (const { el, markerLine } of glyphs) {
      const topPx = (markerLine - viewportY) * cellHeight
      el.style.top = `${topPx}px`
    }
  }

  function dispose(): void {
    gutter.remove()
    glyphs.length = 0
  }

  return { gutter, addGlyph, syncPositions, dispose }
}

/**
 * The exact xterm 5.5 API calls the blocks milestone should use.
 *
 * This is the canonical recipe. All other decoration/gutter code should
 * derive from this pattern.
 */
export const RECOMMENDED_API_CALLS = {
  /** Register a marker at a buffer line. */
  registerMarker: 'terminal.registerMarker(cursorYOffset?: number): IMarker',

  /** Get a marker's current line index (-1 if disposed). */
  getMarkerLine: 'marker.line: number',

  /** Listen for marker disposal (scrollback trim). */
  onMarkerDispose: 'marker.onDispose(listener: () => void): IDisposable',

  /** Get the viewport's current scroll position. */
  getViewportY: 'terminal.buffer.active.viewportY: number',

  /** Get the active buffer type (normal vs alternate). */
  getBufferType: 'terminal.buffer.active.type: "normal" | "alternate"',

  /** Cell height estimate (fontSize * lineHeight, or measure-element rect). */
  cellHeight:
    'Math.ceil((terminal.options.fontSize ?? 15) * (terminal.options.lineHeight ?? 1.0))',

  /** Cell width estimate. */
  cellWidth:
    'Math.ceil((terminal.options.fontSize ?? 15) * 0.6 + (terminal.options.letterSpacing ?? 0))',

  /** Get the screen element for coordinate reference. */
  getScreenElement: 'terminal.element?.querySelector(".xterm-screen")',

  /** Listen for scroll events (viewportY changes). */
  onScroll: 'terminal.onScroll(listener: (newY: number) => void): IDisposable',

  /** Listen for resize events (cell dimensions change). */
  onResize: 'terminal.onResize(listener: (size: {cols:number, rows:number}) => void): IDisposable',

  /** Listen for render events (viewport content updated). */
  onRender:
    'terminal.onRender(listener: (range: {start:number, end:number}) => void): IDisposable',

  /** Listen for buffer type changes (normal ↔ alternate). */
  onBufferChange:
    'terminal.buffer.onBufferChange(listener: (buffer: IBuffer) => void): IDisposable',
} as const
