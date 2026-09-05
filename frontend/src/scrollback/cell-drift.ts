// THE FROZEN-LINE DRIFT INSTRUMENT (nocx-4n6sj). Temporary, off by default,
// deleted with nocx-ec18.
//
// A frozen block is HTML in flow; the live region is a grid. The two agree
// on the font and disagree on the metric, so a line of N columns can lay out
// wider or narrower than N × cellWidth — measured on the owner's pane at one
// whole column over 152, which is a TUI frame whose corners do not meet.
//
// The fix is expensive (per-column boxes, a second serializer, virtualisation)
// and the owner's reading is that it fires "only on omp and only at the
// bottom". That may well be right. This instrument exists so the decision is
// made on a number instead of on either of our impressions — it measures and
// changes nothing.
//
// WHY NOT scrollWidth > clientWidth, the one-line version everyone reaches
// for first: serializeRange deliberately JOINS soft-wrapped rows into one
// logical line ("JOIN (owner directive)"), so a line legitimately wider than
// the block is indistinguishable from a mislaid one by overflow alone. The
// comparison has to be against the COLUMN COUNT the grid itself used, which
// is why serializeRange now hands it out.
//
// WHAT IS STORED: counters, bucket labels, and the offending grapheme
// clusters themselves. No command text, no output text, no paths. The
// clusters are the finding — a report naming "some symbol" would send the
// next reader back to reproduce it.

const EPSILON_PX = 0.5

/** How many lines of one block are measured. Every measured line is a forced
 *  layout read at freeze time, and a 10 000-line block would be a visible
 *  hitch in the very moment the block replaces the live region. Lines beyond
 *  the cap are COUNTED, not silently treated as clean. */
const LINE_CAP = 2000

/** Bound on the offender table. A week of dogfooding sees a few dozen
 *  distinct non-ASCII clusters; the cap is there so a run of mojibake cannot
 *  fill localStorage. */
export const MAX_GLYPHS = 128

export const STORAGE_KEY = 'nocx.cellDrift.v1'
export const ENABLED_KEY = 'nocx.cellDrift.enabled'

/** Absolute error in columns, bucketed. Labels are the report's vocabulary,
 *  so they are stored as written rather than derived at read time. */
const BUCKETS: Array<[string, number]> = [
  ['0-0.25', 0.25],
  ['0.25-0.5', 0.5],
  ['0.5-1', 1],
  ['1-2', 2],
  ['2-4', 4],
  ['4+', Infinity],
]

interface GlyphRecord {
  advancePx: number
  cellWidth: number
  lines: number
}

export interface DriftState {
  since: string
  blocks: number
  /** Lines actually measured. */
  lines: number
  /** Lines whose width missed their columns by more than sub-pixel noise. */
  drifted: number
  wider: number
  narrower: number
  /** Lines out by a WHOLE column or more — the ones a reader sees. */
  wideByAColumn: number
  /** Worst single-line absolute error, in columns. */
  worstCols: number
  buckets: Record<string, number>
  cappedLines: number
  glyphs: Record<string, GlyphRecord>
}

export interface LineSample {
  cols: number
  widthPx: number
}

export interface GlyphSample {
  cluster: string
  advancePx: number
  lines: number
}

export function emptyState(since: string): DriftState {
  return {
    since,
    blocks: 0,
    lines: 0,
    drifted: 0,
    wider: 0,
    narrower: 0,
    wideByAColumn: 0,
    worstCols: 0,
    buckets: {},
    cappedLines: 0,
    glyphs: {},
  }
}

export function accumulateLines(
  state: DriftState,
  cellWidth: number,
  samples: readonly LineSample[],
  cap: number = LINE_CAP,
): DriftState {
  const next: DriftState = { ...state, buckets: { ...state.buckets }, glyphs: { ...state.glyphs } }
  next.blocks += 1
  if (!(cellWidth > 0)) return next

  let measured = 0
  for (const s of samples) {
    // A line with no columns is a blank the walk kept; there is nothing to
    // be wrong about, and dividing by the grid would report Infinity.
    if (!(s.cols > 0)) continue
    if (measured >= cap) {
      next.cappedLines += 1
      continue
    }
    measured += 1
    next.lines += 1
    const errPx = s.widthPx - s.cols * cellWidth
    if (Math.abs(errPx) < EPSILON_PX) continue
    const errCols = Math.abs(errPx) / cellWidth
    next.drifted += 1
    if (errPx > 0) {
      next.wider += 1
      if (errPx >= cellWidth) next.wideByAColumn += 1
    } else {
      next.narrower += 1
    }
    if (errCols > next.worstCols) next.worstCols = errCols
    const label = BUCKETS.find(([, ceiling]) => errCols < ceiling)?.[0] ?? '4+'
    next.buckets[label] = (next.buckets[label] ?? 0) + 1
  }
  return next
}

