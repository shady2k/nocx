import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { ciJobNames, ciStepManifests, verifyCI } from './promotion.mjs'
import { collectCI, githubClient, publish, verifyUploaded } from './promotion-github.mjs'

const repo = 'shady2k/nocx'
const head = 'b'.repeat(40)
const commit = 'a'.repeat(40)
const workflow =
  'jobs:\n  changes:\n    runs-on: ubuntu-latest\n  ci-backlog:\n    runs-on: ubuntu-latest\n  ci-linux:\n    strategy:\n      matrix:\n        keyring: [with-secret-service, no-secret-service]\n'
function fixture() {
  const pr = {
    number: 12,
    head: { sha: head },
    merged_at: '2026-10-27T00:00:00Z',
    merge_commit_sha: commit,
    base: { ref: 'main', repo: { full_name: repo } },
  }
  const run = {
    id: 22,
    run_attempt: 1,
    head_sha: head,
    event: 'pull_request',
    path: '.github/workflows/ci.yml',
    conclusion: 'success',
    status: 'completed',
    pull_requests: [{ number: 12 }],
  }
  const jobs = ciJobNames(workflow).map((name) => ({
    name,
    conclusion: 'success',
    status: 'completed',
  }))
  const responses = new Map([
    [`repos/${repo}/commits/${commit}/pulls`, [pr]],
    [`repos/${repo}/pulls/12/files`, [{ filename: 'main.go' }]],
    [`repos/${repo}/actions/workflows/ci.yml/runs?event=pull_request&head_sha=${head}`, [run]],
    [`repos/${repo}/actions/runs/22/attempts/1/jobs`, jobs],
  ])
  const github = {
    pages: (url) => {
      assert.ok(responses.has(url), url)
      return responses.get(url)
    },
  }
  const options = {
    repo,
    commits: [commit],
    github,
    loadWorkflow: (sha) => {
      assert.equal(sha, head)
      return workflow
    },
    touchesProduct: (files) => files.includes('main.go'),
  }
  return { options, responses, pr, run, jobs }
}

test('job contract expands the actual CI matrix and refuses unsupported syntax', () => {
  const names = ciJobNames(readFileSync('.github/workflows/ci.yml', 'utf8'))
  const original = [
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
  ]
  for (const name of original) assert.ok(names.includes(name), `missing original CI job ${name}`)
  assert.deepEqual(
    ciJobNames(readFileSync('.github/workflows/ci.yml', 'utf8'), { gated: true }),
    original.filter((name) => /^ci-(mac|backend|linux|e2e)( |$)/.test(name)),
  )
  assert.throws(
    () =>
      ciJobNames(
        workflow.replace(
          '[with-secret-service, no-secret-service]',
          '${{ fromJSON(inputs.matrix) }}',
        ),
      ),
    /unsupported/,
  )
})

test('CI adapter resolves merge commit to PR head, classifies files and fetches authoritative workflow jobs', async () => {
  const { options } = fixture()
  const result = await collectCI(options)
  assert.equal(result[0].head, head)
  assert.equal(result[0].number, 12)
  assert.equal(result[0].product, true)
})

test('CI adapter refuses API errors, uncovered main commits, foreign PRs, no CI, and failed newest CI', async () => {
  for (const mutate of [
    (f) => {
      f.options.github.pages = () => {
        throw new Error('GitHub unavailable')
      }
    },
    (f) => {
      f.responses.set(`repos/${repo}/commits/${commit}/pulls`, [])
    },
    (f) => {
      f.pr.base.repo.full_name = 'foreign/nocx'
    },
    (f) => {
      f.pr.merge_commit_sha = head
    },
    (f) => {
      f.responses.set(
        `repos/${repo}/actions/workflows/ci.yml/runs?event=pull_request&head_sha=${head}`,
        [],
      )
    },
    (f) => {
      f.responses
        .get(`repos/${repo}/actions/workflows/ci.yml/runs?event=pull_request&head_sha=${head}`)
        .push({ ...f.run, id: 23, conclusion: 'failure' })
      f.responses.set(`repos/${repo}/actions/runs/23/attempts/1/jobs`, f.jobs)
    },
  ]) {
    const f = fixture()
    mutate(f)
    await assert.rejects(() => collectCI(f.options))
  }
})

