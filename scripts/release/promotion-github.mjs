// GitHub I/O is isolated here; policy and byte checks live in promotion.mjs.
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  authorize,
  ciJobNames,
  failedTests,
  isNewerVersion,
  prepareStable,
  readKeyring,
  readQuarantine,
  runBelongsTo,
  signedRunNumber,
  signingKey,
  updateCask,
  verifyCandidate,
  verifyCI,
  verifyFiles,
  verifySignature,
} from './promotion.mjs'

const fail = (condition, message) => {
  if (!condition) throw new Error(message)
}
export function githubClient(run = execFileSync) {
  const gh = (args) => run('gh', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
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

export async function collectCI({
  repo,
  commits,
  github,
  loadWorkflow,
  touchesProduct,
  quarantine = {},
  commitFiles,
  isAncestor,
}) {
  fail(commits.length > 0, 'CI: promoted range is empty')
  const evidence = []
  const seen = new Set()
  for (const commit of commits) {
    const prs = github.pages(`repos/${repo}/commits/${commit}/pulls`)
    const merged = prs.filter(
      (pr) =>
        pr.merged_at &&
        pr.merge_commit_sha === commit &&
        pr.base?.ref === 'main' &&
        pr.base.repo?.full_name === repo,
    )
    // The owner may push to main directly (AGENTS.md). Such a commit has no PR
    // and no CI run of its own; it is recorded here and judged after the loop,
    // against the later PRs whose CI ran over it.
    if (merged.length === 0 && commitFiles) {
      evidence.push({ direct: true, commit, product: touchesProduct(commitFiles(commit)) })
      continue
    }
    fail(merged.length === 1, `CI: commit ${commit} must map to exactly one merged main PR`)
    const pr = merged[0]
    fail(!seen.has(pr.number), `CI: duplicate PR #${pr.number} in promoted range`)
    seen.add(pr.number)
    const files = github.pages(`repos/${repo}/pulls/${pr.number}/files`)
    // GitHub caps this endpoint at 3000 files. Never classify a truncated list as supporting.
    fail(files.length > 0 && files.length < 3000, `CI: incomplete file list for PR #${pr.number}`)
    const runs = github.pages(
      `repos/${repo}/actions/workflows/ci.yml/runs?event=pull_request&head_sha=${pr.head.sha}`,
      'workflow_runs',
    )
    const matching = runs.filter(
      (run) =>
        run.head_sha === pr.head.sha &&
        run.event === 'pull_request' &&
        run.path === '.github/workflows/ci.yml' &&
        runBelongsTo(run, pr, repo),
    )
    // A failed newer attempt must not be hidden by an older successful run.
    matching.sort((a, b) => b.id - a.id)
    fail(matching.length, `CI: no trusted CI run for PR #${pr.number} head ${pr.head.sha}`)
    const run = matching[0]
    const jobs = github.pages(
      `repos/${repo}/actions/runs/${run.id}/attempts/${run.run_attempt}/jobs`,
      'jobs',
    )
    const entry = {
      number: pr.number,
      head: pr.head.sha,
      headRef: pr.head.ref,
      headRepo: pr.head.repo?.full_name,
      product: touchesProduct(files.map((f) => f.filename)),
      run,
      jobs,
      requiredJobs: ciJobNames(loadWorkflow(pr.head.sha)),
    }
    // A run that is not plainly green is read whole: every attempt, and the
    // log of every failed job, so the policy can tell a quarantined flake
    // from a real failure (promotion.mjs verifyCI).
    if (run.conclusion !== 'success' || jobs.some((job) => job.conclusion === 'failure')) {
      entry.quarantine = quarantine
      entry.attempts = []
      for (let attempt = 1; attempt <= run.run_attempt; attempt++) {
        const attemptJobs =
          attempt === run.run_attempt
            ? jobs
            : github.pages(`repos/${repo}/actions/runs/${run.id}/attempts/${attempt}/jobs`, 'jobs')
        entry.attempts.push({
          attempt,
          jobs: attemptJobs.map((job) => ({
            name: job.name,
            status: job.status,
            conclusion: job.conclusion,
            ...(job.conclusion === 'failure'
              ? {
                  failures: failedTests(
                    github.gh([
                      'api',
                      // Job logs carry ANSI colour; gh refuses them without this.
                      '--allow-escape-sequences',
                      `repos/${repo}/actions/jobs/${job.id}/logs`,
                    ]),
                  ),
                }
              : {}),
          })),
        })
      }
    }
    verifyCI([entry])
    evidence.push(entry)
  }
  // A direct product commit is accepted only when a later PR in the range ran
  // the full product suites green, with nothing quarantined, on a head that
  // contains it. A direct supporting commit (tracker rows, documents) owes no
  // product run, as the classifier already says of a PR.
  evidence.forEach((entry, index) => {
    if (!entry.direct || !entry.product) return
    const cover = evidence
      .slice(index + 1)
      .find(
        (later) =>
          !later.direct &&
          later.product &&
          later.run.conclusion === 'success' &&
          !later.attempts &&
          isAncestor(entry.commit, later.head),
      )
    fail(cover, `CI: direct commit ${entry.commit} touches product and no later green PR covers it`)
    entry.coveredBy = cover.number
  })
  return evidence
}

// The tracker export the quarantine is checked against is ~14 MB (2026-10-29),
// far past execFileSync's 1 MiB default, which throws ENOBUFS rather than
// truncating. 256 MiB leaves room for growth and still fails loudly beyond it.
const git = (...args) =>
  execFileSync('git', args, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    maxBuffer: 256 * 1024 * 1024,
  }).trim()
// The quarantine is optional: a commit with no file quarantines nothing.
const showIfPresent = (sha, path) => {
  try {
    return git('show', `${sha}:${path}`)
  } catch {
    return undefined
  }
}
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

async function collect(directory, github) {
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
    repo === 'shady2k/nocx' && /^nightly-\d{8}-\d+$/.test(tag),
    'name a dated nightly from shady2k/nocx',
  )
  mkdirSync(directory)
  const source = join(directory, 'source')
  mkdirSync(source)
  const release = github.api(`repos/${repo}/releases/tags/${tag}`)
  fail(!release.draft && release.prerelease && release.tag_name === tag, 'not a published nightly')
  git(
    'fetch',
    '--no-tags',
    'origin',
    `refs/heads/main:refs/remotes/origin/main`,
    `refs/tags/${tag}:refs/tags/${tag}`,
  )
  const sha = git('rev-parse', `${tag}^{commit}`)
  git('merge-base', '--is-ancestor', sha, 'origin/main')
  const keyringSource = git('show', `${sha}:internal/update/keyring.go`)
  const keys = readKeyring(keyringSource)
  writeFileSync(join(directory, 'keyring.go'), keyringSource)
  for (const name of ['nightly.json', 'nightly.json.sig'])
    github.gh(['release', 'download', tag, '--repo', repo, '--pattern', name, '--dir', source])
  const nightlyBody = readFileSync(join(source, 'nightly.json'))
  verifySignature(nightlyBody, readFileSync(join(source, 'nightly.json.sig'), 'utf8'), keys)
  const nightly = JSON.parse(nightlyBody)
  const runId = signedRunNumber(nightly.runId)
  fail(runId !== undefined, 'invalid nightly workflow run id')
  const run = github.api(`repos/${repo}/actions/runs/${runId}`)
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
  git(
    'fetch',
    '--no-tags',
    'origin',
    `refs/tags/${previous.tag_name}:refs/tags/${previous.tag_name}`,
  )
  const previousSHA = git('rev-parse', `${previous.tag_name}^{commit}`)
  git('merge-base', '--is-ancestor', previousSHA, sha)
  const commits = git('rev-list', '--first-parent', '--reverse', `${previousSHA}..${sha}`)
    .split('\n')
    .filter(Boolean)
  // This is CI's own classifier, shared with nightly (not copied into promotion).
  const { touchesProduct } = await import('../product-change.mjs')
  const quarantine = readQuarantine(
    showIfPresent(sha, 'docs/testing/quarantine.json'),
    git('show', `${sha}:.beads/issues.jsonl`),
  )
  const ci = await collectCI({
    repo,
    commits,
    github,
    quarantine,
    touchesProduct,
    loadWorkflow: (head) => {
      git('fetch', '--no-tags', 'origin', head)
      return git('show', `${head}:.github/workflows/ci.yml`)
    },
    commitFiles: (commit) =>
      git('diff-tree', '--no-commit-id', '--name-only', '-r', `${commit}^`, commit)
        .split('\n')
        .filter(Boolean),
    isAncestor: (ancestor, head) => {
      try {
        git('merge-base', '--is-ancestor', ancestor, head)
        return true
      } catch {
        return false
      }
    },
  })
  const readme = git('show', `${sha}:README.md`)
  fail(/^\d+\.\d+\.\d+$/.test(nightly.version), 'invalid nightly version')
  const notes = git('show', `${sha}:docs/release-notes/v${nightly.version}.md`)
  const saved = {
    repo,
    tag,
    sha,
    release,
    run,
    ci,
    readme,
    notes,
    previousVersion: previous.tag_name.slice(1),
  }
  saveJSON(join(directory, 'evidence.json'), saved)
  const candidate = verifyCandidate(candidateInput(directory))
  verifyFiles(source, candidate.inventory)
  writeFileSync(join(directory, 'notes.md'), notes)
  console.log(
    `Verified tried nightly ${tag} at ${sha} for v${candidate.version}; ${ci.length} PR-head CI runs`,
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

// VERSION changes through a normal PR, never by rewriting the promoted commit.
function bump(directory, github) {
  const plan = readJSON(join(directory, 'plan.json'))
  const repo = 'shady2k/nocx'
  git('fetch', 'origin', 'main')
  git('checkout', '-b', `release/after-${plan.tag}`, 'origin/main')
  const current = readFileSync('VERSION', 'utf8').trim()
  if (isNewerVersion(current, plan.version)) {
    console.log(`VERSION already advanced to ${current}`)
    return
  }
  fail(current === plan.version, 'VERSION changed unexpectedly since the promoted nightly')
  const [major, minor, patch] = plan.version.split('.').map(Number)
  const next = `${major}.${minor}.${patch + 1}`
  writeFileSync('VERSION', next + '\n')
  git('config', 'user.name', 'github-actions[bot]')
  git('config', 'user.email', '41898282+github-actions[bot]@users.noreply.github.com')
  git('add', 'VERSION')
  git(
    'commit',
    '-m',
    `chore(release): advance next nightly version after ${plan.tag} (nocx-1ebrs.35.3)`,
    '-m',
    `Promotion preserved the tried nightly bytes as ${plan.tag}. Advance VERSION\nthrough review so the next nightly does not reuse the stable version.`,
  )
  git('push', 'origin', `HEAD:refs/heads/release/after-${plan.tag}`)
  github.gh([
    'pr',
    'create',
    '--repo',
    repo,
    '--base',
    'main',
    '--head',
    `release/after-${plan.tag}`,
    '--title',
    `Advance next nightly version to ${next}`,
    '--body',
    `Promotion ${plan.tag} published the exact bytes of ${plan.nightly} (${plan.sha}).\n\nAdvance VERSION to ${next}. This PR is not auto-merged. GITHUB_TOKEN cannot trigger CI: the owner must close/reopen this PR (or push with their own token) to start CI, review, then merge before the next nightly.`,
  ])
}

export async function main(args = process.argv.slice(2), github = githubClient()) {
  const [command, location, path] = args
  fail(
    location,
    'usage: promotion-github.mjs collect|prepare|publish|cask|bump <directory> [cask path]',
  )
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
  if (command === 'bump') return bump(directory, github)
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
