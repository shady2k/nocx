import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { appendCapped, tailOf } from './log-tail.mts'

test('a log shorter than the bound is returned unchanged', () => {
  const text = 'DEBUG one\nDEBUG two\n'
  assert.equal(tailOf(text, 1024), text)
})

test('tailOf keeps the END of a long log, not its beginning', () => {
  const text = 'first line\n' + 'x'.repeat(200) + '\nTHE LAST LINE\n'
  const kept = tailOf(text, 64)
  assert.ok(kept.includes('THE LAST LINE'), `the tail was lost: ${kept}`)
  assert.ok(!kept.includes('first line'), `the head was kept: ${kept}`)
})

test('tailOf starts on a line boundary and says how much it dropped', () => {
  const text = ['line one', 'line two', 'line three', 'line four'].join('\n')
  const kept = tailOf(text, 20)
  const [marker, ...lines] = kept.split('\n')
  assert.match(marker, /^…\[\d+ bytes of earlier log dropped to bound this file\]$/)
  assert.ok(lines.length > 0)
  assert.ok(
    lines.every((line) => line === '' || text.includes(line)),
    `a partial line survived: ${kept}`,
  )
})

test('every dropped byte is accounted for', () => {
  const text = 'a'.repeat(100) + '\ntail\n'
  const kept = tailOf(text, 20)
  const dropped = Number(/…\[(\d+) bytes/.exec(kept)?.[1])
  assert.equal(Number.isFinite(dropped), true)
  assert.equal(
    Buffer.byteLength(text) - Buffer.byteLength(kept.split('\n').slice(1).join('\n')),
    dropped,
  )
})

test('appendCapped never leaves the file larger than a quarter past its bound', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'nocx-log-tail-'))
  try {
    const file = path.join(dir, 'stand.log')
    const max = 1024
    let written = 0
    for (let i = 0; i < 200; i++) {
      const chunk = `line ${i} ${'y'.repeat(60)}\n`
      written += Buffer.byteLength(chunk)
      appendCapped(file, chunk, max)
      const size = Buffer.byteLength(readFileSync(file, 'utf8'))
      assert.ok(size <= max + max / 4, `file grew to ${size} with a bound of ${max}`)
    }
    // The newest line is still there, which is the whole point of keeping a tail.
    assert.ok(readFileSync(file, 'utf8').includes('line 199'), 'the newest line was dropped')
    assert.ok(written > max, 'the test did not write enough to exercise the bound')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('appendCapped keeps a whole file that fits', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'nocx-log-tail-'))
  try {
    const file = path.join(dir, 'stand.log')
    appendCapped(file, 'one\n', 1024)
    appendCapped(file, 'two\n', 1024)
    assert.equal(readFileSync(file, 'utf8'), 'one\ntwo\n')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
