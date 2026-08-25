// Ground truth for nocx-szb40.1: render a capture through the SAME xterm.js
// the product ships and dump the resulting grid.
//
// Two pins matter and both are deliberate. The version is 5.5.0 because that
// is what frontend/package.json resolves; a comparison against 6.x would
// answer a question nobody asked. And the unicode11 addon is loaded because
// renderers/xterm.ts:334 loads it at runtime, so without it the ground truth
// would account column widths differently from the product it is standing in
// for — which is the exact class of error this whole exercise exists to find.
//
// Output is a frame: the character at every cell, plus the cursor. Attributes
// are emitted separately and coarsely (fg/bg/bold), because the task's bar for
// a frame is the chrome anchors — where things ARE — and colour is a second
// question that should not be allowed to drown the first.
import { readFileSync, writeFileSync } from 'node:fs'
// Both packages ship CommonJS at 5.5.0, so the named-export form does not
// resolve under Node's ESM loader — take the default and destructure.
import headless from '@xterm/headless'
import unicode11 from '@xterm/addon-unicode11'
const { Terminal } = headless
const { Unicode11Addon } = unicode11

const [, , capturePath, outPath] = process.argv
if (!capturePath || !outPath) {
  console.error('usage: node render-xterm.mjs CAPTURE.jsonl OUT.json')
  process.exit(2)
}

const lines = readFileSync(capturePath, 'utf8').split('\n').filter(Boolean)
const header = JSON.parse(lines[0])
const data = lines.slice(1).map((l) => JSON.parse(l).data).join('')

const term = new Terminal({
  cols: header.cols,
  rows: header.rows,
  allowProposedApi: true,
  // The ring is not scrollback here either: this frame is the viewport, which
  // is what a driver classifies. Kept large enough that nothing is evicted
  // mid-capture and small enough to stay cheap.
  scrollback: 10000,
})
const uni = new Unicode11Addon()
term.loadAddon(uni)
term.unicode.activeVersion = '11'

await new Promise((resolve) => term.write(data, resolve))

const buf = term.buffer.active
const rows = []
for (let y = 0; y < term.rows; y++) {
  // buf.viewportY + y is the viewport row; the driver looks at the viewport,
  // never at scrollback, so that is what is dumped.
  const line = buf.getLine(buf.viewportY + y)
  const cells = []
  let text = ''
  if (line) {
    for (let x = 0; x < term.cols; x++) {
      const c = line.getCell(x)
      if (!c) { cells.push(null); continue }
      const chars = c.getChars()
      const width = c.getWidth()
      text += chars === '' ? (width === 0 ? '' : ' ') : chars
      cells.push({
        ch: chars,
        w: width,
        fg: c.isFgDefault() ? null : c.getFgColor(),
        bg: c.isBgDefault() ? null : c.getBgColor(),
        bold: c.isBold() ? 1 : 0,
        inverse: c.isInverse() ? 1 : 0,
      })
    }
  } else {
    for (let x = 0; x < term.cols; x++) cells.push(null)
  }
  rows.push({ text, cells })
}

writeFileSync(
  outPath,
  JSON.stringify(
    {
      renderer: 'xterm.js',
      version: '5.5.0+unicode11@0.8.0',
      source: capturePath,
      cols: term.cols,
      rows: term.rows,
      cursor: { x: buf.cursorX, y: buf.cursorY },
      grid: rows,
    },
    null,
    1,
  ),
)
console.error(`xterm.js frame -> ${outPath} (${term.cols}x${term.rows}, cursor ${buf.cursorX},${buf.cursorY})`)
