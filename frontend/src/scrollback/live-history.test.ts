// @vitest-environment jsdom
// The live tier's scrollback surface (nocx-zg3k3.10.4): the discriminating
// facts the acceptance names, each pinned against a stub page source that
// speaks the real seam's shape — the result and its rows document as two
// planes, the rows document free to land before the result that names it.
import { describe, expect, it, vi } from 'vitest'
import {
  AWAIT_ROWS_MS,
  HEAD_REFRESH_MS,
  LiveHistorySurface,
  type LiveHistoryPageSource,
} from './live-history'
import type { SessionHistoryPage } from '../generated/session.historyPage'
import type { SessionHistoryPageRows } from '../generated/session.historyPageRows'
import { styleOf, wireRowOf, type CellSpec } from '../painter/fixtures'

const STYLE = styleOf({ foreground: { kind: 1, palette: 7, rgb: { r: 0, g: 0, b: 0 } } })

/** One wire row of plain columns. The wire row stops at its last explicit
 *  column — the padding to geometry.cols is the decoder's act, exactly as
 *  it is for a frame's rows. */
function textRow(text: string): SessionHistoryPageRows['rows'][number] {
  const cells: CellSpec[] = Array.from(text, (ch) => [ch, 1, true, STYLE])
  return wireRowOf(cells)
}

interface ScriptedPage {
  pageId: string
  result: SessionHistoryPage
  rows: SessionHistoryPageRows['rows']
}

/** A page source over a fixed script of pages, addressed the way the
 *  runtime addresses them: before = null for the head, before = the
 *  previous answer's start for the next page backwards. `holdRows` keeps
 *  the rows document undelivered so a test can land it later — the two
 *  planes of one page, ordered by one socket, one FIFO, in either order. */
function scriptedSource(
  pages: ScriptedPage[],
  opts: { holdRows?: boolean; holdResult?: boolean } = {},
): LiveHistoryPageSource & {
  requested: (number | null)[]
  deliverRows: (pageId: string) => void
  resolveResult: (pageId: string) => void
} {
  const requested: (number | null)[] = []
  let rowsCb: ((doc: SessionHistoryPageRows) => void) | null = null
  // Head requests consume the script in order — an empty history's first
  // head read and the refresh output arms are different answers.
  let nextHead = 0
  const held = new Map<string, (page: SessionHistoryPage) => void>()
  return {
    requested,
    deliverRows: (pageId) => {
      const hit = pages.find((p) => p.pageId === pageId)
      expect(hit, `no scripted page carries ${pageId}`).toBeTruthy()
      rowsCb?.({ pageId, rows: hit!.rows })
    },
    resolveResult: (pageId) => {
      const resolve = held.get(pageId)
      const hit = pages.find((p) => p.pageId === pageId)
      expect(resolve, `no held request for ${pageId}`).toBeTruthy()
      resolve!(hit!.result)
    },
    historyPage(before, limit) {
      requested.push(before)
      // before=B is answered by the page whose interval ENDS at B — the
      // runtime's own rule ("the next page backwards is Before = Start").
      const hit = before === null ? pages[nextHead++] : pages.find((p) => p.result.end === before)
      expect(hit, `no scripted page answers before=${before}`).toBeTruthy()
      expect(limit).toBeLessThanOrEqual(64)
      // The rows document rides the carrier and lands BEFORE the result
      // names it — one socket, one FIFO — whether or not the result is
      // held back.
      if (opts.holdRows !== true) rowsCb?.({ pageId: hit!.pageId, rows: hit!.rows })
      if (opts.holdResult === true) {
        // The executor form: Promise.withResolvers needs an ES2024 lib and
        // the project targets ES2021.
        let resolve!: (page: SessionHistoryPage) => void
        const promise = new Promise<SessionHistoryPage>((done) => {
          resolve = done
        })
        held.set(hit!.pageId, resolve)
        return promise
      }
      return Promise.resolve(hit!.result)
    },
    onHistoryPageRows(cb) {
      rowsCb = cb
    },
  }
}

