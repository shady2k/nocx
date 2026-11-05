// node --test scripts/helper-hashes.test.mjs
//
// The helper hash files have one format — `<goos>-<goarch> <sha256>` — written
// by three release jobs and read by a fourth. The reader spelled the platform
// `darwin/<arch>`, which matches no line, so every scheduled nightly from
// 2026-11-02 stopped in sign-local-helpers before it signed anything
// (nocx-vh2hr.1.18). That logic lives only in YAML on the release path, so
// this test runs the YAML's own writer and reader against one fixture and
// fails while they disagree.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { gzipSync } from 'node:zlib'
import { lookupHash } from './helper-hashes.mjs'

const WORKFLOW = readFileSync(
  new URL('../.github/workflows/release-build.yml', import.meta.url),
  'utf8',
)

// The run blocks of the steps with this name, dedented. The release logic
// lives in YAML, so the test executes the YAML's own shell rather than a copy.
// Both build jobs carry a step of the same name, so every match is returned.
function stepRuns(name) {
  const lines = WORKFLOW.split('\n')
  const starts = []
  lines.forEach((line, i) => {
    if (line.trim() === `- name: ${name}`) starts.push(i)
  })
  assert.ok(starts.length > 0, `the workflow has no step named ${name}`)
  return starts.map((start) => {
    const runAt = lines.findIndex((line, i) => i > start && line.trim() === 'run: |')
    assert.notEqual(runAt, -1, `${name} has no run block`)
    const indent = lines[runAt].search(/\S/)
    const block = []
    for (let i = runAt + 1; i < lines.length; i++) {
      const line = lines[i]
      if (line.trim() !== '' && line.search(/\S/) <= indent) break
      block.push(line)
    }
    const width = Math.min(...block.filter((line) => line.trim()).map((line) => line.search(/\S/)))
    return block.map((line) => (line.trim() ? line.slice(width) : '')).join('\n')
  })
}

function stepRun(name) {
  return stepRuns(name)[0]
}

const LOCAL = {
  'darwin-amd64': 'darwin amd64 helper bytes',
  'darwin-arm64': 'darwin arm64 helper bytes',
  'linux-amd64': 'linux amd64 helper bytes',
}
const REMOTE = {
  'linux-amd64': 'remote linux amd64 helper bytes',
  'linux-arm64': 'remote linux arm64 helper bytes',
  'darwin-amd64': 'remote darwin amd64 helper bytes',
  'darwin-arm64': 'remote darwin arm64 helper bytes',
}

const sha256 = (text) => createHash('sha256').update(text).digest('hex')

// A checkout-shaped directory with the helper artifacts every writer reads.
// The signing step also runs `scripts/helper-hashes.mjs`, so the one owner is
// copied in beside them.
function fixture() {
  const dir = mkdtempSync(join(tmpdir(), 'nocx-helper-hashes-'))
  const local = join(dir, 'internal/helper/deploy/artifacts/bin/local')
  const remote = join(dir, 'internal/helper/deploy/artifacts/bin')
  mkdirSync(local, { recursive: true })
  mkdirSync(remote, { recursive: true })
  mkdirSync(join(dir, 'scripts'), { recursive: true })
  copyFileSync(
    new URL('./helper-hashes.mjs', import.meta.url),
    join(dir, 'scripts/helper-hashes.mjs'),
  )
  for (const [platform, bytes] of Object.entries(LOCAL))
    writeFileSync(join(local, `nocx-helper-${platform}.gz`), gzipSync(Buffer.from(bytes)))
  for (const [platform, bytes] of Object.entries(REMOTE))
    writeFileSync(join(remote, `nocx-helper-${platform}.gz`), gzipSync(Buffer.from(bytes)))
  return dir
}

function run(block, dir) {
  return execFileSync('bash', ['-c', block], {
    cwd: dir,
    env: { ...process.env, RUNNER_TEMP: dir, VERSION: '0.0.0' },
    encoding: 'utf8',
  })
}

// THE THREE WRITERS AND THE ONE LOOKUP, EXECUTED. The signing job's file is
// later diffed against both build jobs' embedded lists, so format and order
// are part of the contract, not incidental.
test('every writer produces the one file the signing lookup reads', () => {
  const writers = [
    [
      'helpers job',
      stepRun('Hash the local helpers the build jobs must have embedded'),
      'helper-local-hashes.txt',
    ],
    [
      'signing job',
      stepRun('Hash the local helpers the build jobs must embed'),
      'helper-local-hashes-signed.txt',
    ],
    [
      'macOS build job',
      stepRuns('Hash the helper bytes this job embedded')[0],
      'embedded-local-hashes.txt',
    ],
    [
      'Linux build job',
      stepRuns('Hash the helper bytes this job embedded')[1],
      'embedded-local-hashes.txt',
    ],
  ]
  const produced = []
  for (const [label, step, file] of writers) {
    const dir = fixture()
    run(step, dir)
    const text = readFileSync(join(dir, file), 'utf8')
    produced.push([label, text])
    for (const [platform, bytes] of Object.entries(LOCAL)) {
      assert.equal(
        lookupHash(text, platform),
        sha256(bytes),
        `${label} wrote no ${platform} line the lookup can read:\n${text}`,
      )
    }
  }
  for (const [label, text] of produced.slice(1))
    assert.equal(text, produced[0][1], `${label} and the helpers job disagree on format or order`)
})

// THE READER, AS THE WORKFLOW SPELLS IT. Extracting the two assignments and
// running them is what would have caught `darwin/<arch>`: the writer's key is
// `darwin-<arch>`, and a reader that derives its own spelling stops here.
test('the signing step looks up the writer platform key and finds the hash', () => {
  const signing = stepRun("Sign the darwin local helpers with the app's identity")
  const assignment = (name) => {
    const line = signing
      .split('\n')
      .find((candidate) => new RegExp(`^\\s*${name}=`).test(candidate))
    assert.ok(line, `the signing step no longer assigns ${name}`)
    return line
  }
  const plat = assignment('plat')
  const want = assignment('want')
  assert.doesNotMatch(
    plat,
    /^\s*plat="darwin\//,
    `the reader derives its own platform spelling: ${plat}`,
  )
  assert.match(plat, /helper-hashes\.mjs platform/, `the reader has a second platform key: ${plat}`)
  assert.match(want, /helper-hashes\.mjs lookup/, `the reader has a second hash lookup: ${want}`)

  const dir = fixture()
  run(stepRun('Hash the local helpers the build jobs must have embedded'), dir)
  mkdirSync(join(dir, 'hashes/unsigned'), { recursive: true })
  renameSync(
    join(dir, 'helper-local-hashes.txt'),
    join(dir, 'hashes/unsigned/helper-local-hashes.txt'),
  )

  for (const arch of ['amd64', 'arm64']) {
    const script = [
      'set -euo pipefail',
      `arch=${arch}`,
      `gz=internal/helper/deploy/artifacts/bin/local/nocx-helper-darwin-${arch}.gz`,
      plat,
      want,
      `printf '%s\\n%s\\n' "$plat" "$want"`,
    ].join('\n')
    const [platform, hash] = run(script, dir).trim().split('\n')
    assert.equal(
      platform,
      `darwin-${arch}`,
      `the reader looks up ${platform}; the writer wrote darwin-${arch}`,
    )
    assert.equal(hash, sha256(LOCAL[`darwin-${arch}`]), `the reader found no hash for ${platform}`)
  }
})
