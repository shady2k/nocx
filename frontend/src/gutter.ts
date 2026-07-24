// Sibling-gutter overlay (ADR-0008 first increment). A narrow, pointer-events:none
// div beside the xterm screen, carrying one small glyph per command record on its
// row. Position sync is coalesced into a single requestAnimationFrame, culled to
// the viewport + overscan, and hidden entirely in the alt buffer.

import type { CommandRecord } from './command-ledger'
import type { TerminalRenderer } from './renderers/types'

const GUTTER_WIDTH_PX = 16
const OVERSCAN = 5 // rows above/below viewport to pre-render

export interface GlyphPosition {
  record: CommandRecord
  /** Pixel offset from the top of the gutter (top of viewport = 0). */
  top: number
}

/**
 * Pure helper: compute which glyphs are visible and their pixel positions.
 *
 * @param records  All ledger records.
 * @param viewportTopLine  Absolute buffer line at the top of the visible area.
 * @param rows     Number of visible rows.
 * @param overscan Extra rows above/below the viewport to include (avoids
 *                 pop-in during rapid scroll).
 * @returns Sorted array of {record, top} for visible, non-disposed records.
 */
export function visibleGlyphs(
  records: readonly CommandRecord[],
  viewportTopLine: number,
  rows: number,
  overscan: number,
): GlyphPosition[] {
  if (rows <= 0 || records.length === 0) return []

  const minLine = viewportTopLine - overscan
  const maxLine = viewportTopLine + rows + overscan

  const result: GlyphPosition[] = []
  for (const rec of records) {
    if (rec.disposed) continue
    const line = rec.lineOf()
    if (line === undefined) continue
    if (line < minLine || line >= maxLine) continue
    result.push({ record: rec, top: line - viewportTopLine })
  }
  return result
}

/** Status → CSS class mapping. Success = nearly invisible; failure = red; etc. */
function statusClass(status: CommandRecord['status']): string {
  switch (status) {
    case 'running':
      return 'nocx-gutter-running'
    case 'success':
      return 'nocx-gutter-success'
    case 'failure':
      return 'nocx-gutter-failure'
    case 'interrupted':
      return 'nocx-gutter-interrupted'
    case 'unknown':
      return 'nocx-gutter-unknown'
  }
}

export class Gutter {
  private _el: HTMLElement | null = null
  private _glyphs = new Map<number, HTMLElement>() // record id → glyph element
  private _rafId = 0
  private _records: readonly CommandRecord[] = []
  private _renderer: TerminalRenderer | null = null
  private _disposed = false

  /**
   * Create and mount the gutter element. Must be called after the renderer
   * has mounted (paneElement is available).
   */
  mount(renderer: TerminalRenderer): void {
    if (this._disposed || this._el) return
    this._renderer = renderer

    const pane = renderer.paneElement
    if (!pane) return

    const el = document.createElement('div')
    el.className = 'nocx-gutter'
    el.style.cssText =
      'position:absolute;left:0;top:0;bottom:0;' +
      `width:${GUTTER_WIDTH_PX}px;` +
      'pointer-events:none;z-index:10;overflow:hidden;'
    pane.style.position = pane.style.position || 'relative'
    pane.appendChild(el)
    this._el = el

    // Subscribe to sync events.
    renderer.onScroll?.(() => this._scheduleSync())
    renderer.onRender?.(() => this._scheduleSync())
    // onResize is already wired by the Tab — we expose sync() for the caller.
  }

  /** Feed the current records. Called whenever the ledger changes. */
  setRecords(records: readonly CommandRecord[]): void {
    this._records = records
    this._scheduleSync()
  }

  /** Force an immediate resync (call on resize, buffer change, etc.). */
  sync(): void {
    this._cancelRaf()
    this._paint()
  }

  /** Hide the gutter (alt buffer). Re-show on next setRecords or sync. */
  hide(): void {
    if (this._el) this._el.style.display = 'none'
  }

  /** Show the gutter (normal buffer). Schedules a sync. */
  show(): void {
    if (this._el) this._el.style.display = ''
    this._scheduleSync()
  }

  /** Dispose the gutter and all glyphs. Idempotent. */
  dispose(): void {
    this._disposed = true
    this._cancelRaf()
    this._glyphs.forEach((el) => el.remove())
    this._glyphs.clear()
    this._el?.remove()
    this._el = null
    this._renderer = null
  }

  // ── internal ──────────────────────────────────────────────────────────

  private _scheduleSync(): void {
    if (this._disposed || this._rafId !== 0) return
    this._rafId = window.requestAnimationFrame(() => {
      this._rafId = 0
      this._paint()
    })
  }

  private _cancelRaf(): void {
    if (this._rafId !== 0) {
      window.cancelAnimationFrame(this._rafId)
      this._rafId = 0
    }
  }

  private _paint(): void {
    const r = this._renderer
    const el = this._el
    if (!r || !el || this._disposed) return

    const ch = r.cellHeight ?? 15
    const vtl = r.viewportTopLine ?? 0
    const rows = r.rows

    const positions = visibleGlyphs(this._records, vtl, rows, OVERSCAN)

    // Track which glyphs we need.
    const needed = new Set<number>()
    for (const { record, top } of positions) {
      needed.add(record.id)
      let glyph = this._glyphs.get(record.id)

      if (!glyph) {
        glyph = document.createElement('div')
        glyph.className = `nocx-gutter-glyph ${statusClass(record.status)}`
        glyph.style.cssText =
          'position:absolute;left:0;' +
          `width:3px;height:${Math.round(ch)}px;` +
          'border-radius:1px;'
        el.appendChild(glyph)
        this._glyphs.set(record.id, glyph)
      }

      // Always update class (status may have changed).
      glyph.className = `nocx-gutter-glyph ${statusClass(record.status)}`
      glyph.style.top = `${Math.round(top * ch)}px`
    }

    // Remove glyphs that are no longer visible or whose records are disposed.
    for (const [id, glyph] of this._glyphs) {
      if (!needed.has(id)) {
        glyph.remove()
        this._glyphs.delete(id)
      }
    }
  }
}
