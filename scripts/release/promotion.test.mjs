import assert from 'node:assert/strict'
import { createHash, generateKeyPairSync, sign } from 'node:crypto'
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import {
  authorize,
  verifyCandidate,
  prepareStable,
  verifyFiles,
  verifyCI,
  updateCask,
} from './promotion.mjs'

const sha = 'a'.repeat(40)
const digest = (body) => createHash('sha256').update(body).digest('hex')
const { privateKey, publicKey } = generateKeyPairSync('ed25519')
const keys = [publicKey]
const signature = (body) => sign(null, body, privateKey).toString('base64') + '\n'

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'nocx-promotion-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const source = join(root, 'source')
  mkdirSync(source)
  const version = '0.8.0'
  const tag = 'nightly-20261027-42'
  const prefix = `https://github.com/shady2k/nocx/releases/download/${tag}/`
  const payloads = [
    `nocx-${version}-darwin-universal.zip`,
    `nocx-${version}-darwin-universal.dmg`,
    `nocx-${version}-linux-amd64.AppImage`,
    ...['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64'].map(
      (p) => `nocx-helper-${version}-${p}`,
    ),
  ]
  for (const name of payloads) writeFileSync(join(source, name), `tried nightly bytes: ${name}`)
  const describe = (name) => {
    const body = readFileSync(join(source, name))
    return { name, sha256: digest(body), size: body.length }
  }
  const entry = (name, os, arch, format) => {
    const { sha256, size } = describe(name)
    return { os, arch, ...(format ? { format } : {}), url: prefix + name, sha256, size }
  }
  const manifest = {
    version,
    released: '2026-10-27T00:00:00Z',
    notesUrl: `https://github.com/shady2k/nocx/releases/tag/${tag}`,
    artifacts: [
      entry(payloads[0], 'darwin', 'universal', 'zip'),
      entry(payloads[2], 'linux', 'amd64', 'AppImage'),
    ],
    helpers: payloads.slice(3).map((n) => {
      const [os, arch] = n.split('-').slice(-2)
      return entry(n, os, arch)
    }),
  }
  const manifestBody = Buffer.from(JSON.stringify(manifest))
  writeFileSync(join(source, 'manifest.json'), manifestBody)
  writeFileSync(join(source, 'manifest.json.sig'), signature(manifestBody))
  const nightly = {
    schema: 1,
    sha,
    version,
    tag,
    // The producer's own shape (publish-nightly.mjs): GitHub's env strings.
    runId: '42',
    runAttempt: '1',
    artifacts: [...payloads, 'manifest.json', 'manifest.json.sig'].map(describe),
  }
  const nightlyBody = Buffer.from(JSON.stringify(nightly))
  const ci = [
    {
      number: 12,
      head: 'b'.repeat(40),
      product: true,
      run: {
        id: 9,
        event: 'pull_request',
        head_sha: 'b'.repeat(40),
        path: '.github/workflows/ci.yml',
        status: 'completed',
        conclusion: 'success',
        pull_requests: [{ number: 12 }],
      },
      jobs: [
        'changes',
        'ci-backlog',
        'ci-mac',
        'ci-frontend',
        'ci-backend (with-secret-service)',
        'ci-backend (no-secret-service)',
        'ci-linux (with-secret-service)',
        'ci-linux (no-secret-service)',
        'ci-e2e (chromium, 1)',
        'ci-e2e (chromium, 2)',
        'ci-e2e (webkit, 1)',
        'ci-e2e (webkit, 2)',
      ].map((name) => ({ name, status: 'completed', conclusion: 'success' })),
    },
  ]
  ci[0].requiredJobs = ci[0].jobs.map((job) => job.name)
  const input = {
    repo: 'shady2k/nocx',
    tag,
    sha,
    keys,
    nightlyBody,
    nightlySignature: signature(nightlyBody),
    manifestBody,
    manifestSignature: signature(manifestBody),
    release: { tag_name: tag, draft: false, prerelease: true },
    run: {
      id: 42,
      run_attempt: 1,
      head_sha: sha,
      head_branch: 'main',
      event: 'schedule',
      path: '.github/workflows/nightly.yml',
      status: 'completed',
      conclusion: 'success',
    },
    readme: '**Status:** `v0.8.0`, early release. See [notes](docs/release-notes/v0.8.0.md).',
    notes: '# v0.8.0\nA release worth trying.',
    ci,
    previousVersion: '0.7.2',
  }
  return { input, source, root, nightly, manifest, describe }
}

