#!/usr/bin/env node
/**
 * Adapter: beads (`br`) -> the normalized backlog the gate's rules read.
 *
 * The ONLY file in this directory that knows a tracker. The five checks beside
 * it are vendored verbatim from the shady2k-skills plugin (provenance in
 * docs/agents/backlog.md) and name no project and no tracker; everything this
 * repository is, is here, in commit-links.mjs and in config.json.
 *
 * It reads the TRACKED JSONL EXPORT rather than the database, deliberately:
 * `br` never runs git, so the export is the only copy git can show at another
 * revision — which is where a baseline comes from, and the only honest source
 * of ages once a bulk edit has rewritten them. The database is also the main
 * checkout's from every worktree, so reading it would answer about the wrong
 * branch.
 *
 * MAPPING, and the places it is not mechanical:
 *
 *   status  open -> open · in_progress -> active · deferred -> deferred ·
 *           closed -> closed · submitted -> submitted · implemented -> implemented.
 *           blocked -> open: older exports stored the computed "blocked"
 *           status, and it still reaches us through --at on an old revision;
 *           blockedBy carries the same fact.
 *           tombstone -> left out: a deleted issue is absent, not finished.
 *           Anything else is a status this file has never seen, and it stops
 *           with exit 2 rather than guessing.
 *   type    epic/task/bug/chore pass through; anything else -> other.
 *   edges   parent-child gives `parent`. ONLY `blocks` gives `blockedBy`.
 *           `discovered-from` (246 edges on 2026-09-18), `related`, `tracks`
 *           and `supersedes` are provenance and are DROPPED: they gate nothing
 *           in beads, and a hand-written script that read one as a dependency
 *           is how three brainstorms were rescued into the work queue.
 *   holder  the assignee, or null. A released leaf has its assignee cleared.
 *
 * LANDED is the main line at the judged revision: every commit reachable from
 * both that revision and the main branch (origin/main, else main) whose header
 * names a task, read by commit-links.mjs's linkedIds — the commit check's own
 * parser, so the gate and the commit check cannot read one message two ways.
 * The intersection, not the revision's whole history: a branch's own commits
 * have not landed, and a leaf its worker still holds is not merged-but-open.
 * A checkout with neither branch is refused rather than given an empty list,
 * which would switch the rule off without saying so.
 *
 * SUBMITTED AND IMPLEMENTED are br statuses, declared in .beads/policy.yaml,
 * which also makes br refuse a transition into either without a comment
 * written in the same transaction. That comment carries the record:
 *
 *   submitted: <revision> -- <local-check evidence>
 *   implemented: <revision> -- <related-check evidence>
 *
 * The LATEST comment of the issue's own status's kind is its record. One
 * without both halves, or none at all, still passes the status through with an
 * empty record, so the gate's *-without-evidence check can report it. On any
 * other status these comments are history and read as nothing: a reopen is a
 * status change, not a comment.
 *
 * WORK RECORDS. Comments starting with `[shady2k-time` carry spans; comments
 * starting with `<!-- shady2k-exec` carry command durations used by the next
 * run's bound. Both families go out RAW and whole, damaged or not, with the
 * tracker's comment id, time and author: their consumers judge them. Dropping
 * either loses recorded work. Other comments are left out.
 *
 * Usage:
 *   node .githooks/backlog-gate/adapter.mjs [--at <git-rev>] [--export <path>]
 *
 *   --at HEAD   the last commit           (the baseline the hook compares against)
 *   --at :0     the staged copy           (what this commit is about to publish)
 *   no --at     the working tree
 */