export function accumulateGlyphs(
  state: DriftState,
  cellWidth: number,
  samples: readonly GlyphSample[],
): DriftState {
  const glyphs: Record<string, GlyphRecord> = { ...state.glyphs }
  for (const s of samples) {
    if (!(cellWidth > 0) || !(s.advancePx > 0) || s.cluster === '') continue
    const prior = glyphs[s.cluster]
    glyphs[s.cluster] = {
      // The advance is a property of the glyph and the metric, not of the
      // sighting: a re-probe under a new cell width replaces it rather than
      // averaging two different questions together.
      advancePx: s.advancePx,
      cellWidth,
      lines: (prior?.lines ?? 0) + s.lines,
    }
  }
  return { ...state, glyphs: evict(glyphs) }
}

/** Drop the least damaging clusters until the store fits. Damage, not
 *  novelty: a cluster that fits its cell is worth nothing to the reader
 *  however unusual it looks. */
function evict(glyphs: Record<string, GlyphRecord>): Record<string, GlyphRecord> {
  const keys = Object.keys(glyphs)
  if (keys.length <= MAX_GLYPHS) return glyphs
  const ranked = keys.sort((a, b) => impact(glyphs[b]) - impact(glyphs[a])).slice(0, MAX_GLYPHS)
  const kept: Record<string, GlyphRecord> = {}
  for (const k of ranked) kept[k] = glyphs[k]
  return kept
}

/** Columns lost to this cluster, read as charitably as the DOM allows: we
 *  cannot see which column count xterm gave it, so the damage is the SMALLER
 *  of the two misses. A cluster that lands exactly on one cell or exactly on
 *  two scores zero and is not an offender. */
function impact(g: GlyphRecord): number {
  return (
    Math.min(Math.abs(g.advancePx - g.cellWidth), Math.abs(g.advancePx - 2 * g.cellWidth)) * g.lines
  )
}

interface Offender {
  cluster: string
  codepoints: string
  advancePx: number
  missIfOneCol: number
  missIfTwoCols: number
  fitsNeither: boolean
  lines: number
}

export interface DriftReport {
  since: string
  blocks: number
  lines: number
  drifted: number
  driftedShare: string
  wider: number
  narrower: number
  wideByAColumn: number
  worstCols: number
  buckets: Record<string, number>
  cappedLines: number
  offenders: Offender[]
}

export function report(state: DriftState): DriftReport {
  const offenders = Object.entries(state.glyphs)
    .map(([cluster, g]) => {
      const missIfOneCol = g.advancePx - g.cellWidth
      const missIfTwoCols = g.advancePx - 2 * g.cellWidth
      return {
        cluster,
        codepoints: codepointsOf(cluster),
        advancePx: g.advancePx,
        missIfOneCol,
        missIfTwoCols,
        // BOTH numbers are reported and neither is chosen. Which column
        // count the grid gave a cluster is not visible from the DOM, and an
        // instrument that guessed it would be manufacturing the finding it
        // was built to check.
        fitsNeither: Math.abs(missIfOneCol) >= EPSILON_PX && Math.abs(missIfTwoCols) >= EPSILON_PX,
        lines: g.lines,
      }
    })
    .filter((o) => o.fitsNeither)
    .sort((a, b) => impactOf(b) - impactOf(a))
  return {
    since: state.since,
    blocks: state.blocks,
    lines: state.lines,
    drifted: state.drifted,
    driftedShare: state.lines > 0 ? `${((state.drifted / state.lines) * 100).toFixed(1)}%` : '0.0%',
    wider: state.wider,
    narrower: state.narrower,
    wideByAColumn: state.wideByAColumn,
    worstCols: state.worstCols,
    buckets: state.buckets,
    cappedLines: state.cappedLines,
    offenders,
  }
}

