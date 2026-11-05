import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import {
  annotationFor,
  DiskWatch,
  parseDfFreeKb,
  reportLines,
  summaryLine,
} from './e2e-disk-watch.mjs'

const SCRIPT = path.join(import.meta.dirname, 'e2e-disk-watch.mjs')

function run(args) {
  try {
    const stdout = execFileSync(process.execPath, [SCRIPT, ...args], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    return { status: 0, stdout }
  } catch (err) {
    return { status: err.status ?? -1, stdout: `${err.stdout ?? ''}${err.stderr ?? ''}` }
  }
}

test('parseDfFreeKb reads the free column of df -Pk', () => {
  const text = [
    'Filesystem     1024-blocks      Used Available Capacity Mounted on',
    '/dev/nvme0n1p1   75823104  63951360  11871744      85% /',
  ].join('\n')
  assert.equal(parseDfFreeKb(text), 11871744)
})

test('parseDfFreeKb refuses output it cannot read rather than guessing zero', () => {
  assert.throws(() => parseDfFreeKb('df: /: No such file or directory'), /cannot read free KiB/)
})

test('DiskWatch keeps the minimum and reports the first crossing exactly once', () => {
  const watch = new DiskWatch({ thresholdKb: 5_000_000 })
  assert.equal(watch.record(9_000_000, 1), 'sampled')
  assert.equal(watch.record(4_000_000, 2), 'crossed')
  assert.equal(watch.record(3_000_000, 3), 'sampled')
  assert.equal(watch.record(8_000_000, 4), 'sampled')
  assert.equal(watch.minimumKb, 3_000_000)
  assert.equal(watch.minimumAt, 3)
  assert.equal(watch.samples, 4)
  assert.equal(watch.belowThreshold, true)
})

test('a minimum that comes back is still below the threshold', () => {
  const watch = new DiskWatch({ thresholdKb: 5_000_000 })
  watch.record(2_000_000, 1)
  watch.record(9_000_000, 2)
  assert.equal(watch.belowThreshold, true)
  assert.match(annotationFor(watch), /^::error::/)
  assert.match(summaryLine(watch), /minimum free 1953 MiB/)
})

test('above the threshold there is a summary line and no annotation', () => {
  const watch = new DiskWatch({ thresholdKb: 5_000_000 })
  watch.record(9_000_000, 1)
  assert.equal(annotationFor(watch), '')
  assert.match(summaryLine(watch), /minimum free 8789 MiB/)
})

test('the wrapper runs the command, exits with its status, and records the minimum', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'nocx-disk-watch-'))
  try {
    const state = path.join(dir, 'state.json')
    const result = run([
      '--threshold-mb',
      '1000000',
      '--interval-ms',
      '50',
      '--no-breakdown',
      '--state',
      state,
      '--',
      'sh',
      '-c',
      'sleep 0.3; exit 7',
    ])
    assert.equal(result.status, 7, result.stdout)
    assert.match(result.stdout, /disk-watch: df -h at start/)
    assert.match(result.stdout, /::error::/)
    const recorded = JSON.parse(readFileSync(state, 'utf8'))
    assert.equal(recorded.belowThreshold, true)
    assert.ok(recorded.samples >= 2, `expected repeated samples, got ${recorded.samples}`)
    assert.ok(recorded.minimumFreeMiB > 0)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('a run with room exits 0 and annotates nothing', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'nocx-disk-watch-'))
  try {
    const state = path.join(dir, 'state.json')
    const result = run(['--threshold-mb', '0', '--no-breakdown', '--state', state, '--', 'true'])
    assert.equal(result.status, 0, result.stdout)
    assert.doesNotMatch(result.stdout, /::error::/)
    assert.equal(JSON.parse(readFileSync(state, 'utf8')).belowThreshold, false)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('a command that cannot be started is reported as 127, not as a disk problem', () => {
  const result = run(['--threshold-mb', '0', '--no-breakdown', '--', 'no-such-command-nocx'])
  assert.equal(result.status, 127, result.stdout)
  assert.match(result.stdout, /cannot run no-such-command-nocx/)
})

test('the end-of-job report names the worst peak even when the space came back', () => {
  const states = [
    {
      mount: '/',
      thresholdMiB: 6144,
      minimumFreeMiB: 5200,
      minimumAt: '2026-11-05T06:18:46.000Z',
      samples: 240,
      belowThreshold: false,
      exit: 0,
    },
    {
      mount: '/',
      thresholdMiB: 6144,
      minimumFreeMiB: 410,
      minimumAt: '2026-11-05T06:30:24.000Z',
      samples: 90,
      belowThreshold: true,
      exit: 1,
    },
  ]
  const lines = reportLines(states)
  assert.equal(lines.length, 3)
  assert.match(lines[0], /minimum free 5200 MiB on \/ at 2026-11-05T06:18:46\.000Z/)
  assert.match(lines[2], /^::error::/)
  assert.match(lines[2], /fell to 410 MiB/)
})

test('the end-of-job report stays quiet when nothing dipped', () => {
  const lines = reportLines([
    {
      mount: '/',
      thresholdMiB: 6144,
      minimumFreeMiB: 9000,
      minimumAt: '2026-11-05T06:00:00.000Z',
      samples: 10,
      belowThreshold: false,
      exit: 0,
    },
  ])
  assert.equal(lines.length, 1)
  assert.doesNotMatch(lines[0], /::error::/)
})

test('--report reads the state files the steps wrote and never fails the job', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'nocx-disk-watch-'))
  try {
    const dipped = path.join(dir, 'dipped.json')
    const ok = path.join(dir, 'ok.json')
    writeFileSync(
      dipped,
      JSON.stringify({
        mount: '/',
        thresholdMiB: 6144,
        minimumFreeMiB: 512,
        minimumAt: '2026-11-05T06:30:24.000Z',
        samples: 12,
        belowThreshold: true,
        exit: 1,
      }),
    )
    writeFileSync(ok, '{}')
    const result = run(['--report', dipped, ok, path.join(dir, 'missing.json')])
    assert.equal(result.status, 0, result.stdout)
    assert.match(result.stdout, /::error::/)
    assert.match(result.stdout, /no usable record/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