import { execFileSync, spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

import { linkedIds } from './commit-links.mjs'

const TYPES = new Set(['epic', 'task', 'bug', 'chore'])
const STATUS = {
  open: 'open',
  blocked: 'open',
  in_progress: 'active',
  submitted: 'submitted',
  implemented: 'implemented',
  deferred: 'deferred',
  closed: 'closed',
}
const MARKER = /^(submitted|implemented|accepted):\s*(.*)$/s
const RECORD = /^(\S+)\s+--\s+(\S[\s\S]*)$/
const WORK_RECORD = '[shady2k-time'
const EXEC_RECORD = '<!-- shady2k-exec'

export function workRecords(comments) {
  return (comments || [])
    .filter(
      (c) =>
        typeof c.text === 'string' &&
        (c.text.startsWith(WORK_RECORD) || c.text.startsWith(EXEC_RECORD)),
    )
    .map((c) => ({ id: String(c.id), at: c.created_at, author: c.author, body: c.text }))
}

// The record of the latest `<kind>:` comment, or an empty one where there is
// none or it lacks a half.
export function record(comments, kind) {
  let last = null
  // Oldest first by the tracker's own clock, then its id, whatever order the
  // export happens to write them in: "latest" must not depend on that.
  const ordered = [...(comments || [])].sort(
    (a, b) => String(a.created_at).localeCompare(String(b.created_at)) || (a.id ?? 0) - (b.id ?? 0),
  )
  for (const c of ordered) {
    const m = MARKER.exec((c.text || '').trim())
    if (m && m[1] === kind) last = m[2]
  }
  const r = last === null ? null : RECORD.exec(last.trim())
  return { revision: r ? r[1] : '', evidence: r ? r[2].trim() : '' }
}

function acceptanceBody(description, criteria) {
  if (typeof criteria !== 'string' || !criteria.length) return description
  const separator = description.length ? '\n\n' : ''
  return `${description}${separator}## Acceptance Criteria\n\n${criteria}`
}

export function normalize(rows) {
  const issues = []
  for (const r of rows) {
    if (r.status === 'tombstone') continue
    const status = STATUS[r.status]
    if (!Object.hasOwn(STATUS, r.status)) {
      throw new Error(`${r.id} has the beads status "${r.status}", which this adapter does not map`)
    }
    const delivery = status === 'submitted' ? record(r.comments, 'submitted') : undefined
    const integration = status === 'implemented' ? record(r.comments, 'implemented') : undefined
    let parent = null
    const blockedBy = []
    for (const d of r.dependencies || []) {
      if (d.type === 'parent-child') parent = d.depends_on_id
      else if (d.type === 'blocks') blockedBy.push(d.depends_on_id)
    }
    issues.push({
      id: r.id,
      title: r.title || '',
      type: TYPES.has(r.issue_type) ? r.issue_type : 'other',
      status,
      labels: r.labels || [],
      parent,
      blockedBy,
      body: acceptanceBody(r.description || '', r.acceptance_criteria),
      updatedAt: r.updated_at,
      createdAt: r.created_at,
      holder: r.assignee || null,
      comments: workRecords(r.comments),
      ...(delivery && { delivery }),
      ...(integration && { integration }),
    })
  }
  return issues
}

const HERE = dirname(fileURLToPath(import.meta.url))
const ISSUES_SECTION = '=== .beads/issues.jsonl\n'

// The tracker's state, as the mechanism's own command returns it. `export` is
// tracker-sync's reader: it resolves the store commit a recording names, checks
// it against the digest the recording wrote, and answers with the state files
// that commit holds. Reading the recordings' layout here would be a second owner
// of the store format, with its own answer to "which commit is this revision".
function mechanismExport(repo, extra, what) {
  const mechanism = join(HERE, 'tracker-sync.mjs')
  const recipe = join(HERE, 'br-tracker.mjs')
  if (!existsSync(mechanism) || !existsSync(recipe))
    throw new Error('tracker-sync integration is missing; run make connect')
  const result = spawnSync(
    'node',
    [mechanism, 'export', '--repo', repo, '--recipe', recipe, ...extra],
    { cwd: repo, encoding: 'utf8', maxBuffer: 1 << 28 },
  )
  if (result.error || result.status !== 0)
    throw new Error(
      `tracker-sync could not read ${what}: ${(result.stderr || result.error?.message || `exit ${result.status}`).trim()}`,
    )
  const start = result.stdout.indexOf(ISSUES_SECTION)
  if (start < 0) throw new Error(`tracker-sync returned no .beads/issues.jsonl for ${what}`)
  const from = start + ISSUES_SECTION.length
  const next = result.stdout.indexOf('\n=== ', from)
  return result.stdout.slice(from, next < 0 ? undefined : next)
}

// A revision's recording read from refs/tracker/state, for a clone that has no
// store. The mechanism writes that ref once, when it creates the store, and
// afterwards commits recordings on the store worktree's detached HEAD and
// publishes them; so the ref goes stale and cannot be the reader in a connected
// checkout. A clone that only fetched the ref - CI, through
// scripts/connect-tracker-ci.sh - has no store, and this is the state there.
// The recording names a store commit and is written once and never moved, so
// this is that revision's state and never a later one.
function publishedExport(repo, revision) {
  let recording
  try {
    recording = execFileSync(
      'git',
      ['show', `refs/tracker/state:.tracker-sync/recordings/${revision}.jsonl`],
      { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] },
    )
  } catch {
    // No recording at this revision on the published state, which is the
    // missing-recording case the caller reports; anything else git refuses we
    // also let the caller's error name.
    return null
  }
  const entry = JSON.parse(recording)
  if (typeof entry.store !== 'string' || !/^[0-9a-f]{40,64}$/.test(entry.store))
    throw new Error(`tracker recording for ${revision} has no valid store commit`)
  return execFileSync('git', ['show', `${entry.store}:.beads/issues.jsonl`], {
    cwd: repo,
    encoding: 'utf8',
    maxBuffer: 1 << 28,
  })
}