function impactOf(o: Offender): number {
  return Math.min(Math.abs(o.missIfOneCol), Math.abs(o.missIfTwoCols)) * o.lines
}

function codepointsOf(cluster: string): string {
  return [...cluster]
    .map((ch) => `U+${(ch.codePointAt(0) ?? 0).toString(16).toUpperCase().padStart(4, '0')}`)
    .join(' ')
}

// ── Storage ────────────────────────────────────────────────────────────────

export function isEnabled(): boolean {
  try {
    return localStorage.getItem(ENABLED_KEY) === '1'
  } catch {
    return false
  }
}

export function setEnabled(on: boolean): void {
  try {
    if (on) localStorage.setItem(ENABLED_KEY, '1')
    else localStorage.removeItem(ENABLED_KEY)
  } catch {
    /* a browser with storage denied simply has no instrument */
  }
}

export function loadState(): DriftState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === null) return emptyState(new Date().toISOString())
    const parsed = JSON.parse(raw) as Partial<DriftState>
    return { ...emptyState(new Date().toISOString()), ...parsed }
  } catch {
    return emptyState(new Date().toISOString())
  }
}

export function saveState(state: DriftState): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state))
  } catch {
    /* quota or denial: the week's sample is lost, the product is not */
  }
}

// ── Measurement (the layout half) ──────────────────────────────────────────

/** New clusters probed per block. Each probe is a layout read; a block full
 *  of mojibake must not turn one freeze into thousands of them. */
const PROBE_CAP = 64

const PROBE_REPEATS = 16
const PROBE_CLASS = 'cell-drift-probe'

export interface Measurers {
  lineWidth(el: HTMLElement): number
  clusterAdvance(host: HTMLElement, cluster: string): number
}

/** A hidden, unconstrained box INSIDE the block's own output, so whatever
 *  goes in it inherits the family, the size and the letter-spacing
 *  correction the shipped rows carry. `max-content` and `pre` are what make
 *  it a ruler: no wrapping, no container width. */
function probeHost(within: HTMLElement): HTMLElement {
  const existing = within.querySelector<HTMLElement>(`:scope > .${PROBE_CLASS}`)
  if (existing) return existing
  const host = within.ownerDocument.createElement('div')
  host.className = PROBE_CLASS
  host.style.cssText =
    'position:absolute;left:-99999px;top:0;width:max-content;white-space:pre;visibility:hidden;pointer-events:none'
  within.appendChild(host)
  return host
}

const domMeasurers: Measurers = {
  // NOT el.getBoundingClientRect(): `.term-line` is `display: block`, so its
  // border box is the CONTAINER's width and identical for every row —
  // an instrument that would have reported a constant and been believed.
  // And a Range over the row is no better once `terminal.wrapOutput` is on,
  // because a row wide enough to be interesting is exactly the row that has
  // wrapped, and its rects are then the visual fragments rather than the
  // advance. So the row is re-laid in a box that cannot wrap, and what is
  // measured is its INTRINSIC width — the number the grid comparison wants.
  lineWidth: (el) => {
    const host = probeHost(el.parentElement ?? el)
    const clone = el.cloneNode(true) as HTMLElement
    host.replaceChildren(clone)
    const width = clone.getBoundingClientRect().width
    host.replaceChildren()
    return width
  },
  clusterAdvance: (within, cluster) => {
    const host = probeHost(within)
    const probe = within.ownerDocument.createElement('span')
    probe.className = 'term-line'
    probe.textContent = cluster.repeat(PROBE_REPEATS)
    host.replaceChildren(probe)
    const width = probe.getBoundingClientRect().width
    host.replaceChildren()
    return width / PROBE_REPEATS
  },
}

/** Distinct non-ASCII grapheme clusters of a line. ASCII is excluded on
 *  purpose: it is the case the existing letter-spacing correction was
 *  calibrated on, and including it would drown the report in `a`. */
function clustersOf(text: string, seg: Intl.Segmenter): Set<string> {
  const out = new Set<string>()
  for (const { segment } of seg.segment(text)) {
    if (segment === '' || segment.codePointAt(0)! < 0x80) continue
    out.add(segment)
  }
  return out
}

