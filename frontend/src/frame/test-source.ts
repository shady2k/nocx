// Shared test double: an xterm-shaped CaptureEventSource.
//
// Models WriteBuffer's real contract (xterm 5.5.0, common/input/WriteBuffer.ts):
//
//   - write() QUEUES a chunk; nothing is applied yet.
//   - a parse pass takes chunks off the front and applies each one WHOLE —
//     _innerWrite hands the entire chunk to the parser, fires that chunk's
//     callback, and only THEN checks its 12 ms budget. So a pass can stop
//     between chunks and never inside one; parseOnePass(n) models the budget
//     as "n chunks get through".
//   - onWriteParsed fires once per pass, at its end, whether or not the queue
//     is now empty.
//   - a callback queued behind the chunks is a BARRIER: it runs when the
//     chunks ahead of it have been applied, and chunks written afterwards go
//     behind it. awaitWriteBarrier() models that, and carries no bytes — so
//     it does not move the unsettled count, exactly as the renderer writes it
//     straight to the terminal rather than through write().

import { BufferLine, type BufferLine as BufferLineType } from '../scrollback/test-helpers'
import type { CaptureEventSource } from './types'

export class FakeSource implements CaptureEventSource {
  cols = 80
  rows = 24
  private cells = new Map<number, string[]>()
  private styled = new Map<number, BufferLineType>()
  cursor = { line: 0, col: 0 }
  /** The write queue, front entry first. A chunk carries the callback that
   *  runs when it has been applied — a real write settles the unsettled
   *  count, a barrier resolves its promise. */
  private queued: Array<{ data: string; settle: () => void }> = []
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

  /** The capture fence: a callback queued behind everything written so far.
   *  A disposed source returns a promise that never settles — the terminal
   *  took its callbacks with it, which is exactly the hang the tracker's
   *  disposal rejection exists to close. */
  awaitWriteBarrier(): Promise<void> {
    if (this.disposed) return new Promise<void>(() => {})
    return new Promise<void>((resolve) => {
      this.queued.push({ data: '', settle: resolve })
    })
  }

  /** Instrumented pending count for capture-fence tests. The product seam
   *  stays boolean; only the witness needs to distinguish 1 → 1 starvation
   *  from a count that never receives callbacks. */
  unsettledWriteCount(): number {
    return this.pending
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

  /** Queue a write — mirrors xterm: queued, not yet parsed, and settled as
   *  a whole by the pass that applies it. */
  write(data: string): void {
    this.queued.push({
      data,
      settle: () => {
        this.pending = Math.max(0, this.pending - 1)
      },
    })
    this.pending++
  }

  /** One parse pass: apply up to `chunks` whole queued chunks (default: the
   *  whole queue), settling each, then fire onWriteParsed once. The default
   *  of one chunk per call is deliberate — it is how a test shows that a
   *  pass CAN end with queued bytes still unparsed, which is why the fence
   *  is a barrier and not the next parse boundary. */
  parseOnePass(chunks = 1): void {
    for (let i = 0; i < chunks && this.queued.length > 0; i++) {
      const entry = this.queued.shift()
      if (!entry) break
      this.apply(entry.data)
      entry.settle()
    }
    for (const sub of this.writeParsedSubs) sub()
  }

  /** Drain every queued chunk (the common small-write case). */
  flush(): void {
    while (this.queued.length > 0) this.parseOnePass(Number.POSITIVE_INFINITY)
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