function resign(input, nightly) {
  input.nightlyBody = Buffer.from(JSON.stringify(nightly))
  input.nightlySignature = signature(input.nightlyBody)
}

test('only the owner on main who confirms trying this nightly may promote', () => {
  const good = {
    actor: 'shady2k',
    triggeringActor: 'shady2k',
    owner: 'shady2k',
    ref: 'refs/heads/main',
    tried: true,
  }
  assert.doesNotThrow(() => authorize(good))
  for (const change of [
    { actor: 'collaborator' },
    { triggeringActor: 'collaborator' },
    { ref: 'refs/heads/other' },
    { tried: false },
  ]) {
    assert.throws(() => authorize({ ...good, ...change }))
  }
})

test('a verified candidate promotes the exact payload bytes and signs the stable URL manifest', (t) => {
  const { input, source, root, nightly } = fixture(t)
  const candidate = verifyCandidate(input)
  const output = join(root, 'stable')
  const plan = prepareStable(candidate, source, output, privateKey, keys)
  verifyFiles(output, plan.assets)
  for (const file of nightly.artifacts.filter((a) => !a.name.startsWith('manifest.'))) {
    assert.deepEqual(readFileSync(join(source, file.name)), readFileSync(join(output, file.name)))
  }
  const stable = JSON.parse(readFileSync(join(output, 'manifest.json')))
  assert.equal(stable.version, '0.8.0')
  assert.match(stable.artifacts[0].url, /\/download\/v0\.8\.0\//)
  assert.equal(plan.sha, sha)
  assert.equal(
    plan.macSHA256,
    digest(readFileSync(join(source, 'nocx-0.8.0-darwin-universal.zip'))),
  )
})

test('the signed run id and attempt are read in the form the producer signs them', (t) => {
  const { input } = fixture(t)
  assert.doesNotThrow(() => verifyCandidate(input))
  for (const [field, value] of [
    ['runId', 42],
    ['runId', '042'],
    ['runId', '4.2'],
    ['runId', ''],
    ['runId', 'x42'],
    ['runId', String(2 ** 60)],
    ['runAttempt', 1],
    ['runAttempt', '0'],
  ]) {
    const { input: changed, nightly } = fixture(t)
    nightly[field] = value
    resign(changed, nightly)
    assert.throws(() => verifyCandidate(changed), /nightly/i, `${field} ${JSON.stringify(value)}`)
  }
})

test('an unverified nightly and mismatched commit/run/attempt are refused', (t) => {
  for (const change of [
    { conclusion: 'failure' },
    { conclusion: null },
    { head_sha: 'c'.repeat(40) },
    { run_attempt: 2 },
    { path: '.github/workflows/other.yml' },
    { head_branch: 'feature' },
  ]) {
    const { input } = fixture(t)
    input.run = { ...input.run, ...change }
    assert.throws(() => verifyCandidate(input), /nightly/i)
  }
})

test('stale README refusal names v0.8.0; a version elsewhere or a wrong/missing notes link cannot satisfy it', (t) => {
  for (const readme of [
    '**Status:** `v0.7.2`.\nElsewhere: v0.8.0 [notes](docs/release-notes/v0.8.0.md)',
    '**Status:** `v0.8.0`. [notes](docs/release-notes/v0.7.2.md)',
    '**Status:** `v0.8.0`.',
    '**Status:** `v0.8.00`. [notes](docs/release-notes/v0.8.0.md)',
  ]) {
    const { input } = fixture(t)
    input.readme = readme
    assert.throws(() => verifyCandidate(input), /README.*v0\.8\.0/)
  }
  const { input } = fixture(t)
  input.notes = ''
  assert.throws(() => verifyCandidate(input), /v0\.8\.0/)
})

test('a rebuilt input or changed output digest is caught, including dmg which is not an updater payload', (t) => {
  for (const name of ['nocx-0.8.0-darwin-universal.dmg', 'nocx-0.8.0-linux-amd64.AppImage']) {
    const { input, source, root } = fixture(t)
    const candidate = verifyCandidate(input)
    const output = join(root, 'stable')
    const plan = prepareStable(candidate, source, output, privateKey, keys)
    writeFileSync(join(output, name), 'rebuilt output')
    assert.throws(() => verifyFiles(output, plan.assets), /digest|size/)
    writeFileSync(join(source, name), 'rebuilt input')
    assert.throws(
      () => prepareStable(candidate, source, join(root, 'other'), privateKey, keys),
      /digest|size/,
    )
  }
})

test('unsigned/tampered provenance and manifest, missing payloads and unsafe names/URLs fail closed', (t) => {
  for (const field of ['nightlySignature', 'manifestSignature']) {
    const { input } = fixture(t)
    input[field] = Buffer.alloc(64).toString('base64')
    assert.throws(() => verifyCandidate(input), /signature/)
  }
  for (const change of [
    (n) => {
      n.artifacts.pop()
    },
    (n) => {
      n.artifacts.push(n.artifacts[0])
    },
    (n) => {
      n.artifacts[0].name = '../escape'
    },
    (n) => {
      n.sha = 'c'.repeat(40)
    },
  ]) {
    const { input, nightly } = fixture(t)
    change(nightly)
    resign(input, nightly)
    assert.throws(() => verifyCandidate(input))
  }
  const { input, manifest, nightly } = fixture(t)
  manifest.artifacts[0].url = 'https://evil.example/a.zip'
  input.manifestBody = Buffer.from(JSON.stringify(manifest))
  input.manifestSignature = signature(input.manifestBody)
  nightly.artifacts.find((a) => a.name === 'manifest.json').sha256 = digest(input.manifestBody)
  nightly.artifacts.find((a) => a.name === 'manifest.json').size = input.manifestBody.length
  nightly.artifacts.find((a) => a.name === 'manifest.json.sig').sha256 = digest(
    input.manifestSignature,
  )
  resign(input, nightly)
  assert.throws(() => verifyCandidate(input), /URL/)
})

test('CI must belong to each merged PR HEAD; drafts/skipped product jobs, missing matrices, empty evidence and wrong workflow fail', (t) => {
  const { input } = fixture(t)
  verifyCI(input.ci)
  for (const change of [
    (e) => {
      e.run.head_sha = sha
    },
    (e) => {
      e.run.conclusion = 'failure'
    },
    (e) => {
      e.run.path = '.github/workflows/other.yml'
    },
    (e) => {
      e.run.pull_requests = []
    },
    (e) => {
      e.jobs[2].conclusion = 'skipped'
    },
    (e) => {
      e.jobs.pop()
    },
  ]) {
    const evidence = structuredClone(input.ci)
    change(evidence[0])
    assert.throws(() => verifyCI(evidence), /CI/)
  }
  assert.throws(() => verifyCI([]), /CI/)
  const supporting = structuredClone(input.ci)
  supporting[0].product = false
  supporting[0].jobs.slice(2).forEach((j) => {
    j.conclusion = 'skipped'
  })
  verifyCI(supporting)
})

test('stable cask pins the promoted zip and preserves unrelated cask text; malformed templates refuse', () => {
  const text =
    'cask "nocx" do\n  version "0.7.2"\n  sha256 "' +
    'a'.repeat(64) +
    '"\n  auto_updates true\nend\n'
  const result = updateCask(text, '0.8.0', 'b'.repeat(64))
  assert.match(result, /version "0.8.0"/)
  assert.ok(result.includes('sha256 "' + 'b'.repeat(64) + '"'))
  assert.match(result, /auto_updates true/)
  assert.throws(() =>
    updateCask(text.replace(/sha256.*/, 'sha256 :no_check'), '0.8.0', 'b'.repeat(64)),
  )
})

test('a release key whose base64 holds "//" is read whole, beside a real comment', async () => {
  const { readKeyring } = await import('./promotion.mjs')
  // Base64 uses "/", so roughly one key in a hundred contains "//". Draw keys
  // until one does: the outcome is fixed even though the key is not.
  let raw
  do {
    raw = generateKeyPairSync('ed25519')
      .publicKey.export({ format: 'der', type: 'spki' })
      .subarray(-32)
      .toString('base64')
  } while (!raw.includes('//'))
  const keys = readKeyring(
    `package update\nvar releaseKeys = []string{\n\t// a comment // with slashes\n\t"${raw}", // trailing\n}\n`,
  )
  assert.equal(keys.length, 1)
  assert.equal(
    keys[0].export({ format: 'der', type: 'spki' }).subarray(-32).toString('base64'),
    raw,
  )
})

test('candidate keyring and seed interoperate with the raw detached Ed25519 contract', async () => {
  const { readKeyring, signingKey, verifySignature } = await import('./promotion.mjs')
  const rawPublic = publicKey
    .export({ format: 'der', type: 'spki' })
    .subarray(-32)
    .toString('base64')
  const rawSeed = privateKey
    .export({ format: 'der', type: 'pkcs8' })
    .subarray(-32)
    .toString('base64')
  const parsed = readKeyring(
    `package update\nvar releaseKeys = []string{\n // key from owner\n "${rawPublic}",\n}\n`,
  )
  const body = Buffer.from('exact manifest bytes\n')
  const sig = sign(null, body, signingKey(rawSeed)).toString('base64') + '\n'
  verifySignature(body, sig, parsed)
  assert.throws(() => verifySignature(Buffer.from('changed'), sig, parsed), /signature/)
  assert.throws(() => signingKey(''), /RELEASE_SIGNING_KEY/)
  assert.throws(() => readKeyring('var releaseKeys = []string{\n}\n'), /empty/)
})

test('README must have one status, published prerelease must be dated and version must advance', (t) => {
  for (const change of [
    (i) => {
      i.readme += '\n' + i.readme
    },
    (i) => {
      i.tag = 'nightly'
    },
    (i) => {
      i.release.draft = true
    },
    (i) => {
      i.release.prerelease = false
    },
    (i) => {
      i.previousVersion = '0.8.0'
    },
    (i) => {
      i.previousVersion = '0.9.0'
    },
  ]) {
    const { input } = fixture(t)
    change(input)
    assert.throws(() => verifyCandidate(input))
  }
})

test('a missing download or an existing staging directory cannot become a promotion', (t) => {
  const { input, source, root } = fixture(t)
  const candidate = verifyCandidate(input)
  mkdirSync(join(root, 'exists'))
  assert.throws(
    () => prepareStable(candidate, source, join(root, 'exists'), privateKey, keys),
    /EEXIST/,
  )
  rmSync(join(source, 'nocx-0.8.0-darwin-universal.zip'))
  assert.throws(
    () => prepareStable(candidate, source, join(root, 'stable'), privateKey, keys),
    /ENOENT/,
  )
})

test('a substituted signing secret cannot sign a manifest clients of this nightly can verify', (t) => {
  const { input, source, root } = fixture(t)
  const other = generateKeyPairSync('ed25519')
  assert.throws(
    () =>
      prepareStable(verifyCandidate(input), source, join(root, 'stable'), other.privateKey, keys),
    /signature/,
  )
})

test('supporting CI uses real GitHub collapsed skipped matrices, not invented green matrix legs', (t) => {
  // Observed via GET actions/runs/40295304087/jobs on 2026-10-27.
  const { input } = fixture(t)
  const evidence = structuredClone(input.ci)
  evidence[0].product = false
  evidence[0].jobs = [
    'changes',
    'ci-backlog',
    'ci-backend',
    'ci-e2e',
    'ci-mac',
    'ci-frontend',
    'ci-linux',
  ].map((name) => ({
    name,
    status: 'completed',
    conclusion: ['changes', 'ci-backlog'].includes(name) ? 'success' : 'skipped',
  }))
  verifyCI(evidence)
  evidence[0].product = true
  assert.throws(() => verifyCI(evidence), /CI/)
})

test('a signed manifest with wrong platform metadata is refused before stable re-signing', (t) => {
  for (const mutate of [
    (m) => {
      m.artifacts[0].os = 'linux'
    },
    (m) => {
      m.artifacts[1].arch = 'universal'
    },
    (m) => {
      m.helpers[0].format = 'zip'
    },
  ]) {
    const { input, nightly, manifest } = fixture(t)
    mutate(manifest)
    input.manifestBody = Buffer.from(JSON.stringify(manifest))
    input.manifestSignature = signature(input.manifestBody)
    for (const [name, body] of [
      ['manifest.json', input.manifestBody],
      ['manifest.json.sig', Buffer.from(input.manifestSignature)],
    ]) {
      Object.assign(
        nightly.artifacts.find((a) => a.name === name),
        { size: body.length, sha256: digest(body) },
      )
    }
    resign(input, nightly)
    assert.throws(() => verifyCandidate(input), /tuples/)
  }
})