function pageOf(
  id: string,
  start: number,
  count: number,
  floor: number,
  more: boolean,
): ScriptedPage {
  const rows: SessionHistoryPageRows['rows'] = []
  for (let i = 0; i < count; i++) rows.push(textRow(`L${String(start + i).padStart(6, '0')}`))
  return {
    pageId: id,
    result: { pageId: id, start, end: start + count, floor, more, durableThrough: null },
    rows,
  }
}

const ROW_PX = 20

function mount(): {
  surface: LiveHistorySurface
  scroller: HTMLElement
} {
  const stack = document.createElement('div')
  const live = document.createElement('div')
  live.className = 'xterm-live-container'
  stack.appendChild(live)
  const scroller = document.createElement('div')
  scroller.appendChild(stack)
  const surface = new LiveHistorySurface({
    scroller,
    stack,
    columns: () => 80,
    metric: () => null,
  })
  // jsdom lays nothing out; the surface reads exactly three numbers off
  // the scroller, so the geometry the anchor math needs is named here:
  // the viewport is fixed and the content is the live pane's box plus one
  // ROW_PX per painted history row.
  const VIEWPORT_PX = 240
  Object.defineProperty(scroller, 'clientHeight', { value: VIEWPORT_PX, configurable: true })
  Object.defineProperty(scroller, 'scrollHeight', {
    configurable: true,
    get: () => VIEWPORT_PX + surface.el.querySelectorAll('.term-grid-row').length * ROW_PX,
  })
  let top = 0
  Object.defineProperty(scroller, 'scrollTop', {
    get: () => top,
    set: (v: number) => {
      // A real scroller clamps to its content: when the surface drops
      // pages, the browser pulls the reader back to what remains. jsdom
      // does not lay out, so the clamp is named here instead.
      top = Math.max(0, Math.min(v, scroller.scrollHeight - scroller.clientHeight))
    },
    configurable: true,
  })
  return { surface, scroller }
}

/** The reader scrolled to the live end, as every pane starts. */
function atBottom(scroller: HTMLElement): void {
  scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight
}

/** A scroll gesture near the top — the gesture that asks for more. */
function scrollNearTop(scroller: HTMLElement, to: number): void {
  scroller.scrollTop = to
  scroller.dispatchEvent(new Event('scroll'))
}