test('a red CI run is read whole: earlier attempts and each failed job log, quarantine attached', async () => {
  const f = fixture()
  f.run.conclusion = 'failure'
  f.run.run_attempt = 2
  const latest = f.jobs.map((job, i) => ({ ...job, id: 100 + i }))
  latest[0] = { ...latest[0], conclusion: 'failure' }
  const first = latest.map((job) => ({ ...job, id: job.id + 50 }))
  f.responses.set(`repos/${repo}/actions/runs/22/attempts/2/jobs`, latest)
  f.responses.set(`repos/${repo}/actions/runs/22/attempts/1/jobs`, first)
  const logs = []
  f.options.github.gh = (args) => {
    logs.push(args.join(' '))
    return '2026-10-29T00:00:00Z setup failed\n'
  }
  f.options.quarantine = { 'job:changes#12': 'nocx-x' }
  const [entry] = await collectCI(f.options)
  assert.deepEqual(entry.quarantine, { 'job:changes#12': 'nocx-x' })
  assert.deepEqual(
    entry.attempts.map((a) => a.attempt),
    [1, 2],
  )
  assert.deepEqual(entry.attempts[1].jobs[0].failures, [])
  assert.equal(entry.attempts[1].jobs[0].complete, false)
  assert.deepEqual(logs, [
    `api --allow-escape-sequences repos/${repo}/actions/jobs/150/logs`,
    `api --allow-escape-sequences repos/${repo}/actions/jobs/100/logs`,
  ])
})

test('a direct push is judged by the later green PR that covers it, or by being supporting', async () => {
  const direct = 'd'.repeat(40)
  const build = (product, ancestor) => {
    const f = fixture()
    f.options.commits = [direct, commit]
    f.responses.set(`repos/${repo}/commits/${direct}/pulls`, [])
    f.options.commitFiles = () => (product ? ['.github/workflows/ci.yml'] : ['.beads/issues.jsonl'])
    f.options.touchesProduct = (files) => files.some((name) => !name.startsWith('.beads/'))
    f.options.isAncestor = () => ancestor
    return f
  }
  const supporting = await collectCI(build(false, false).options)
  assert.equal(supporting[0].direct, true)
  assert.equal(supporting[0].product, false)
  const covered = await collectCI(build(true, true).options)
  assert.equal(covered[0].coveredBy, 12)
  await assert.rejects(() => collectCI(build(true, false).options), /no later green PR/)
  const noRecord = fixture()
  noRecord.options.commits = [direct, commit]
  noRecord.responses.set(`repos/${repo}/commits/${direct}/pulls`, [])
  await assert.rejects(() => collectCI(noRecord.options), /exactly one merged main PR/)
})

test('GitHub list reader follows every page and propagates a missing next page', () => {
  const calls = []
  const client = githubClient((_program, args) => {
    calls.push(args[1])
    return JSON.stringify(
      args[1].endsWith('&page=1') ? Array.from({ length: 100 }, (_, n) => n) : [100],
    )
  })
  assert.equal(client.pages('repos/example/commits').length, 101)
  assert.deepEqual(calls, [
    'repos/example/commits?per_page=100&page=1',
    'repos/example/commits?per_page=100&page=2',
  ])
  const broken = githubClient((_program, args) => {
    if (args[1].endsWith('page=2')) throw new Error('missing second page')
    return JSON.stringify(Array(100).fill({}))
  })
  assert.throws(() => broken.pages('list'), /second page/)
})

