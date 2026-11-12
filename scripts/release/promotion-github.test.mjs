import assert from 'node:assert/strict'
import { createHash, createPublicKey, sign } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { signingKey } from './promotion.mjs'
import { collect, githubClient, publish, verifyUploaded } from './promotion-github.mjs'

const repo = 'shady2k/nocx'
const commit = 'a'.repeat(40)

// The same fixed Ed25519 material promotion.test.mjs uses: the collector reads
// the candidate's compiled keyring, so the nightly it verifies must be signed by
// a key that keyring holds.
const privateKey = signingKey('RrPxO9g5jdFEU/r/RYD5mnx0JTv65EpicoELS/ob+Ao=')
const signature = (body) => sign(null, body, privateKey).toString('base64') + '\n'
const spki = createPublicKey(privateKey).export({ type: 'spki', format: 'der' })
const rawPublicKey = spki.subarray(spki.length - 32).toString('base64')

test('a promotion reads no pull request CI at all, so a red PR in the range cannot refuse it', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-collect-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const source = join(root, 'release-source')
  mkdirSync(source)
  const version = '0.8.0'
  const tag = 'v0.8.0-nightly.202611121902'
  const sha = 'a'.repeat(40)
  const prefix = `https://github.com/${repo}/releases/download/${tag}/`
  const payloads = [
    `nocx-${version}-darwin-universal.zip`,
    `nocx-${version}-darwin-universal.dmg`,
    `nocx-${version}-linux-amd64.AppImage`,
    ...['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64'].map(
      (platform) => `nocx-helper-${version}-${platform}`,
    ),
  ]
  for (const name of payloads) writeFileSync(join(source, name), `tried bytes: ${name}`)
  const describe = (name) => {
    const body = readFileSync(join(source, name))
    return { name, sha256: createHash('sha256').update(body).digest('hex'), size: body.length }
  }
  const entry = (name, os, arch, format) => ({
    os,
    arch,
    ...(format ? { format } : {}),
    url: prefix + name,
    ...(({ sha256, size }) => ({ sha256, size }))(describe(name)),
  })
  const manifest = {
    version,
    released: '2026-11-12T19:02:00Z',
    notesUrl: `https://github.com/${repo}/releases/tag/${tag}`,
    artifacts: [
      entry(payloads[0], 'darwin', 'universal', 'zip'),
      entry(payloads[2], 'linux', 'amd64', 'AppImage'),
    ],
    helpers: payloads.slice(3).map((name) => {
      const [os, arch] = name.split('-').slice(-2)
      return entry(name, os, arch)
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
    runId: '43292929736',
    runAttempt: '1',
    artifacts: [...payloads, 'manifest.json', 'manifest.json.sig'].map(describe),
  }
  const nightlyBody = Buffer.from(JSON.stringify(nightly))
  writeFileSync(join(source, 'nightly.json'), nightlyBody)
  writeFileSync(join(source, 'nightly.json.sig'), signature(nightlyBody))

  const readme = '**Status:** `v0.8.0`, early release. See [notes](docs/release-notes/v0.8.0.md).'
  const notes = '# v0.8.0\nA release worth trying.'
  const keyring = `package update\n\nvar releaseKeys = []string{\n\t"${rawPublicKey}",\n}\n`

  const asked = []
  const github = {
    // A page that answers a red pull request: the collector must never ask.
    pages: (path) => {
      asked.push(path)
      throw new Error(`the promotion read ${path}`)
    },
    api: (path) => {
      asked.push(path)
      if (path === `repos/${repo}/releases/tags/${tag}`)
        return { tag_name: tag, draft: false, prerelease: true }
      if (path === `repos/${repo}/actions/runs/43292929736`)
        return {
          id: 43292929736,
          run_attempt: 1,
          head_sha: sha,
          head_branch: 'main',
          event: 'schedule',
          path: '.github/workflows/nightly.yml',
          status: 'completed',
          conclusion: 'success',
        }
      if (path === `repos/${repo}/releases/latest`)
        return { tag_name: 'v0.7.2', draft: false, prerelease: false }
      throw new Error(`unexpected GitHub API read: ${path}`)
    },
    gh: (args) => {
      asked.push(args.join(' '))
      const at = args.indexOf('--pattern')
      if (args[0] !== 'release' || args[1] !== 'download' || at < 0)
        throw new Error(`unexpected gh call: ${args.join(' ')}`)
      const name = args[at + 1]
      writeFileSync(join(args[args.indexOf('--dir') + 1], name), readFileSync(join(source, name)))
    },
  }
  const git = (...args) => {
    const [command, ...rest] = args
    if (command === 'fetch') return ''
    if (command === 'merge-base') return ''
    if (command === 'rev-parse') return rest[0] === `${tag}^{commit}` ? sha : 'f'.repeat(40)
    if (command === 'show') {
      const path = rest[0].slice(sha.length + 1)
      if (path === 'internal/update/keyring.go') return keyring
      if (path === 'README.md') return readme
      if (path === 'docs/release-notes/v0.8.0.md') return notes
    }
    throw new Error(`unexpected git call: ${args.join(' ')}`)
  }
  const saved = { ...process.env }
  Object.assign(process.env, {
    GITHUB_ACTOR: 'shady2k',
    GITHUB_TRIGGERING_ACTOR: 'shady2k',
    GITHUB_REPOSITORY_OWNER: 'shady2k',
    GITHUB_REF: 'refs/heads/main',
    NIGHTLY_TAG: tag,
    TRIED_NIGHTLY: 'true',
    GH_REPO: repo,
  })
  t.after(() => {
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key]
    Object.assign(process.env, saved)
  })
  const directory = join(root, 'promotion')
  await collect(directory, github, git)
  const written = JSON.parse(readFileSync(join(directory, 'evidence.json'), 'utf8'))
  assert.equal(written.ci, undefined)
  assert.equal(written.sha, sha)
  assert.equal(readFileSync(join(directory, 'notes.md'), 'utf8'), notes)
  // A red pull request is not consulted: no page, no pull request, no job, no log.
  assert.deepEqual(
    asked.filter((path) => /pulls|actions\/jobs|ci\.yml/.test(path)),
    [],
  )
  assert.deepEqual(
    asked.sort(),
    [
      `repos/${repo}/actions/runs/43292929736`,
      `repos/${repo}/releases/latest`,
      `repos/${repo}/releases/tags/${tag}`,
      `release download ${tag} --repo ${repo} --pattern nightly.json --dir ${join(directory, 'source')}`,
      `release download ${tag} --repo ${repo} --pattern nightly.json.sig --dir ${join(directory, 'source')}`,
      ...nightly.artifacts.map(
        (artifact) =>
          `release download ${tag} --repo ${repo} --pattern ${artifact.name} --dir ${join(directory, 'source')}`,
      ),
    ].sort(),
  )
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
test('promotion has no version file to advance: its stable tag is the next version', () => {
  const source = readFileSync('scripts/release/promotion-github.mjs', 'utf8')
  assert.doesNotMatch(source, /VERSION|bump/)
  const release = readFileSync('.github/workflows/release.yml', 'utf8')
  assert.doesNotMatch(release, /promotion-github\.mjs bump/)
  assert.equal(existsSync('VERSION'), false)
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
