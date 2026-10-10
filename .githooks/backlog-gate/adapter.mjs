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
 * WORK RECORDS. A comment whose text starts with `[shady2k-time` is a claim or
 * time record printed by the set's run script. Every one goes out RAW and
 * whole, damaged or not, with the tracker's comment id, time and author: the
 * gate judges a record, and one this file dropped or tidied would be time lost
 * with nothing to say so. Other comments are left out.
 *
 * Usage:
 *   node .githooks/backlog-gate/adapter.mjs [--at <git-rev>] [--export <path>]
 *
 *   --at HEAD   the last commit           (the baseline the hook compares against)
 *   --at :0     the staged copy           (what this commit is about to publish)
 *   no --at     the working tree
 */

import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

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

export function workRecords(comments) {
  return (comments || [])
    .filter((c) => typeof c.text === 'string' && c.text.startsWith(WORK_RECORD))
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

export function readExport(at, exportPath = '.beads/issues.jsonl') {
  // `git show :0:<path>` is the staged copy and `git show HEAD:<path>` the last
  // committed one; both take the same spelling, so one flag serves the hook and
  // the ages snapshots alike.
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