test('post-upload verification downloads fresh bytes; changed digest, extra assets and API errors refuse', (t) => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-upload-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const bytes = Buffer.from('tried bytes')
  const asset = {
    name: 'nocx.zip',
    size: bytes.length,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  }
  const plan = { tag: 'v0.8.0', assets: [asset] }
  let serial = 0
  for (const failure of [null, 'changed', 'extra', 'api', 'twice']) {
    const github = {
      gh: (args) => {
        assert.equal(args[0], 'release')
        assert.equal(args[1], 'download')
        writeFileSync(
          join(args.at(-1), asset.name),
          failure === 'changed' ? 'rebuilt bytes' : bytes,
        )
      },
      // The release list, not releases/tags/<tag>: that endpoint 404s for a
      // draft, which is the state this check first runs in.
      pages: (path) => {
        assert.equal(path, `repos/${repo}/releases`)
        if (failure === 'api') throw new Error('GitHub unavailable')
        return [
          { tag_name: 'v0.7.2', assets: [] },
          {
            tag_name: 'v0.8.0',
            draft: true,
            assets: failure === 'extra' ? [asset, { name: 'extra' }] : [asset],
          },
          ...(failure === 'twice' ? [{ tag_name: 'v0.8.0', assets: [asset] }] : []),
        ]
      },
      api: () => {
        throw new Error('releases/tags/<tag> 404s for a draft and must not be asked')
      },
    }
    const run = () => verifyUploaded(github, repo, plan, join(root, String(serial++)))
    if (failure) assert.throws(run)
    else assert.doesNotThrow(run)
  }
})

test('manual promotion has no build route, secret-free authorization and post-upload gate before cask', () => {
  const release = readFileSync('.github/workflows/release.yml', 'utf8')
  assert.match(release, /workflow_dispatch:/)
  assert.doesNotMatch(release, /^ {2}push:|uses: .*ci\.yml|uses: .*release-build\.yml/m)
  assert.doesNotMatch(release, /go (?:run|build)|npm |make |codesign |ditto |hdiutil /)
  assert.match(release, /default: false/)
  assert.match(release, /needs: authorize/)
  assert.match(release, /github\.triggering_actor == github\.repository_owner/)
  assert.match(release, /cancel-in-progress: false/)
  assert.ok(release.indexOf('publish promotion') < release.indexOf('cask promotion'))
  assert.ok(release.indexOf('collect promotion') < release.indexOf('secrets.RELEASE_SIGNING_KEY'))
})

test('publishing explicitly pins the tag, verifies draft downloads before latest, and never overwrites a conflict', (t) => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-publish-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const body = Buffer.from('verified byte fixture')
  const asset = {
    name: 'nocx.zip',
    size: body.length,
    sha256: createHash('sha256').update(body).digest('hex'),
  }
  const plan = { tag: 'v0.8.0', sha: commit, assets: [asset] }
  // Use a fresh fixture per case because failed operations must not reuse directories.
  for (const failure of [null, 'conflict', 'upload', 'changed', 'publish']) {
    const location = join(root, failure ?? 'good')
    mkdirSync(location)
    mkdirSync(join(location, 'stable'))
    writeFileSync(join(location, 'stable', asset.name), body)
    writeFileSync(join(location, 'plan.json'), JSON.stringify(plan))
    const calls = []
    // GitHub's state: the release exists, as a draft and then published, only
    // once `release create` has run; the list is where verifyUploaded reads it.
    let created = false
    const github = {
      pages: (url) => {
        if (url.includes('matching-refs'))
          return failure === 'conflict' ? [{ ref: `refs/tags/${plan.tag}` }] : []
        return created ? [{ tag_name: plan.tag, assets: [asset] }] : []
      },
      api: (url) =>
        url.includes('git/ref/')
          ? { object: { type: 'commit', sha: commit } }
          : { assets: [asset] },
      gh: (args) => {
        calls.push(args)
        if (args[0] === 'release' && args[1] === 'create') {
          if (failure === 'upload') throw new Error('upload failed')
          created = true
        }
        if (args[0] === 'release' && args[1] === 'edit' && failure === 'publish')
          throw new Error('publish failed')
        if (args[0] === 'release' && args[1] === 'download')
          writeFileSync(
            join(args.at(-1), asset.name),
            failure === 'changed' ? 'changed bytes' : body,
          )
      },
    }
    if (failure) assert.throws(() => publish(location, github))
    else assert.doesNotThrow(() => publish(location, github))
    if (failure === 'conflict') assert.equal(calls.length, 0)
    else {
      assert.equal(calls[0][0], 'api')
      assert.ok(calls[0].includes(`sha=${commit}`))
      assert.ok(calls[1].includes('--draft'))
    }
    const publishIndex = calls.findIndex((args) => args[1] === 'edit')
    if (failure === 'changed' || failure === 'upload') assert.equal(publishIndex, -1)
    if (!failure) {
      assert.equal(calls[publishIndex - 1][1], 'download')
      assert.equal(calls[publishIndex + 1][1], 'download')
    }
  }
})

