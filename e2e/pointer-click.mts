/**
 * The person's click, read from what the program actually received
 * (nocx-1ebrs.66.1.26).
 *
 * A pane does not put a mouse report on the wire when the mouse moves: the
 * intent is queued behind the pane's other input, and behind the input fence
 * a mode switch holds until its resize applies. A snapshot of the wire taken
 * the instant `page.mouse.click` returns can therefore hold the press alone,
 * or neither, while the program goes on to receive both — and a spec that
 * takes that snapshot for the gesture reports as lost a release the program
 * already received.
 *
 * The program's own bytes are the witness that the click got through, so the
 * click is read from there: the PTY bytes the fixture prints, hex, one chunk
 * per read. The wire is then read against this, not instead of it.
 */

export interface SgrMouseReport {
  /** 0 left, 1 middle, 2 right; 32 and up are motion, and 64 and up wheel. */
  button: number
  /** Cells, 1-based, as SGR numbers them. */
  col: number
  row: number
  /** SGR spells the press `M` and the release `m`. */
  press: boolean
}

// eslint-disable-next-line no-control-regex -- an SGR mouse report starts with ESC
const SGR_MOUSE = /\x1b\[<(\d+);(\d+);(\d+)([Mm])/g

/** The SGR (1006) mouse reports in a program's input, in the order it read
 *  them. Chunks are hex; a report can straddle two reads, so they are joined
 *  before matching. */
export function sgrMouseReports(chunks: readonly string[]): SgrMouseReport[] {
  const bytes = Buffer.from(chunks.join(''), 'hex').toString('latin1')
  const reports: SgrMouseReport[] = []
  for (const match of bytes.matchAll(SGR_MOUSE)) {
    reports.push({
      button: Number(match[1]),
      col: Number(match[2]),
      row: Number(match[3]),
      press: match[4] === 'M',
    })
  }
  return reports
}

export interface ClickGesture {
  press: SgrMouseReport
  release: SgrMouseReport
}

/** The first left-button press and the release that follows it. Motion is not
 *  part of a click, and a press without a release is not one yet: the release
 *  is the half that says the click finished travelling. */
export function clickGesture(chunks: readonly string[]): ClickGesture | null {
  const left = sgrMouseReports(chunks).filter((report) => report.button === 0)
  const press = left.findIndex((report) => report.press)
  if (press < 0) return null
  const release = left.slice(press + 1).find((report) => !report.press)
  if (release === undefined) return null
  return { press: left[press], release }
}