function trackerExport(at) {
  const repo = execFileSync('git', ['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim()
  let body
  if (at && at !== ':0') {
    // A code revision's state is the one its recording names, from the moment
    // the revision is recorded. The store is the live copy; the published ref is
    // what a clone without a store fetched. A missing recording is an error,
    // never a fallback to a later state, so when neither has one the refusal is
    // the store's own explanation.
    const revision = execFileSync('git', ['rev-parse', '--verify', `${at}^{commit}`], {
      cwd: repo,
      encoding: 'utf8',
    }).trim()
    const what = `the recording of ${revision}`
    let failed
    body = null
    try {
      body = mechanismExport(repo, ['--at', revision], what)
    } catch (e) {
      failed = e
    }
    if (body === null) {
      body = publishedExport(repo, revision)
      if (body === null) throw failed
    }
  } else {
    // :0 has no code revision recording. The staged gate compares the active
    // store; a normal working read refreshes it from the shared ref first.
    body = mechanismExport(repo, at === ':0' ? [] : ['--now'], at || 'the active store')
  }
  return body
    .trim()
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line))
}

export function readExport(at, exportPath = '.beads/issues.jsonl') {
  // A worktree after the store move has no tracker file in its Git tree. Read
  // current state from the store and a historical baseline only from the exact
  // tracker-sync recording. Keep the file path for isolated adapter fixtures.
  if (
    exportPath === '.beads/issues.jsonl' &&
    existsSync(join(process.cwd(), '.githooks/backlog-gate/tracker-sync.mjs'))
  )
    return trackerExport(at)
  const text = at
    ? execFileSync('git', ['show', `${at}:${exportPath}`], {
        encoding: 'utf8',
        maxBuffer: 1 << 28,
      })
    : readFileSync(exportPath, 'utf8')
  return text
    .trim()
    .split('\n')
    .filter(Boolean)
    .map((l) => JSON.parse(l))
}
const MAIN_REFS = ['refs/remotes/origin/main', 'refs/heads/main']