// A gate can make a failed test job API-green. Promotion must read that job's
// retained log record, not mistake GitHub's conclusion for a clean suite.
function gatedFixture(
  steps = [{ name: 'go-backend', status: 1, failures: ['go:TestFlaky'], complete: true }],
) {
  const f = fixture()
  f.options.loadWorkflow = () =>
    workflow +
    `    steps:
      - name: Honour test quarantine
        env:
          NOCX_EXPECTED_STEPS: ${steps.map((step) => step.name).join(' ')}
        run: node scripts/quarantine-gate.mjs
`
  f.records = new Map()
  f.logs = []
  for (const [index, job] of f.jobs.entries()) {
    job.id = 100 + index
    if (!job.name.startsWith('ci-linux (')) continue
    f.records.set(job.id, {
      schema: 1,
      job: job.name,
      pull: '12',
      runId: '22',
      attempt: 1,
      manifestDigest: createHash('sha256')
        .update(JSON.stringify(steps.map((step) => step.name).sort()))
        .digest('hex'),
      steps: structuredClone(steps),
    })
  }
  f.options.github.gh = (args) => {
    f.logs.push(args)
    const id = Number(/jobs\/(\d+)\/logs$/.exec(args.at(-1))[1])
    return (
      '2026-10-29T00:00:00Z NOCX_QUARANTINE_EVIDENCE_V1 ' + JSON.stringify(f.records.get(id)) + '\n'
    )
  }
  return f
}

test('API-green gated failures are rejudged against the promotion quarantine', async () => {
  const f = gatedFixture()
  await assert.rejects(() => collectCI(f.options), /no open bead quarantines/)
})

test('API-green quarantine evidence is retained, including passing steps, and rejudged on saved verification', async () => {
  const steps = [
    { name: 'go-backend', status: 1, failures: ['go:TestFlaky'], complete: true },
    { name: 'go-local-ssh', status: 0, failures: [], complete: true },
  ]
  const f = gatedFixture(steps)
  f.options.quarantine = { 'go:TestFlaky': 'nocx-x' }
  const [entry] = await collectCI(f.options)
  assert.equal(f.logs.length, 2)
  assert.deepEqual(
    entry.gatedJobs,
    f.jobs.slice(2).map((job) => job.name),
  )
  for (const job of entry.jobs.slice(2)) assert.deepEqual(job.quarantineEvidence.steps, steps)
  const { verifyCI } = await import('./promotion.mjs')
  for (const contract of [[], 'ci-linux', ['missing'], [entry.gatedJobs[0], entry.gatedJobs[0]]]) {
    assert.throws(() => verifyCI([{ ...entry, gatedJobs: contract }]), /contract/)
  }
  const withoutContract = JSON.parse(JSON.stringify(entry))
  delete withoutContract.gatedJobs
  withoutContract.quarantine = {}
  assert.throws(() => verifyCI([withoutContract]), /no open bead quarantines/)
  const saved = JSON.parse(JSON.stringify(entry))
  saved.quarantine = {}
  assert.throws(() => verifyCI([saved]), /no open bead quarantines/)
  delete saved.jobs[2].quarantineEvidence
  assert.throws(() => verifyCI([saved]), /evidence/)
})

test('successful gated jobs require one valid matching record per matrix leg', async () => {
  for (const damage of [
    () => '',
    () => 'NOCX_QUARANTINE_EVIDENCE_V1 not-json',
    (log) => log + log,
    (log) => log.replace('"pull":"12"', '"pull":"13"'),
    (log) => log.replace('with-secret-service)', 'other-job)'),
    (log) => log.replace('"schema":1', '"schema":2'),
    (log) => log.replace('"status":0', '"status":"0"'),
    (log) => log.replace('"complete":true', '"complete":null'),
    (log) => log.replace('"complete":true', '"complete":false'),
    (log) => log.replace('"failures":[]', '"failures":["go:TestHidden"]'),
    (log) => log.replace('"status":0', '"status":-1'),
    (log) => log.replace('"steps":[{', '"steps":[] ,"ignored":[{'),
    () => {
      throw new Error('job logs unavailable')
    },
  ]) {
    const f = gatedFixture([{ name: 'tests', status: 0, failures: [], complete: true }])
    const read = f.options.github.gh
    f.options.github.gh = (args) => damage(read(args))
    await assert.rejects(() => collectCI(f.options))
  }
})

