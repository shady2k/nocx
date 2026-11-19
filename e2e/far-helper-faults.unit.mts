import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import ts from 'typescript'

// The far helper's death, staged by the far host: the helper's cue protocol and
// the wait a spec stands on (nocx-oalo1.6.3).
//
// The module's only runtime imports are fs's writeFileSync and renameSync, and
// the e2e unit tests load a module by stripping its imports
// (ask-gesture.unit.mts's shape). So the real functions are handed to the
// transpiled code here, which is the whole of what it needs: what is under test
// is the protocol — a distinct content per cue, the answer that names its cue,
// and the failure a far host that found nothing produces — and one property of
// the filesystem, that the gate is never truncated in place.
Object.assign(globalThis, { renameSync, writeFileSync })
const source = readFileSync(new URL('./far-helper-faults.ts', import.meta.url), 'utf8')
const compiled = ts
  .transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ESNext },
  })
  .outputText.replace(/import .*? from .*?;\n/g, '')
const { helperKillAnswer, killFarHelper } = await import(
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
 * answered with the pids `answer` gives, and every answer names its cue —
 * `HELPERKILLED=<pid> cue=<cue>`, then `HELPERKILLDONE=<n> cue=<cue>`. The poll
 * is the test's own because the far side's is a process the Go tests own; here
 * it only has to be the observable the helper waits on.
 */
function fakeFarHost(host: FakeHost, answer: () => number[]): () => void {
  let last: string | null = null
  const timer = setInterval(() => {
    if (!existsSync(host.helperKillFile)) return
    const cue = readFileSync(host.helperKillFile, 'utf8').trim()
    if (cue === '' || cue === last) return
    last = cue
    const pids = answer()
    const lines = host.lines() as string[]
    for (const pid of pids) lines.push(`HELPERKILLED=${pid} cue=${cue}`)
    lines.push(`HELPERKILLDONE=${pids.length} cue=${cue}`)
  }, 5)
  return () => clearInterval(timer)
}

test('an answer names its cue: a previous cue’s answer does not answer this one', () => {
  const lines = ['ADDR=127.0.0.1:1', 'HELPERKILLED=11 cue=first', 'HELPERKILLDONE=1 cue=first']
  assert.deepEqual(helperKillAnswer(lines, 'first'), { done: true, pids: [11], failure: null })
  // The race the names exist against: a call for `second` must not be
  // satisfied by the answer to `first`, however many have printed already.
  assert.deepEqual(helperKillAnswer(lines, 'second'), { done: false, pids: [], failure: null })
})

test('a cue that killed nothing answers with its own count and failure', () => {
  const lines = [
    'HELPERKILLED=11 cue=first',
    'HELPERKILLDONE=1 cue=first',
    'HELPERKILLFAILED=permission denied cue=second',
    'HELPERKILLDONE=0 cue=second',
  ]
  assert.deepEqual(helperKillAnswer(lines, 'second'), {
    done: true,
    pids: [],
    failure: 'permission denied',
  })
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
  } finally {
    stop()
  }
})

test('a cue is published beside the gate and renamed onto it, never truncated in place', async () => {
  const host = fakeHost(mkdtempSync(path.join(tmpdir(), 'nocx-far-helper-')))
  const stop = fakeFarHost(host, () => [4300])
  const real = writeFileSync
  const written: string[] = []
  // The gate is a file the far side polls. Writing IT would truncate it first,
  // and a poll in that gap reads blank — which is why publication must not name
  // the gate when it writes.
  Object.assign(globalThis, {
    writeFileSync: (target: string, data: string) => {
      written.push(target)
      if (target === host.helperKillFile) {
        throw new Error('the gate was truncated in place: a poll could read it empty')
      }
      real(target, data)
    },
  })
  try {
    assert.deepEqual(await killFarHelper(host), [4300])
  } finally {
    Object.assign(globalThis, { writeFileSync: real })
    stop()
  }
  assert.equal(written.length, 1)
  assert.notEqual(written[0], host.helperKillFile)
  assert.equal(existsSync(written[0]), false)
  assert.match(readFileSync(host.helperKillFile, 'utf8'), /^kill-/)
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
