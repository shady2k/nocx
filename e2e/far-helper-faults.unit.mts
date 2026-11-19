import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import ts from 'typescript'

// The far helper's death, staged by the far host: the helper's cue protocol and
// the wait a spec stands on (nocx-oalo1.6.3).
//
// The module's only runtime import is fs's writeFileSync, and the e2e unit
// tests load a module by stripping its imports (ask-gesture.unit.mts's shape).
// So the real function is handed to the transpiled code here, which is the
// whole of what it needs: what is under test is the protocol — a distinct
// content per cue, the pids a cue announced, and the failure a far host that
// found nothing produces — not the file system.
Object.assign(globalThis, { writeFileSync })
const source = readFileSync(new URL('./far-helper-faults.ts', import.meta.url), 'utf8')
const compiled = ts
  .transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ESNext },
  })
  .outputText.replace(/import .*? from .*?;\n/g, '')
const { helperKillCues, helperKilledPids, killFarHelper } = await import(
  `data:text/javascript,${encodeURIComponent(compiled)}`
)

interface FakeHost {
  helperKillFile: string
  lines(): readonly string[]
}

/** A fixture-shaped host: the printed lines are a live array, exactly as
 *  SshdFixture.lines() is. */
function fakeHost(dir: string, gate = true): FakeHost {
  const lines: string[] = []
  return {
    helperKillFile: gate ? path.join(dir, 'cue') : '',
    lines: () => lines,
  }
}

/**
 * Stands in for cmd/e2e-sshd's gate: every new content of the gate is one cue,
 * answered with the pids `answer` gives. The poll is the test's own because the
 * far side's is a process the Go tests own; here it only has to be the
 * observable the helper waits on.
 */
function fakeFarHost(host: FakeHost, answer: () => number[]): () => void {
  let last: string | null = null
  const timer = setInterval(() => {
    if (!existsSync(host.helperKillFile)) return
    const content = readFileSync(host.helperKillFile, 'utf8')
    if (content === last) return
    last = content
    const pids = answer()
    const lines = host.lines() as string[]
    for (const pid of pids) lines.push(`HELPERKILLED=${pid}`)
    lines.push(`HELPERKILLDONE=${pids.length}`)
  }, 5)
  return () => clearInterval(timer)
}

test('a cue answers only its own pids, not every pid the far side ever killed', () => {
  const lines = [
    'ADDR=127.0.0.1:1',
    'HELPERKILLED=11',
    'HELPERKILLDONE=1',
    'HELPERKILLED=21',
    'HELPERKILLED=22',
    'HELPERKILLDONE=2',
    'HELPERKILLED=31',
    'HELPERKILLDONE=0',
    'HELPERKILLED=41',
    'HELPERKILLDONE=1',
  ]
  assert.equal(helperKillCues(lines), 4)
  assert.deepEqual(helperKilledPids(lines), [41])
})

test('killFarHelper arms the gate and answers the far host’s cue', async () => {
  const host = fakeHost(mkdtempSync(path.join(tmpdir(), 'nocx-far-helper-')))
  const stop = fakeFarHost(host, () => [4100])
  try {
    assert.deepEqual(await killFarHelper(host), [4100])
  } finally {
    stop()
  }
})

test('a second kill is a second cue, with its own answer', async () => {
  const host = fakeHost(mkdtempSync(path.join(tmpdir(), 'nocx-far-helper-')))
  let next = 4200
  const stop = fakeFarHost(host, () => [next++])
  try {
    assert.deepEqual(await killFarHelper(host), [4200])
    assert.deepEqual(await killFarHelper(host), [4201])
    assert.equal(helperKillCues(host.lines()), 2)
  } finally {
    stop()
  }
})

test('a far host that found nothing to kill is a failure, not a silent success', async () => {
  const host = fakeHost(mkdtempSync(path.join(tmpdir(), 'nocx-far-helper-')))
  const stop = fakeFarHost(host, () => [])
  try {
    await assert.rejects(killFarHelper(host), /found no helper daemon to kill/)
  } finally {
    stop()
  }
})

test('a fixture that announced no gate path cannot be asked to kill anything', async () => {
  const host = fakeHost(mkdtempSync(path.join(tmpdir(), 'nocx-far-helper-')), false)
  await assert.rejects(killFarHelper(host), /announced no HELPERKILL path/)
})