test('a gated quarantine cannot cover a direct product commit, but clean gated suites can', async () => {
  const direct = 'd'.repeat(40)
  for (const failed of [false, true]) {
    const f = gatedFixture([
      {
        name: 'tests',
        status: failed ? 1 : 0,
        failures: failed ? ['go:TestFlaky'] : [],
        complete: true,
      },
    ])
    f.options.commits = [direct, commit]
    f.responses.set(`repos/${repo}/commits/${direct}/pulls`, [])
    f.options.commitFiles = () => ['main.go']
    f.options.isAncestor = () => true
    f.options.quarantine = { 'go:TestFlaky': 'nocx-x' }
    if (failed) await assert.rejects(() => collectCI(f.options), /no later green PR/)
    else assert.equal((await collectCI(f.options))[0].coveredBy, 12)
  }
})

test('a gated named failure with incomplete evidence cannot be quarantined', async () => {
  const f = gatedFixture([
    { name: 'tests', status: 1, failures: ['go:TestFlaky'], complete: false },
  ])
  f.options.quarantine = { 'go:TestFlaky': 'nocx-x' }
  await assert.rejects(() => collectCI(f.options), /incomplete|complete/)
})

test('a failed run still rejudges API-green gated jobs and keeps whole-job exceptions PR-specific', async () => {
  const f = gatedFixture([{ name: 'tests', status: 1, failures: [], complete: false }])
  f.run.conclusion = 'failure'
  f.jobs[0].conclusion = 'failure'
  const read = f.options.github.gh
  f.options.github.gh = (args) => (args.at(-1).includes('/100/') ? 'setup failed' : read(args))
  f.options.quarantine = { 'job:changes#12': 'nocx-setup' }
  for (const job of f.jobs.slice(2)) f.options.quarantine[`job:${job.name}#13`] = 'nocx-wrong-pr'
  await assert.rejects(() => collectCI(f.options), /never passed/)
  for (const job of f.jobs.slice(2)) f.options.quarantine[`job:${job.name}#12`] = 'nocx-correct-pr'
  const [entry] = await collectCI(f.options)
  assert.equal(entry.attempts[0].jobs[2].quarantineEvidence.steps[0].status, 1)
})

test('supporting-only skipped gated matrices do not invent gate records', async () => {
  const f = gatedFixture()
  f.options.touchesProduct = () => false
  f.jobs.splice(2, 2, { name: 'ci-linux', conclusion: 'skipped', status: 'completed' })
  await collectCI(f.options)
  assert.equal(f.logs.length, 0)
})

test('legacy named text cannot assert complete failure evidence for promotion', async () => {
  const f = fixture()
  f.run.conclusion = 'failure'
  f.jobs[0].conclusion = 'failure'
  f.jobs[0].id = 100
  f.options.github.gh = () => '--- FAIL: TestFlaky (1s)\n'
  f.options.quarantine = { 'go:TestFlaky': 'nocx-x', 'job:changes#12': 'nocx-job' }
  await assert.rejects(() => collectCI(f.options), /incomplete|complete/)
})

test('older API-green gated attempts are rejudged even when the latest run is green', async () => {
  const f = gatedFixture([{ name: 'tests', status: 0, failures: [], complete: true }])
  f.run.run_attempt = 2
  f.options.quarantine = { 'job:changes#12': 'nocx-setup' }
  f.responses.set(`repos/${repo}/actions/runs/22/attempts/2/jobs`, f.jobs)
  const first = f.jobs.map((job) => ({ ...job, id: job.id + 100 }))
  f.responses.set(`repos/${repo}/actions/runs/22/attempts/1/jobs`, first)
  for (const job of f.jobs.slice(2)) f.records.get(job.id).attempt = 2
  for (const job of first.slice(2)) {
    f.records.set(job.id, {
      ...structuredClone(f.records.get(job.id - 100)),
      attempt: 1,
      schema: 1,
      job: job.name,
      pull: '12',
      steps: [{ name: 'tests', status: 1, failures: ['go:TestClosed'], complete: true }],
    })
  }
  const read = f.options.github.gh
  f.options.github.gh = (args) =>
    /\/(100|200)\/logs$/.test(args.at(-1)) ? 'setup failed' : read(args)
  await assert.rejects(() => collectCI(f.options), /no open bead quarantines/)
})