async function settle(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

describe('live-history', () => {
  it('pages the emulator to the floor: every row that left the screen is found, oldest painted first', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    // 96 rows left the screen: the head page [32,96), then the page below
    // it [0,32), whose answer names more=false — the floor's word.
    const source = scriptedSource([
      pageOf('a'.repeat(32), 32, 64, 0, true),
      pageOf('b'.repeat(32), 0, 32, 0, false),
    ])
    surface.setMode('unstructured')
    surface.bind(source)
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(64)

    // The reader walks up; near the top the surface asks for the page
    // below, addressed at the first answer's start.
    scrollNearTop(scroller, 100)
    await settle()

    const rows = surface.el.querySelectorAll('.term-grid-row')
    expect(rows.length).toBe(96)
    expect(rows[0].textContent).toContain('L000000')
    expect(rows[95].textContent).toContain('L000095')
    expect(source.requested).toEqual([null, 32])
    // Nothing further is asked: more=false is the emulator's own end.
    scrollNearTop(scroller, 0)
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(96)
  })

  it('keeps the reader anchored: a page installed above adds its height to scrollTop', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    expect(scroller.scrollTop).toBe(0)
    surface.setMode('unstructured')
    surface.bind(scriptedSource([pageOf('c'.repeat(32), 0, 30, 0, false)]))
    await settle()

    // One page of 30 rows painted above the live end: the reader who was
    // at the live end is still at it — scrollTop grew by exactly the rows
    // added above (one rule; a reader mid-history keeps their rows the
    // same way), and the scrollbar now has somewhere to go.
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
    expect(scroller.scrollTop).toBe(30 * ROW_PX)
  })

  it('installs a page only when BOTH planes landed: the rows document may arrive first', async () => {
    const source = scriptedSource([pageOf('d'.repeat(32), 0, 10, 0, false)], {
      holdRows: true,
    })
    const { surface } = mount()
    surface.setMode('unstructured')
    surface.bind(source)
    await settle()

    // The result landed, its rows document did not: nothing is painted,
    // and the page is not lost — it installs when the document arrives.
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(0)
    source.deliverRows('d'.repeat(32))
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(10)
  })

  it('shows the surface only in the unstructured mode', async () => {
    const { surface } = mount()
    expect(surface.el.hidden).toBe(true)
    surface.setMode('unstructured')
    expect(surface.el.hidden).toBe(false)
    surface.bind(scriptedSource([pageOf('e'.repeat(32), 0, 5, 0, false)]))
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(5)

    // A full-screen program takes the pane: the surface hides what it
    // keeps (the painted rows stay, the mode class and the hidden flag
    // take the element out of the flow). The cascade half — that a
    // full-screen program's pane really shows nothing of the primary's
    // history — is the real browser's to prove, and the e2e spec does.
    surface.setMode('other')
    expect(surface.el.hidden).toBe(true)
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(5)
  })

  it('a sighted erase-saved-lines drops everything painted and invents no boundary', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    surface.setMode('unstructured')
    surface.bind(scriptedSource([pageOf('f'.repeat(32), 0, 20, 0, false)]))
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(20)

    surface.cleared()
    expect(surface.el.querySelectorAll('.live-history-page').length).toBe(0)
    expect(surface.el.childElementCount).toBe(0)
  })

  it('a floor the painted rows never saw prunes them: a clear between two fetches is caught at paging time', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    // The head page [64,128), painted. The emulator then erased its saved
    // lines; the next answer — asked at before=64 — is the empty interval
    // at the cursor with the floor stated at the head (128), more=false.
    surface.setMode('unstructured')
    surface.bind(
      scriptedSource([
        pageOf('g'.repeat(32), 64, 64, 0, true),
        pageOf('h'.repeat(32), 64, 0, 128, false),
      ]),
    )
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(64)

    scrollNearTop(scroller, 100)
    await settle()

    // Everything painted was below the floor the fresh answer named, so
    // the surface is empty — not short, not boundary-marked — and the
    // paging stopped at the emulator's own end.
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(0)
    expect(surface.el.querySelectorAll('.live-history-page').length).toBe(0)
  })

  it('output arriving while the reader is away re-arms paging on the return to the live end', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    surface.setMode('unstructured')
    // Two head reads: the one that painted the past, and the one the
    // return to the live end asks for after output moved the head.
    const source = scriptedSource([
      pageOf('i'.repeat(32), 64, 10, 0, true),
      pageOf('j'.repeat(32), 74, 10, 0, true),
    ])
    surface.bind(source)
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(10)

    // Rows departed while the reader was away from the live end...
    surface.noteOutput()
    surface.tailReengaged()
    await settle()
    // ...so the painted past was dropped and the head is read again: the
    // next scroll-up reads what the emulator holds NOW — every printed
    // line, not the ones that existed when the reader left.
    expect(source.requested).toEqual([null, null])
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(10)
  })

  it('output departs rows while nothing is painted: the head is re-read without any scroll', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      atBottom(scroller)
      surface.setMode('unstructured')
      // The pane bound against an empty history: the head page answers
      // empty, the surface is exhausted, and nothing is scrollable.
      const source = scriptedSource([
        pageOf('k'.repeat(32), 0, 0, 0, false),
        pageOf('l'.repeat(32), 0, 30, 0, false),
      ])
      surface.bind(source)
      await settle()
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(0)
      expect(source.requested).toEqual([null])

      // Output departs its first rows into history. Nothing is above the
      // live rectangle, so NO scroll event can fire — and none is
      // dispatched here. Three chunks land inside one coalescing window.
      surface.noteOutput()
      surface.noteOutput()
      surface.noteOutput()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()

      // The empty surface asked for the head ONCE — one request per burst,
      // not one per chunk — and what came back is painted above the live
      // rectangle, so the wheel now has somewhere to go. The reader, who
      // was at the live end, still is.
      expect(source.requested).toEqual([null, null])
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
      expect(scroller.scrollTop).toBe(30 * ROW_PX)
    } finally {
      vi.useRealTimers()
    }
  })

  it('output departs while the first head read is in flight: the refresh outlives the flight and re-reads', async () => {
    vi.useFakeTimers()
    try {
      const { surface } = mount()
      surface.setMode('unstructured')
      // The bind's head read A is on the wire, held there — and it will
      // answer for the buffer AS IT WAS: empty.
      const source = scriptedSource(
        [pageOf('b1'.repeat(16), 0, 0, 0, false), pageOf('b2'.repeat(16), 0, 30, 0, false)],
        { holdResult: true },
      )
      surface.bind(source)
      await settle()
      expect(source.requested).toEqual([null])

      // Output departs rows while A is still flying; the refresh arms
      // behind it.
      surface.noteOutput()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      // The timer fired into the flight and was NOT lost: nothing was
      // asked yet, and the intent stands.
      expect(source.requested).toEqual([null])

      // A lands — the empty pre-output snapshot. Its own more=false must
      // not latch exhaustion over rows that departed after its read, and
      // the retained refresh re-reads the head WITHOUT any further output.
      source.resolveResult('b1'.repeat(16))
      await settle()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      // The re-read left while nothing else was asked; its answer lands
      // the same way the first one did — by being delivered.
      source.resolveResult('b2'.repeat(16))
      await settle()

      expect(source.requested).toEqual([null, null])
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
      // And the history is scrollable now: a reader at the live end is
      // still at it, with rows above to scroll into.
      expect(surface.el.querySelectorAll('.live-history-page').length).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('remembers a departure while the initial head read is in flight', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      surface.setMode('unstructured')
      const first = pageOf('v'.repeat(32), 64, 30, 0, true)
      const second = pageOf('w'.repeat(32), 94, 30, 0, true)
      const source = scriptedSource([first, second], { holdResult: true })
      surface.bind(source)
      await settle()
      expect(source.requested).toEqual([null])

      // The live pane has a little more content than its viewport while the
      // page result is outstanding. The first scroll crosses the live end.
      // The read must not make that departure invisible just because its
      // promise has not settled yet.
      Object.defineProperty(scroller, 'scrollHeight', {
        configurable: true,
        get: () =>
          scroller.clientHeight +
          surface.el.querySelectorAll('.term-grid-row').length * ROW_PX +
          ROW_PX,
      })
      scroller.scrollTop = 0
      scroller.dispatchEvent(new Event('scroll'))
      surface.noteOutput()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      expect(source.requested).toEqual([null])

      // The old read lands after output advanced the head. Its page is
      // painted, but neither that read nor its trailing refresh may tear it
      // down under the reader who already left the live end.
      source.resolveResult(first.pageId)
      await settle()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()

      expect(source.requested).toEqual([null])
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a result whose rows document never arrives stops blocking the next gesture', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      surface.setMode('unstructured')
      // Both pages keep their rows documents: the first result waits for
      // one forever, until the watchdog gives the page back.
      const source = scriptedSource(
        [pageOf('m'.repeat(32), 64, 10, 0, true), pageOf('n'.repeat(32), 64, 0, 0, false)],
        { holdRows: true },
      )
      surface.bind(source)
      await settle()
      expect(source.requested).toEqual([null])

      // While the page waits, a gesture pages nothing: the next request
      // would chain its cursor from a page that has not landed.
      scroller.dispatchEvent(new Event('scroll'))
      await settle()
      expect(source.requested).toEqual([null])

      // The watchdog: the page is the caller's retry, and the next gesture
      // goes through.
      vi.advanceTimersByTime(AWAIT_ROWS_MS)
      await settle()
      scroller.dispatchEvent(new Event('scroll'))
      await settle()
      expect(source.requested).toEqual([null, null])
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('rows departed while the reader sat at the tail: the first scroll-up re-reads the head, and every line is found once', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      atBottom(scroller)
      surface.setMode('unstructured')
      // The session binds against an empty history; the first burst of
      // output arms the refresh, which paints the head as it then was.
      const source = scriptedSource([
        pageOf('p'.repeat(32), 0, 0, 0, false), // the bind's head read: empty
        pageOf('q'.repeat(32), 0, 10, 0, false), // the refresh: rows 0..9, floor 0
        pageOf('s'.repeat(32), 126, 64, 0, true), // the departure's head: rows 126..189
        pageOf('t'.repeat(32), 62, 64, 0, true), // chained: rows 62..125
        pageOf('u'.repeat(32), 0, 62, 0, false), // the floor: rows 0..61
      ])
      surface.bind(source)
      await settle()
      surface.noteOutput()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(10)

      // 180 MORE rows depart while the reader never leaves the tail. The
      // painted past now ends at row 10, and the emulator's head is 190.
      for (let i = 0; i < 12; i++) surface.noteOutput()

      // The reader departs: the first scroll away is where the stale pages
      // are rebuilt from the head — the reader is still within sight of
      // the live screen, and the anchor pays the change back. Each
      // further gesture walks the chain down over the old coverage.
      scrollNearTop(scroller, 100)
      await settle()
      scrollNearTop(scroller, 100)
      await settle()
      scrollNearTop(scroller, 100)
      await settle()

      // rows 10..125 — which NO backwards page from the old coverage could
      // ever have reached — are found, and nothing is painted twice.
      const rows = surface.el.querySelectorAll('.term-grid-row')
      expect(rows.length).toBe(190)
      expect(rows[0].textContent).toContain('L000000')
      expect(rows[9].textContent).toContain('L000009')
      expect(rows[125].textContent).toContain('L000125')
      expect(rows[189].textContent).toContain('L000189')
      const text = Array.from(rows, (r) => r.textContent ?? '').join('\n')
      expect(text.split('L000005').length - 1).toBe(1)
      expect(text.split('L000100').length - 1).toBe(1)
      expect(source.requested).toEqual([null, null, null, 126, 62])
    } finally {
      vi.useRealTimers()
    }
  })

  it('a floor that lands inside the oldest page trims it: rows the emulator pruned are not shown', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      atBottom(scroller)
      surface.setMode('unstructured')
      const source = scriptedSource([
        pageOf('v'.repeat(32), 0, 0, 0, false), // the bind's head read: empty
        pageOf('w'.repeat(32), 30, 64, 0, true), // the refresh: rows 30..93, floor 0
        pageOf('x'.repeat(32), 30, 0, 50, false), // asked at 30: empty at the cursor, floor 50
      ])
      surface.bind(source)
      await settle()
      surface.noteOutput()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(64)
      expect(scroller.scrollTop).toBe(64 * ROW_PX)

      // The emulator pruned rows 30..49 between the two reads. The next
      // answer states the floor inside the painted page; the page is
      // trimmed to its surviving rows, in place. The reader sat INSIDE the
      // pruned span (100px in is row 35), so there is no row under them to
      // hold: the anchor pays back the removed height and clamps at the
      // first surviving row — the top of what is left.
      scrollNearTop(scroller, 100)
      await settle()

      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(44)
      const first = surface.el.querySelectorAll('.term-grid-row')[0]
      expect(first.textContent).toContain('L000050')
      expect(surface.el.querySelector('.live-history-page')?.getAttribute('data-start')).toBe('50')
      expect(scroller.scrollTop).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a request the drop left behind owns nothing: the new history pages, the old answer is ignored', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    surface.setMode('unstructured')
    // The first session's head request is on the wire, and held there.
    const first = scriptedSource([pageOf('y'.repeat(32), 64, 10, 0, true)], {
      holdResult: true,
    })
    surface.bind(first)
    await settle()
    expect(first.requested).toEqual([null])

    // The pane sights a clear while the request is still in flight, and a
    // new history space takes over: the bind must not wait for, or be
    // blocked by, what the old epoch never answered.
    surface.cleared()
    const second = scriptedSource([
      pageOf('z'.repeat(32), 64, 10, 0, true),
      pageOf('A'.repeat(32), 0, 64, 0, false),
    ])
    surface.bind(second)
    await settle()
    // The reader walks up: the head is in and the chain asks for the page
    // below it — the flight the old epoch left behind holds nothing of
    // the new one's.
    scrollNearTop(scroller, 100)
    await settle()
    expect(second.requested).toEqual([null, 64])
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(74)

    // The old answer lands now. It belongs to a dropped history: it
    // installs nothing, requests nothing, and leaves the new space's
    // painted rows and its cursor exactly as they were.
    first.resolveResult('y'.repeat(32))
    await settle()
    expect(second.requested).toEqual([null, 64])
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(74)
    // And the new space is not wedged: a fresh session would page the same
    // way again (the flight flag was never the stale answer's to clear).
    surface.cleared()
    const third = scriptedSource([pageOf('B'.repeat(32), 64, 10, 0, true)], {
      holdResult: true,
    })
    surface.bind(third)
    await settle()
    expect(third.requested).toEqual([null])
  })

  it('output departed during a page flight makes that page stale: the trailing refresh replaces it under the at-tail reader', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      atBottom(scroller)
      surface.setMode('unstructured')
      // The head read A is on the wire, held. It answers for the buffer as
      // it WAS: rows 0..9, nothing below.
      const source = scriptedSource(
        [pageOf('C'.repeat(32), 0, 10, 0, false), pageOf('D'.repeat(32), 0, 100, 0, false)],
        { holdResult: true },
      )
      surface.bind(source)

      // Rows 10..99 depart while A is still flying.
      for (let i = 0; i < 6; i++) surface.noteOutput()
      source.resolveResult('C'.repeat(32))
      await settle()
      // A installs — it is what the emulator answered — and what it holds
      // is already stale coverage.
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(10)

      // The trailing refresh fires while the reader still sits at the live
      // end: the stale page is replaced THERE, invisibly — the reader never
      // gestured, and the rebuild cannot move them. A fix that rebuilt only
      // on the first departure would instead EAT that gesture: the reader
      // wheeled up and nothing moved (the e2e clause-5 failure).
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      source.resolveResult('D'.repeat(32))
      await settle()
      const rows = surface.el.querySelectorAll('.term-grid-row')
      expect(rows.length).toBe(100)
      const text = Array.from(rows, (r) => r.textContent ?? '').join('\n')
      expect(text.split('L000050').length - 1).toBe(1)
      expect(source.requested).toEqual([null, null])
      // The reader is still at the live end, on the live screen, with the
      // fresh past above them.
      expect(scroller.scrollTop).toBe(100 * ROW_PX)

      // And the first wheel-up is THEIRS: the past is current, nothing is
      // rebuilt under them, and the rows they were reading stay painted.
      scrollNearTop(scroller, 100)
      await settle()
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(100)
      expect(source.requested).toEqual([null, null])
    } finally {
      vi.useRealTimers()
    }
  })

  it('output while the reader is AWAY rebuilds nothing: their rows do not move under them', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      atBottom(scroller)
      surface.setMode('unstructured')
      const source = scriptedSource([pageOf('E'.repeat(32), 0, 30, 0, false)])
      surface.bind(source)
      await settle()
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)

      // The reader walks up into the past...
      scrollNearTop(scroller, 100)
      await settle()
      // ...and while they are away, output departs more rows. The trailing
      // refresh must NOT fire a replacement under a reader who is reading:
      // their anchor holds, nothing is requested, nothing moves.
      surface.noteOutput()
      vi.advanceTimersByTime(HEAD_REFRESH_MS)
      await settle()
      expect(source.requested).toEqual([null])
      expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
      expect(scroller.scrollTop).toBe(100)
    } finally {
      vi.useRealTimers()
    }
  })

  it('the wheel over the live screen reaches the scroller in the unstructured mode, and only there', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    surface.setMode('unstructured')
    const source = scriptedSource([pageOf('F'.repeat(32), 0, 40, 0, false)])
    surface.bind(source)
    await settle()
    expect(scroller.scrollTop).toBe(40 * ROW_PX)

    // xterm's layer above the live screen consumes the gesture's default
    // action; the scroller's own owner still sees the event (a
    // preventDefault cancels the default, never the propagation) and
    // applies the delta itself — the person's wheel moves the history.
    const wheel = new WheelEvent('wheel', { deltaY: -240, cancelable: true, bubbles: true })
    scroller.dispatchEvent(wheel)
    expect(scroller.scrollTop).toBe(40 * ROW_PX - 240)

    // A wheel the clamp holds (down at the live end) claims nothing and
    // cancels nothing.
    scroller.scrollTop = 40 * ROW_PX
    const atTail = scroller.scrollTop
    const pinned = new WheelEvent('wheel', { deltaY: 240, cancelable: true, bubbles: true })
    scroller.dispatchEvent(pinned)
    expect(scroller.scrollTop).toBe(atTail)
    expect(pinned.defaultPrevented).toBe(false)

    // The alternate screen is not this surface's: the wheel goes wherever
    // the input path sends it, and the scroller is not translated.
    surface.setMode('other')
    const other = new WheelEvent('wheel', { deltaY: -240, cancelable: true, bubbles: true })
    scroller.dispatchEvent(other)
    expect(scroller.scrollTop).toBe(atTail)
    expect(other.defaultPrevented).toBe(false)
  })

  it('a column reflow drops the painted past and asks for the head again at once', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    surface.setMode('unstructured')
    const source = scriptedSource([
      pageOf('G'.repeat(32), 0, 30, 0, false),
      pageOf('H'.repeat(32), 0, 30, 0, false),
    ])
    surface.bind(source)
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
    expect(source.requested).toEqual([null])

    // The pane reflowed: the addresses are a numbering that no longer
    // exists, and a resize that dropped the past and waited for output
    // would leave nothing to scroll — the wheel cannot ask for what it
    // cannot reach. The head is re-read immediately.
    surface.reflowed()
    await settle()
    expect(source.requested).toEqual([null, null])
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
  })

  it('a clamped wheel on an empty surface is still the gesture a lost page waits on', async () => {
    vi.useFakeTimers()
    try {
      const { surface, scroller } = mount()
      surface.setMode('unstructured')
      // The first page's rows document never arrives; the watchdog gives
      // the page back, and the surface is empty with no exhaustion latched.
      const source = scriptedSource(
        [pageOf('I'.repeat(32), 0, 10, 0, false), pageOf('J'.repeat(32), 0, 10, 0, false)],
        { holdRows: true },
      )
      surface.bind(source)
      await settle()
      vi.advanceTimersByTime(AWAIT_ROWS_MS)
      await settle()
      expect(source.requested).toEqual([null])

      // The live rectangle fills the scroller exactly, so the wheel-up
      // moves nothing — clamped at zero — and the retry it stands for must
      // still run: the gesture counts even when the pixels do not.
      const wheel = new WheelEvent('wheel', { deltaY: -240, cancelable: true, bubbles: true })
      scroller.dispatchEvent(wheel)
      await settle()
      expect(source.requested).toEqual([null, null])
    } finally {
      vi.useRealTimers()
    }
  })

  it('a stale rebuild honors the gesture that left: the reader lands the same distance from the tail', async () => {
    const { surface, scroller } = mount()
    atBottom(scroller)
    surface.setMode('unstructured')
    const source = scriptedSource([
      pageOf('K'.repeat(32), 0, 30, 0, false),
      pageOf('L'.repeat(32), 0, 30, 0, false),
    ])
    surface.bind(source)
    await settle()
    expect(scroller.scrollTop).toBe(30 * ROW_PX)

    // Output dirties the painted past; the reader wheels up before the
    // trailing refresh fires. The rebuild must not return them to the live
    // end: they land as far from the tail as the moment they left it.
    surface.noteOutput()
    scrollNearTop(scroller, 100)
    await settle()
    expect(source.requested).toEqual([null, null])
    expect(scroller.scrollTop).toBe(100)
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(30)
  })

  it('a drop cancels what was in flight: a stale answer installs nothing', async () => {
    const { surface } = mount()
    surface.setMode('unstructured')
    const source = scriptedSource([pageOf('j'.repeat(32), 0, 10, 0, false)], {
      holdRows: true,
    })
    surface.bind(source)
    // The fetch is in flight; the pane sighted a clear. Whatever the
    // answer carries now belongs to a history the surface no longer holds.
    surface.cleared()
    source.deliverRows('j'.repeat(32))
    await settle()
    expect(surface.el.querySelectorAll('.term-grid-row').length).toBe(0)
  })
})
