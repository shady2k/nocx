// Shared test double: an xterm-shaped CaptureEventSource.
//
// Models WriteBuffer's real contract (verified against xterm 5.5.0 source):
// write() QUEUES data (nothing applied, hasUnsettledWrite() true); a parse
// pass applies a SLICE of the front write and fires onWriteParsed — the
// write's own settle (its per-write callback) fires only on the pass that
// EMPTIES it, so ONE write can span several passes with onWriteParsed
// firing while the write is still pending (xterm's own doc note on the
// event), which is exactly the trap the capture fence exists for.

import { BufferLine, type BufferLine as BufferLineType } from '../scrollback/test-helpers'
import type { CaptureEventSource } from './types'

export class FakeSource implements CaptureEventSource {
  cols = 80
  rows = 24
  private cells = new Map<number, string[]>()
  private styled = new Map<number, BufferLineType>()
  cursor = { line: 0, col: 0 }
  /** Queued writes, front entry = remaining bytes of the oldest write. */
  private queued: string[] = []
  private pending = 0
  private writeParsedSubs: Array<() => void> = []
  private bufferChangeSubs: Array<(t: 'normal' | 'alternate') => void> = []
  private resizeSubs: Array<(c: number, r: number) => void> = []
  private clearSubs: Array<() => void> = []
  private resetSubs: Array<() => void> = []
  private disposeSubs: Array<() => void> = []
  private disposed = false

  onWriteParsed(cb: () => void): void {
    this.writeParsedSubs.push(cb)
  }
  onBufferChange(cb: (t: 'normal' | 'alternate') => void): void {
    this.bufferChangeSubs.push(cb)
  }
  onResize(cb: (c: number, r: number) => void): void {
    this.resizeSubs.push(cb)
  }
  onClear(cb: () => void): void {
    this.clearSubs.push(cb)
  }
  onReset(cb: () => void): void {
    this.resetSubs.push(cb)
  }
  onDispose(cb: () => void): void {
    if (this.disposed) {
      // Already disposed: fire immediately — a late subscriber must not
      // wait forever on a source that is gone.
      cb()
      return
    }
    this.disposeSubs.push(cb)
  }
  hasUnsettledWrite(): boolean {
    return this.pending > 0
  }

  // ── test drivers ────────────────────────────────────────────────────────

  /** The buffer as the seam reads it: an IBufferLine for absolute line y
   *  (styled fixture lines win over plain seeded ones). */
  getBufferLine(y: number): BufferLineType | undefined {
    const styled = this.styled.get(y)
    if (styled) return styled
    const cells = this.cells.get(y)
    return cells ? new BufferLine(cells.join('')) : undefined
  }

  /** The chars of absolute line y (the plain-content view). */
  getLine(y: number): string[] | undefined {
    return this.cells.get(y)
  }

  /** Seed plain lines directly (bypassing the write queue). */
  seed(lines: string[]): void {
    lines.forEach((line, i) => this.cells.set(i, [...line]))
    this.cursor = { line: lines.length - 1, col: lines[lines.length - 1].length }
  }

  /** Replace one line with a styled BufferLine (attribute fixtures). */
  setLine(y: number, line: BufferLineType): void {
    this.styled.set(y, line)
    this.cells.set(y, line.translateToString().split(''))
  }

  /** Queue a write — mirrors xterm: queued, not yet parsed. The write's
   *  settle fires only when its LAST byte has been parsed, so one write
   *  can span several parseOnePass() calls. */
  write(data: string): void {
    this.queued.push(data)
    this.pending++
  }

  /** One parse pass: parse up to `chars` bytes of the FRONT write (default:
   *  the whole front write). The pass settles the write only when it empties
   *  — so a single write can be split across passes, with onWriteParsed
   *  firing while the write's own callback is still pending: the exact
   *  interleaving xterm's WriteBuffer produces for a large write. */
  parseOnePass(chars = Number.POSITIVE_INFINITY): void {
    const front = this.queued[0]
    if (front !== undefined) {
      const take = Math.min(chars, front.length)
      if (take > 0) {
        this.apply(front.slice(0, take))
        this.queued[0] = front.slice(take)
      }
      if (this.queued[0].length === 0) {
        this.queued.shift()
        this.pending = Math.max(0, this.pending - 1)
      }
    }
    for (const sub of this.writeParsedSubs) sub()
  }

  /** Drain every queued chunk (the common small-write case). */
  flush(): void {
    while (this.queued.length > 0) this.parseOnePass()
  }

  private apply(chunk: string): void {
    // Real terminal semantics: characters land at the cursor and advance it;
    // '\n' moves down a row and back to column 0.
    let { line, col } = this.cursor
    for (const ch of chunk) {
      if (ch === '\n') {
        line++
        col = 0
        continue
      }
      const row = this.cells.get(line) ?? []
      while (row.length <= col) row.push(' ')
      row[col] = ch
      this.cells.set(line, row)
      col++
    }
    this.cursor = { line, col }
  }

  enterAlt(): void {
    for (const sub of this.bufferChangeSubs) sub('alternate')
  }
  leaveAlt(): void {
    for (const sub of this.bufferChangeSubs) sub('normal')
  }
  resize(cols: number, rows: number): void {
    this.cols = cols
    this.rows = rows
    for (const sub of this.resizeSubs) sub(cols, rows)
  }
  clear(): void {
    this.cells.clear()
    this.styled.clear()
    for (const sub of this.clearSubs) sub()
  }
  reset(): void {
    this.cells.clear()
    this.styled.clear()
    for (const sub of this.resetSubs) sub()
  }
  /** Test driver: dispose the source — a capture parked on the fence must
   *  settle (reject), never hang (nocx-x8s2.4). */
  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    const subs = this.disposeSubs
    this.disposeSubs = []
    for (const sub of subs) sub()
  }
}

/** A seeded source with the cursor parked at the end of the last line. */
export function seedSource(lines: string[]): FakeSource {
  const source = new FakeSource()
  source.seed(lines)
  return source
}
