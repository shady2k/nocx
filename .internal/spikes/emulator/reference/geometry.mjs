// Replays a recorded capture through headless @xterm/headless + unicode11 —
// the product's own VT frontend per ADR-0001 — and writes its final screen
// column by column, in the same schema cmd/geom writes, so the two can be
// compared cell for cell.
//
//   node geometry.mjs <capture.jsonl> <out.json> [--parts N | --bytewise]
//
// Version choice is stated rather than implied: @xterm/headless 5.5.0 with
// @xterm/addon-unicode11 0.8.0, matching how the spike's earlier reference and
// the product's own dependency range (@xterm/xterm ^5.5.0, addon-unicode11
// ^0.8.0) line up. The terminal is configured with unicode.activeVersion
// '11', as frontend/src/renderers/xterm.ts does.
//
// Two things about this file are deliberate. It does not normalise: every cell
// is the raw {chars, width} pair xterm.js holds, and cmd/geom score is the one
// place a comparison normalisation happens. And it reads the viewport
// (viewportY + y), not buffer line y, because the last screenful is what a
// person sees and what both candidate emulators report.
// A Node script the spike runs by hand: the root eslint config gives Node
// globals to e2e and .githooks mjs files only.
/* global process, console, Buffer */
import { readFileSync, writeFileSync } from 'node:fs'
import { basename, extname } from 'node:path'
import { createRequire } from 'node:module'

// NODE_PATH is honoured by require(), not by the ESM resolver, and the xterm
// packages deliberately live outside the repository (see geometry.sh).
const require = createRequire(import.meta.url)
const { Terminal } = require('@xterm/headless')
const { Unicode11Addon } = require('@xterm/addon-unicode11')

const [captureArg, outArg, ...rest] = process.argv.slice(2)
if (!captureArg || !outArg) {
  console.error('usage: geometry.mjs <capture.jsonl> <out.json> [--parts N | --bytewise]')
  process.exit(2)
}
let parts = 1
let bytewise = false
for (let i = 0; i < rest.length; i++) {
  const arg = rest[i].replace(/^--?/, '')
  if (arg === 'parts') parts = Number(rest[++i])
  else if (arg === 'bytewise') bytewise = true
  else {
    console.error(`unknown argument ${rest[i]}`)
    process.exit(2)
  }
}

const lines = readFileSync(captureArg, 'utf8')
  .split('\n')
  .filter((l) => l.trim() !== '')
const header = JSON.parse(lines[0])
const chunks = lines.slice(1).map((l) => JSON.parse(l))

let stream = ''
for (const c of chunks) stream += c.data
// A replacement character here means the recorder's JSON encoding already
// changed the bytes (see cmd/geom's readCapture), and the replay would be of a
// stream the program never wrote. Refuse rather than measure it.
const replacements = (stream.match(/\uFFFD/g) ?? []).length
if (replacements > 0) {
  // The recorder writes capture data as a JSON string, and JSON encoding cannot
  // carry a byte that is not valid UTF-8: it becomes U+FFFD, three bytes where
  // there was one. A stream with a replacement character in it is not the
  // stream the program wrote, so there is nothing here worth measuring.
  console.error(`${captureArg}: ${replacements} replacement characters in the decoded stream`)
  process.exit(1)
}

const buf = Buffer.from(stream, 'utf8')
let pieces
if (bytewise) {
  pieces = [...buf].map((_, i) => buf.subarray(i, i + 1))
} else if (parts > 1) {
  pieces = []
  let start = 0
  for (let i = 1; i <= parts; i++) {
    const end = Math.floor((buf.length * i) / parts)
    pieces.push(buf.subarray(start, end))
    start = end
  }
} else {
  pieces = [buf]
}

const mode = bytewise ? 'bytewise' : parts > 1 ? `split:${parts}` : 'whole'

const term = new Terminal({
  cols: header.cols,
  rows: header.rows,
  allowProposedApi: true,
  scrollback: 1000,
})
term.loadAddon(new Unicode11Addon())
term.unicode.activeVersion = '11'

const write = (data) => new Promise((resolve) => term.write(data, resolve))
for (const piece of pieces) await write(piece)

const b = term.buffer.active
const cells = []
for (let y = 0; y < header.rows; y++) {
  const line = b.getLine(b.viewportY + y)
  const row = []
  for (let x = 0; x < header.cols; x++) {
    const cell = line ? line.getCell(x) : undefined
    if (!cell) {
      row.push({ t: '', w: 1 })
      continue
    }
    row.push({ t: cell.getChars(), w: cell.getWidth() })
  }
  cells.push(row)
}

const dump = {
  capture: basename(captureArg, extname(captureArg)),
  emulator: 'xterm',
  mode,
  cols: header.cols,
  rows: header.rows,
  cursorX: b.cursorX,
  cursorY: b.cursorY,
  chunks: chunks.length,
  bytes: buf.length,
  replacementChars: replacements,
  cells,
}
writeFileSync(outArg, JSON.stringify(dump) + '\n')
