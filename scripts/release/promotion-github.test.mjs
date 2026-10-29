import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { ciJobNames } from './promotion.mjs'
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
    return '2026-10-29T00:00:00Z --- FAIL: TestFlaky (1.0s)\n'
  }
  f.options.quarantine = { 'go:TestFlaky': 'nocx-x' }
  const [entry] = await collectCI(f.options)
  assert.deepEqual(entry.quarantine, { 'go:TestFlaky': 'nocx-x' })
  assert.deepEqual(
    entry.attempts.map((a) => a.attempt),
    [1, 2],
  )
  assert.deepEqual(entry.attempts[1].jobs[0].failures, ['go:TestFlaky'])
  assert.deepEqual(logs, [
    `api repos/${repo}/actions/jobs/150/logs`,
    `api repos/${repo}/actions/jobs/100/logs`,
  ])
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
  for (const failure of [null, 'changed', 'extra', 'api']) {
    const github = {
      gh: (args) => {
        assert.equal(args[0], 'release')
        assert.equal(args[1], 'download')
        writeFileSync(
          join(args.at(-1), asset.name),
          failure === 'changed' ? 'rebuilt bytes' : bytes,
        )
      },
      api: () => {
        if (failure === 'api') throw new Error('GitHub unavailable')
        return { assets: failure === 'extra' ? [asset, { name: 'extra' }] : [asset] }
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
    const github = {
      pages: (url) =>
        url.includes('matching-refs') && failure === 'conflict'
          ? [{ ref: `refs/tags/${plan.tag}` }]
          : [],
      api: (url) =>
        url.includes('git/ref/')
          ? { object: { type: 'commit', sha: commit } }
          : { assets: [asset] },
      gh: (args) => {
        calls.push(args)
        if (args[0] === 'release' && args[1] === 'create' && failure === 'upload')
          throw new Error('upload failed')
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