const passingStep = { name: 'tests', status: 0, failures: [], complete: true }
const evidenceSubstitutions = [
  [
    'other run',
    (record) => {
      record.runId = '21'
    },
  ],
  [
    'numeric run',
    (record) => {
      record.runId = 22
    },
  ],
  [
    'missing run',
    (record) => {
      delete record.runId
    },
  ],
  [
    'other attempt',
    (record) => {
      record.attempt = 2
    },
  ],
  [
    'string attempt',
    (record) => {
      record.attempt = '1'
    },
  ],
  [
    'missing attempt',
    (record) => {
      delete record.attempt
    },
  ],
  [
    'other digest',
    (record) => {
      record.manifestDigest = 'a'.repeat(64)
    },
  ],
  [
    'missing digest',
    (record) => {
      delete record.manifestDigest
    },
  ],
  [
    'omitted step',
    (record) => {
      record.steps.pop()
    },
  ],
  [
    'renamed step',
    (record) => {
      record.steps[0].name = 'substitute'
    },
  ],
  [
    'extra step',
    (record) => {
      record.steps.push({ ...passingStep, name: 'extra' })
    },
  ],
  [
    'self-consistent partial manifest',
    (record) => {
      record.steps.pop()
      record.manifestDigest = createHash('sha256')
        .update(JSON.stringify(record.steps.map((step) => step.name).sort()))
        .digest('hex')
    },
  ],
]

test('collected API-green records bind the run, attempt and independent workflow step manifest', async () => {
  for (const [label, substitute] of evidenceSubstitutions) {
    const f = gatedFixture([passingStep, { ...passingStep, name: 'second' }])
    substitute(f.records.get(f.jobs[2].id))
    await assert.rejects(() => collectCI(f.options), /evidence/, label)
  }
})

test('saved API-green records recheck run, attempt, manifest digest and exact step names', async () => {
  const f = gatedFixture([passingStep, { ...passingStep, name: 'second' }])
  const [entry] = await collectCI(f.options)
  for (const [label, substitute] of evidenceSubstitutions) {
    const saved = JSON.parse(JSON.stringify(entry))
    substitute(saved.jobs[2].quarantineEvidence)
    assert.throws(() => verifyCI([saved]), /evidence/, label)
  }
})

function greenRerunFixture() {
  const f = gatedFixture([passingStep, { ...passingStep, name: 'second' }])
  f.run.run_attempt = 2
  f.responses.set(`repos/${repo}/actions/runs/22/attempts/2/jobs`, f.jobs)
  const first = f.jobs.map((job) => ({ ...job, id: job.id + 100 }))
  f.responses.set(`repos/${repo}/actions/runs/22/attempts/1/jobs`, first)
  for (const job of first.slice(2)) {
    f.records.set(job.id, structuredClone(f.records.get(job.id - 100)))
    f.records.get(job.id - 100).attempt = 2
  }
  return f
}

test('historical API-green records bind their own attempt during collection and saved verification', async () => {
  const good = greenRerunFixture()
  const [entry] = await collectCI(good.options)
  assert.deepEqual(
    entry.attempts.map((item) => item.attempt),
    [1, 2],
  )
  for (const [label, substitute] of evidenceSubstitutions) {
    const f = greenRerunFixture()
    substitute(f.records.get(f.jobs[2].id + 100))
    await assert.rejects(() => collectCI(f.options), /evidence/, label)
    const saved = JSON.parse(JSON.stringify(entry))
    substitute(saved.attempts[0].jobs[2].quarantineEvidence)
    assert.throws(() => verifyCI([saved]), /evidence/, label)
  }
})

