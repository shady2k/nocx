// GitHub I/O is isolated here; policy and byte checks live in promotion.mjs.
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  authorize,
  prepareStable,
  readKeyring,
  signedRunNumber,
  signingKey,
  updateCask,
  verifyCandidate,
  verifyFiles,
  verifySignature,
} from './promotion.mjs'

const fail = (condition, message) => {
  if (!condition) throw new Error(message)
}
export function githubClient(run = execFileSync) {
  // Successful product logs can be large; the final gate record is at their
  // end. Bound the read, but do not use execFileSync's 1 MiB default.
  const gh = (args) =>
    run('gh', args, {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
      maxBuffer: 256 * 1024 * 1024,
    })
  const api = (path) => JSON.parse(gh(['api', path]))
  // Explicit pagination on every list. A missing page/API error throws, never means green.
  const pages = (path, field) => {
    const result = []
    for (let page = 1; ; page++) {
      const response = api(`${path}${path.includes('?') ? '&' : '?'}per_page=100&page=${page}`)
      const rows = field ? response[field] : response
      fail(Array.isArray(rows), `invalid GitHub list: ${path}`)
      result.push(...rows)
      if (rows.length < 100) return result
    }
  }
  return { gh, api, pages }
}

// The candidate's source is read with git, and a step's output is bounded past
// execFileSync's 1 MiB default, which throws ENOBUFS rather than truncating.
const git = (...args) =>
  execFileSync('git', args, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    maxBuffer: 256 * 1024 * 1024,
  }).trim()
const readJSON = (path) => JSON.parse(readFileSync(path, 'utf8'))
const saveJSON = (path, value) => writeFileSync(path, JSON.stringify(value, null, 2) + '\n')

function candidateInput(directory) {
  const saved = readJSON(join(directory, 'evidence.json'))
  return {
    ...saved,
    keys: readKeyring(readFileSync(join(directory, 'keyring.go'), 'utf8')),
    nightlyBody: readFileSync(join(directory, 'source/nightly.json')),
    nightlySignature: readFileSync(join(directory, 'source/nightly.json.sig'), 'utf8'),
    manifestBody: readFileSync(join(directory, 'source/manifest.json')),
    manifestSignature: readFileSync(join(directory, 'source/manifest.json.sig'), 'utf8'),
  }
}

/**
 * Read the candidate: the owner's authorization, the nightly's signed
 * provenance and that it is on main, the candidate's README status and notes,
 * and the exact payload inventory the signature covers. It reads no merged pull
 * request's CI — the owner promoted a nightly he installed and tried, and that
 * trial is the release's authority (ADR-gvnw2nv8). `run` is git, injectable so a
 * test drives the whole read without a network.
 */
export async function collect(directory, github, run = git) {
  const {
    GITHUB_ACTOR: actor,
    GITHUB_TRIGGERING_ACTOR: triggeringActor,
    GITHUB_REPOSITORY_OWNER: owner,
    GITHUB_REF: ref,
    NIGHTLY_TAG: tag,
    TRIED_NIGHTLY: tried,
    GH_REPO: repo,
  } = process.env
  authorize({ actor, triggeringActor, owner, ref, tried: tried === 'true' })
  fail(
    repo === 'shady2k/nocx' && /^(?:nightly-\d{8}-\d+|v\d+\.\d+\.\d+-nightly\.\d{12})$/.test(tag),
    'name a dated nightly from shady2k/nocx',
  )
  mkdirSync(directory)
  const source = join(directory, 'source')
  mkdirSync(source)
  const release = github.api(`repos/${repo}/releases/tags/${tag}`)
  fail(!release.draft && release.prerelease && release.tag_name === tag, 'not a published nightly')
  run(
    'fetch',
    '--no-tags',
    'origin',
    `refs/heads/main:refs/remotes/origin/main`,
    `refs/tags/${tag}:refs/tags/${tag}`,
  )
  const sha = run('rev-parse', `${tag}^{commit}`)
  run('merge-base', '--is-ancestor', sha, 'origin/main')
  const keyringSource = run('show', `${sha}:internal/update/keyring.go`)
  const keys = readKeyring(keyringSource)
  writeFileSync(join(directory, 'keyring.go'), keyringSource)
  for (const name of ['nightly.json', 'nightly.json.sig'])
    github.gh(['release', 'download', tag, '--repo', repo, '--pattern', name, '--dir', source])
  const nightlyBody = readFileSync(join(source, 'nightly.json'))
  verifySignature(nightlyBody, readFileSync(join(source, 'nightly.json.sig'), 'utf8'), keys)
  const nightly = JSON.parse(nightlyBody)
  const runId = signedRunNumber(nightly.runId)
  fail(runId !== undefined, 'invalid nightly workflow run id')
  const workflowRun = github.api(`repos/${repo}/actions/runs/${runId}`)
  // Download by an explicit authenticated inventory, never a mutable alias or build output.
  fail(Array.isArray(nightly.artifacts), 'missing nightly artifact inventory')
  for (const { name } of nightly.artifacts) {
    fail(
      /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name) &&
        !['nightly.json', 'nightly.json.sig'].includes(name),
      'unsafe nightly artifact',
    )
    github.gh(['release', 'download', tag, '--repo', repo, '--pattern', name, '--dir', source])
  }
  const previous = github.api(`repos/${repo}/releases/latest`)
  fail(
    /^v\d+\.\d+\.\d+$/.test(previous.tag_name) && !previous.draft && !previous.prerelease,
    'latest release is not stable',
  )
  run(
    'fetch',
    '--no-tags',
    'origin',
    `refs/tags/${previous.tag_name}:refs/tags/${previous.tag_name}`,
  )
  const previousSHA = run('rev-parse', `${previous.tag_name}^{commit}`)
  // The candidate carries the last stable's bytes forward: a nightly built
  // before the stable it follows would publish a downgrade under a new tag.
  run('merge-base', '--is-ancestor', previousSHA, sha)
  const readme = run('show', `${sha}:README.md`)
  fail(/^\d+\.\d+\.\d+$/.test(nightly.version), 'invalid nightly version')
  const notes = run('show', `${sha}:docs/release-notes/v${nightly.version}.md`)
  const saved = {
    repo,
    tag,
    sha,
    release,
    run: workflowRun,
    readme,
    notes,
    previousVersion: previous.tag_name.slice(1),
  }
  saveJSON(join(directory, 'evidence.json'), saved)
  const candidate = verifyCandidate(candidateInput(directory))
  verifyFiles(source, candidate.inventory)
  writeFileSync(join(directory, 'notes.md'), notes)
  console.log(
    `Verified tried nightly ${tag} at ${sha} for v${candidate.version}: provenance, README, notes and ${candidate.inventory.length} payload digests`,
  )
}