// The main-line commits at `revision` that name a task, newest first.
export function landed(revision, cwd = '.') {
  const git = (...args) =>
    execFileSync('git', ['-C', cwd, ...args], {
      encoding: 'utf8',
      maxBuffer: 1 << 30,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
  const main = MAIN_REFS.find((ref) => {
    try {
      git('rev-parse', '-q', '--verify', `${ref}^{commit}`)
      return true
    } catch {
      return false
    }
  })
  if (!main) throw new Error(`no main line: neither ${MAIN_REFS.join(' nor ')} exists here`)
  const base = git('merge-base', `${revision}^{commit}`, main).trim()
  const out = []
  for (const entry of git('log', '--format=%H%x00%B%x1e', base).split('\x1e')) {
    const [id, message] = entry.replace(/^\n/, '').split('\x00')
    if (!message) continue
    const taskIds = linkedIds(message)
    if (taskIds.length) out.push({ id, taskIds })
  }
  return out
}

function nativeRows(checkout) {
  const options = { cwd: checkout, encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] }
  const flags = ['--no-auto-import', '--no-auto-flush']
  const where = JSON.parse(execFileSync('br', [...flags, 'where', '--json'], options))
  if (typeof where.jsonl_path !== 'string' || !where.jsonl_path) {
    throw new Error('br where did not name its JSONL export; run make connect')
  }
  // The native export carries complete edges and comments. list omits them,
  // while show over every historical issue is prohibitively expensive.
  // Refresh from the database only: never import a stale worktree generation.
  execFileSync('br', [...flags, 'sync', '--flush-only'], options)
  return readExport(null, where.jsonl_path)
}

function normalizedNativeRows(rows) {
  const byId = new Map(rows.map((row) => [row.id, row]))
  return normalize(rows).map((issue) => ({ ...issue, raw: byId.get(issue.id) }))
}

function ancestor(checkout, revision, head) {
  if (!revision || !head) return false
  try {
    execFileSync('git', ['-C', checkout, 'merge-base', '--is-ancestor', revision, head], {
      stdio: 'ignore',
    })
    return true
  } catch {
    return false
  }
}

function assertDependencyGraph(issues) {
  const byId = new Map(issues.map((issue) => [issue.id, issue]))
  for (const issue of issues) {
    for (const dependency of issue.blockedBy || []) {
      if (!byId.has(dependency))
        throw new Error(`${issue.id} has missing prerequisite ${dependency}`)
    }
  }
  const visiting = new Set()
  const visited = new Set()
  const visit = (id) => {
    if (visiting.has(id)) throw new Error(`dependency cycle includes ${id}`)
    if (visited.has(id)) return
    visiting.add(id)
    for (const dependency of byId.get(id)?.blockedBy || []) visit(dependency)
    visiting.delete(id)
    visited.add(id)
  }
  for (const issue of issues) visit(issue.id)
  return byId
}

function stageContext(issues, stageId, checkout) {
  const byId = assertDependencyGraph(issues)
  const stage = byId.get(stageId)
  if (!stage || stage.type !== 'epic' || !stage.parent) {
    throw new Error(`${stageId} is not a stage epic`)
  }
  const feature = byId.get(stage.parent)
  if (!feature || feature.type !== 'epic') throw new Error(`${stageId} has no feature root`)
  let scope = stage
  let eligible = true
  const seen = new Set()
  while (scope) {
    if (seen.has(scope.id)) throw new Error(`parent cycle includes ${scope.id}`)
    seen.add(scope.id)
    if (
      !['open', 'active'].includes(scope.status) ||
      scope.blockedBy.some((id) => byId.get(id).status !== 'closed')
    )
      eligible = false
    if (scope.parent && !byId.has(scope.parent))
      throw new Error(`${scope.id} has missing parent ${scope.parent}`)
    scope = scope.parent ? byId.get(scope.parent) : null
  }
  const head = execFileSync('git', ['-C', checkout, 'rev-parse', 'HEAD'], {
    encoding: 'utf8',
  }).trim()
  return { byId, stage, feature, head, checkout, eligible }
}

function leavesInStage(issues, stageId) {
  const descendants = new Set()
  const parents = new Set()
  const queue = [stageId]
  while (queue.length) {
    const parent = queue.shift()
    for (const issue of issues) {
      if (issue.parent !== parent || descendants.has(issue.id)) continue
      descendants.add(issue.id)
      parents.add(parent)
      queue.push(issue.id)
    }
  }
  return issues.filter(
    (issue) => descendants.has(issue.id) && !parents.has(issue.id) && issue.type !== 'epic',
  )
}
function implementedSatisfied(prerequisite, context) {
  if (
    prerequisite.status !== 'implemented' ||
    !prerequisite.integration?.revision ||
    !prerequisite.integration?.evidence?.trim()
  )
    return false
  let prerequisiteStage = context.byId.get(prerequisite.parent)
  const seen = new Set()
  while (prerequisiteStage && prerequisiteStage.parent !== context.feature.id) {
    if (seen.has(prerequisiteStage.id)) return false
    seen.add(prerequisiteStage.id)
    prerequisiteStage = context.byId.get(prerequisiteStage.parent)
  }
  if (prerequisiteStage?.id === context.stage.id) {
    return ancestor(context.checkout, prerequisite.integration.revision, context.head)
  }
  if (prerequisiteStage?.parent !== context.feature.id) return false
  // The real dependency establishes stage order; generated ids do not.
  const accepted = record(prerequisiteStage.raw?.comments, 'accepted')
  return Boolean(
    accepted.evidence &&
    ancestor(context.checkout, prerequisite.integration.revision, accepted.revision) &&
    ancestor(context.checkout, accepted.revision, context.head),
  )
}

function isReady(issue, context) {
  if (!context.eligible || issue.status !== 'open' || issue.holder) return false
  let parent = context.byId.get(issue.parent)
  const seen = new Set()
  while (parent && parent.id !== context.stage.id) {
    if (seen.has(parent.id)) throw new Error(`parent cycle includes ${parent.id}`)
    seen.add(parent.id)
    if (
      !['open', 'active'].includes(parent.status) ||
      parent.blockedBy.some((id) => context.byId.get(id).status !== 'closed')
    )
      return false
    if (parent.parent && !context.byId.has(parent.parent))
      throw new Error(`${parent.id} has missing parent ${parent.parent}`)
    parent = context.byId.get(parent.parent)
  }
  return (issue.blockedBy || []).every((id) => {
    const prerequisite = context.byId.get(id)
    return prerequisite.status === 'closed' || implementedSatisfied(prerequisite, context)
  })
}

export function contextualReady(rows, stageId, checkout) {
  const issues = normalizedNativeRows(rows)
  const context = stageContext(issues, stageId, checkout)
  return leavesInStage(issues, stageId)
    .filter((issue) => isReady(issue, context))
    .map(({ id }) => id)
}

function contextualClaim(id, rows, stageId, checkout, actor) {
  const issues = normalizedNativeRows(rows)
  const context = stageContext(issues, stageId, checkout)
  const issue = leavesInStage(issues, stageId).find((candidate) => candidate.id === id)
  if (!issue) throw new Error(`${id} is not a leaf under stage ${stageId}`)
  if (!isReady(issue, context)) throw new Error(`${id} is not contextually ready`)
  const force = (issue.blockedBy || []).some((dependency) => {
    const prerequisite = context.byId.get(dependency)
    return prerequisite.status === 'implemented' && implementedSatisfied(prerequisite, context)
  })
  const args = ['--no-auto-import', '--no-auto-flush', 'update', id, '--claim', '--actor', actor]
  if (force) args.push('--force')
  execFileSync('br', args, { cwd: checkout, stdio: 'inherit' })
}

function main() {
  const argv = process.argv.slice(2)
  let at = null
  let exportPath = '.beads/issues.jsonl'
  let operation = null
  let item = null
  let stage = null
  let checkout = null
  let actor = null
  const take = (flag, i) => {
    if (!argv[i + 1] || argv[i + 1].startsWith('--')) throw new Error(`${flag} requires a value`)
    return argv[i + 1]
  }
  try {
    for (let i = 0; i < argv.length; i++) {
      const arg = argv[i]
      if (arg === '--at') at = take(arg, i++)
      else if (arg === '--export') exportPath = take(arg, i++)
      else if (arg === '--ready') {
        if (operation) throw new Error('choose only one operation')
        operation = 'ready'
      } else if (arg === '--claim') {
        if (operation) throw new Error('choose only one operation')
        operation = 'claim'
        item = take(arg, i++)
      } else if (arg === '--stage') stage = take(arg, i++)
      else if (arg === '--checkout') checkout = take(arg, i++)
      else if (arg === '--actor') actor = take(arg, i++)
      else if (arg === '--help' || arg === '-h') {
        console.log(
          'adapter.mjs [--at <git-rev>|:0] [--export <path>] | --ready --stage <id> --checkout <path> | --claim <id> --stage <id> --checkout <path> --actor <agent>',
        )
        return 0
      } else throw new Error(`unknown argument: ${arg}`)
    }
    if (operation) {
      if (at || exportPath !== '.beads/issues.jsonl')
        throw new Error('context operations use the active br tracker, not --at/--export')
      if (!stage || !checkout) throw new Error(`${operation} requires --stage and --checkout`)
      if (operation === 'claim' && !actor) throw new Error('--claim requires --actor')
      if (
        operation === 'claim' &&
        !/^[^:\s]+-[^:\s]+:[^@\s]+@[^:\s]+:[^#\s]+#[^\s]+$/.test(actor)
      ) {
        throw new Error(
          '--claim needs the full agent name: <harness>-<role>:<person>@<machine>:<branch>#<session>',
        )
      }
      const rows = nativeRows(checkout)
      if (operation === 'ready') {
        const ready = contextualReady(rows, stage, checkout)
        process.stdout.write(JSON.stringify({ stage, ready }, null, 2) + '\n')
      } else contextualClaim(item, rows, stage, checkout, actor)
      return 0
    }
    const issues = normalize(readExport(at, exportPath))
    process.stdout.write(
      JSON.stringify(
        {
          generatedAt: new Date().toISOString(),
          source: `beads ${exportPath}${at ? ` @ ${at}` : ' (working tree)'}`,
          issues,
          // The staged copy and the working tree are judged against HEAD.
          landed: landed(at && at !== ':0' ? at : 'HEAD'),
        },
        null,
        2,
      ) + '\n',
    )
    return 0
  } catch (e) {
    console.error(`adapter.mjs: ${e.message}`)
    return 2
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  process.exitCode = main()