test('gate manifests come from each actual workflow job and matrix choice', () => {
  const manifests = ciStepManifests(readFileSync('.github/workflows/ci.yml', 'utf8'))
  assert.deepEqual(
    Object.keys(manifests),
    ciJobNames(readFileSync('.github/workflows/ci.yml', 'utf8'), { gated: true }),
  )
  assert.deepEqual(manifests['ci-mac'], [
    'go-mac-zsh-plugins',
    'go-mac-fish',
    'go-mac-os',
    'go-mac-local-ssh',
    'go-mac-release-tag',
    'e2e-macos-first-command',
  ])
  for (const keyring of ['with-secret-service', 'no-secret-service']) {
    assert.deepEqual(manifests[`ci-backend (${keyring})`], [
      'go-backend',
      'go-backend-local-ssh',
      ...(keyring === 'no-secret-service' ? ['go-backend-alloc-budgets'] : []),
    ])
    assert.deepEqual(manifests[`ci-linux (${keyring})`], [
      'go-linux-fish',
      'go-linux',
      'go-linux-local-ssh',
    ])
  }
  for (const project of ['chromium', 'webkit']) {
    for (const shard of ['1', '2']) {
      assert.deepEqual(manifests[`ci-e2e (${project}, ${shard})`], [`e2e-${project}-${shard}`])
    }
  }
})

test('unsupported or missing workflow manifest declarations and expressions fail closed', () => {
  const actual = readFileSync('.github/workflows/ci.yml', 'utf8')
  for (const change of [
    (source) => source.replace(/NOCX_EXPECTED_STEPS: [^\n]+/, 'NOCX_EXPECTED_STEPS: tests tests'),
    (source) =>
      source.replace(/NOCX_EXPECTED_STEPS: [^\n]+/, 'NOCX_EXPECTED_STEPS: |\n            tests'),
    (source) =>
      source.replace(/NOCX_EXPECTED_STEPS: [^\n]+/, 'NOCX_EXPECTED_STEPS: ${{ inputs.steps }}'),
    (source) =>
      source.replace(/NOCX_EXPECTED_STEPS: [^\n]+/, 'NOCX_EXPECTED_STEPS: ${{ matrix.absent }}'),
    (source) =>
      source.replace(
        /NOCX_EXPECTED_STEPS: [^\n]+/,
        "NOCX_EXPECTED_STEPS: ${{ format('tests-{0}', matrix.keyring) }}",
      ),
    (source) => source.replace(/ {10}NOCX_EXPECTED_STEPS: [^\n]+\n/, ''),
    (source) => source.replace(/( {10}NOCX_EXPECTED_STEPS: [^\n]+\n)/, '$1$1'),
    (source) =>
      source.replace(
        'run: node scripts/quarantine-gate.mjs',
        'run: |\n          node scripts/quarantine-gate.mjs',
      ),
  ])
    assert.throws(() => ciStepManifests(change(actual)), /manifest|expression|declaration/)
})

test('saved gated evidence must retain workflow source and uses it rather than record names', async () => {
  const f = gatedFixture([passingStep])
  const [entry] = await collectCI(f.options)
  assert.equal(entry.workflow, f.options.loadWorkflow(head))
  for (const change of [
    (saved) => {
      delete saved.workflow
    },
    (saved) => {
      saved.workflow = saved.workflow.replace(
        'NOCX_EXPECTED_STEPS: tests',
        'NOCX_EXPECTED_STEPS: second',
      )
    },
    (saved) => {
      saved.run.run_attempt++
    },
    (saved) => {
      saved.run.id++
    },
    (saved) => {
      delete saved.gatedJobs
      delete saved.jobs[2].quarantineEvidence
    },
  ]) {
    const saved = JSON.parse(JSON.stringify(entry))
    change(saved)
    assert.throws(() => verifyCI([saved]), /evidence|workflow/)
  }
})

test('saved workflow gates and their earlier attempts cannot be dropped from the contract', async () => {
  const [entry] = await collectCI(greenRerunFixture().options)
  for (const change of [
    (saved) => {
      delete saved.attempts
    },
    (saved) => {
      saved.attempts.shift()
    },
    (saved) => {
      saved.attempts[1].attempt = 1
    },
    (saved) => {
      delete saved.gatedJobs
      saved.jobs = saved.jobs.slice(0, 2)
      saved.requiredJobs = saved.requiredJobs.slice(0, 2)
    },
  ]) {
    const saved = JSON.parse(JSON.stringify(entry))
    change(saved)
    assert.throws(() => verifyCI([saved]), /contract|history/)
  }
})