export function verifyUploaded(github, repo, plan, directory) {
  mkdirSync(directory)
  github.gh(['release', 'download', plan.tag, '--repo', repo, '--dir', directory])
  verifyFiles(directory, plan.assets)
  // releases/tags/<tag> answers 404 for a DRAFT, and this runs first on the
  // draft (Release run 40924528670 refused there after uploading it). The
  // authenticated release list includes drafts, so the release is found there,
  // by tag, and exactly once.
  const matches = github.pages(`repos/${repo}/releases`).filter((r) => r.tag_name === plan.tag)
  fail(matches.length === 1, `expected exactly one release ${plan.tag}, found ${matches.length}`)
  const release = matches[0]
  fail(
    release.assets.length === plan.assets.length &&
      release.assets.every((a) => plan.assets.some((p) => p.name === a.name && p.size === a.size)),
    'stable release asset set differs from verified plan',
  )
}

export function publish(directory, github) {
  const plan = readJSON(join(directory, 'plan.json'))
  const repo = 'shady2k/nocx'
  // Refuse any existing tag/release. Never retarget, overwrite or --clobber.
  const refs = github.pages(`repos/${repo}/git/matching-refs/tags/${plan.tag}`)
  fail(
    !refs.some((r) => r.ref === `refs/tags/${plan.tag}`),
    `stable tag ${plan.tag} already exists; inspect the previous attempt`,
  )
  const releases = github.pages(`repos/${repo}/releases`)
  fail(
    !releases.some((r) => r.tag_name === plan.tag),
    `stable release ${plan.tag} already exists; inspect the previous attempt`,
  )
  verifyFiles(join(directory, 'stable'), plan.assets)
  github.gh([
    'api',
    '--method',
    'POST',
    `repos/${repo}/git/refs`,
    '-f',
    `ref=refs/tags/${plan.tag}`,
    '-f',
    `sha=${plan.sha}`,
  ])
  const ref = github.api(`repos/${repo}/git/ref/tags/${plan.tag}`)
  fail(
    ref.object.type === 'commit' && ref.object.sha === plan.sha,
    'stable tag does not name the nightly commit',
  )
  github.gh([
    'release',
    'create',
    plan.tag,
    '--repo',
    repo,
    '--draft',
    '--verify-tag',
    '--title',
    plan.tag,
    '--notes-file',
    join(directory, 'notes.md'),
    ...plan.assets.map((a) => join(directory, 'stable', a.name)),
  ])
  verifyUploaded(github, repo, plan, join(directory, 'uploaded'))
  github.gh([
    'release',
    'edit',
    plan.tag,
    '--repo',
    repo,
    '--draft=false',
    '--prerelease=false',
    '--latest',
  ])
  verifyUploaded(github, repo, plan, join(directory, 'published'))
  console.log(
    `Published ${plan.tag} from ${plan.sha} without rebuilding; all seven payload digests are unchanged`,
  )
}

function cask(directory, path) {
  const plan = readJSON(join(directory, 'plan.json'))
  verifyFiles(join(directory, 'published'), plan.assets)
  writeFileSync(path, updateCask(readFileSync(path, 'utf8'), plan.version, plan.macSHA256))
}

export async function main(args = process.argv.slice(2), github = githubClient()) {
  const [command, location, path] = args
  fail(location, 'usage: promotion-github.mjs collect|prepare|publish|cask <directory> [cask path]')
  const directory = resolve(location)
  if (command === 'collect') return collect(directory, github)
  if (command === 'prepare') {
    const input = candidateInput(directory)
    const candidate = verifyCandidate(input)
    const plan = prepareStable(
      candidate,
      join(directory, 'source'),
      join(directory, 'stable'),
      signingKey(process.env.RELEASE_SIGNING_KEY),
      input.keys,
    )
    saveJSON(join(directory, 'plan.json'), plan)
    return
  }
  if (command === 'publish') return publish(directory, github)
  if (command === 'cask') return cask(directory, path)
  throw new Error(`unknown promotion command: ${command}`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    await main()
  } catch (error) {
    console.error(`promotion refused: ${error.message}`)
    process.exitCode = 1
  }
}