/**
 * Measure one frozen block and fold it into the stored sample.
 *
 * Returns false and records NOTHING when the rows and the counts disagree —
 * something rewrote the DOM between serialization and here, and a count
 * paired with the wrong row would report drift that is only the pairing
 * being off. A silent wrong number is worse than no instrument.
 */
export function measureFrozenBlock(
  outputEl: HTMLElement,
  cols: readonly number[],
  cellWidth: number,
  m: Measurers = domMeasurers,
): boolean {
  if (!(cellWidth > 0)) return false
  const rows = Array.from(
    outputEl.querySelectorAll<HTMLElement>(`:scope > .term-line:not(.${PROBE_CLASS})`),
  )
  if (rows.length !== cols.length || rows.length === 0) return false

  const samples: LineSample[] = []
  for (let i = 0; i < rows.length; i++) {
    samples.push({ cols: cols[i], widthPx: m.lineWidth(rows[i]) })
  }

  // Which clusters to probe, and on how many lines each was seen. Counted
  // per LINE, not per occurrence: the report ranks by the reading a person
  // does, and a row of forty spinners is still one broken row.
  const seg = new Intl.Segmenter('en', { granularity: 'grapheme' })
  const seenOn = new Map<string, number>()
  for (const row of rows) {
    for (const cluster of clustersOf(row.textContent ?? '', seg)) {
      seenOn.set(cluster, (seenOn.get(cluster) ?? 0) + 1)
    }
  }

  const state = loadState()
  const probes: GlyphSample[] = []
  let budget = PROBE_CAP
  for (const [cluster, lines] of seenOn) {
    const known = state.glyphs[cluster]
    if (known !== undefined && known.cellWidth === cellWidth) {
      // Already measured under this metric: carry the sighting, skip the
      // layout read. This is what keeps a week of dogfooding affordable.
      probes.push({ cluster, advancePx: known.advancePx, lines })
      continue
    }
    if (budget <= 0) continue
    budget -= 1
    probes.push({ cluster, advancePx: m.clusterAdvance(outputEl, cluster), lines })
  }

  saveState(accumulateGlyphs(accumulateLines(state, cellWidth, samples), cellWidth, probes))
  outputEl.querySelector(`:scope > .${PROBE_CLASS}`)?.remove()
  return true
}

/** The freeze-time entry point: measure the block that just replaced the
 *  live region. Reads the cell width from the custom property the metric
 *  publisher already puts on the scrollback container, so the instrument
 *  and the layout it is checking agree on the grid by construction. */
export function recordFrozenBlock(blockEl: HTMLElement, cols: readonly number[]): void {
  if (!isEnabled()) return
  const out = blockEl.querySelector<HTMLElement>('.cmd-output')
  if (!out) return
  const cellWidth = Number.parseFloat(getComputedStyle(out).getPropertyValue('--term-cell-width'))
  if (!Number.isFinite(cellWidth) || cellWidth <= 0) return
  measureFrozenBlock(out, cols, cellWidth)
}

// ── The console surface ────────────────────────────────────────────────────
//
// A window global rather than a Settings control: this is a bead-scoped
// instrument that ships switched off and is deleted with nocx-ec18, and a
// toggle in the product would outlive it — as a wire contract, a stored
// setting and a row in a page nobody can explain in six months.

interface CellDriftApi {
  enable(): string
  disable(): string
  report(): DriftReport
  reset(): string
}

const cellDriftApi: CellDriftApi = {
  enable() {
    setEnabled(true)
    return 'frozen-line drift: ON. Work normally, then nocxCellDrift.report().'
  },
  disable() {
    setEnabled(false)
    return 'frozen-line drift: OFF. The sample is kept; reset() clears it.'
  },
  report() {
    return report(loadState())
  },
  reset() {
    saveState(emptyState(new Date().toISOString()))
    return 'frozen-line drift: sample cleared.'
  },
}

declare global {
  interface Window {
    nocxCellDrift?: CellDriftApi
  }
}

export function installCellDriftApi(target: Window = window): void {
  target.nocxCellDrift = cellDriftApi
}
