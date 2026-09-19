#!/usr/bin/env node
/**
 * Adapter: beads (`br`) -> the normalized backlog the gate's rules read.
 *
 * The ONLY file in this directory that knows a tracker. The three checks beside
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
 *   status  open -> open · deferred -> deferred · closed -> closed.
 *           in_progress -> active, UNLESS the latest execution marker among
 *           its comments says otherwise (below).
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
 * SUBMITTED AND IMPLEMENTED. Beads has neither status, and AGENTS.md forbids a
 * label that restates a field, so they are comments with a fixed first word,
 * on an issue that stays in_progress (which keeps it out of `br ready`):
 *
 *   submitted: <revision> -- <local-check evidence>
 *   implemented: <revision> -- <related-check evidence>
 *   reopened: <why>
 *
 * The LATEST of these on an in_progress issue decides. `reopened` cancels an
 * earlier marker, so the issue is active again. On any other status the
 * markers are history and read as nothing.
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
  deferred: 'deferred',
  closed: 'closed',
}
const MARKER = /^(submitted|implemented|reopened):\s*(.*)$/s
const RECORD = /^(\S+)\s+--\s+(\S[\s\S]*)$/

// The latest execution marker's status and its record, or null for none.
export function execution(comments) {
  let last = null
  // Oldest first by the tracker's own clock, then its id, whatever order the
  // export happens to write them in: "latest" must not depend on that.
  const ordered = [...(comments || [])].sort(
    (a, b) => String(a.created_at).localeCompare(String(b.created_at)) || (a.id ?? 0) - (b.id ?? 0),
  )
  for (const c of ordered) {
    const m = MARKER.exec((c.text || '').trim())
    if (m) last = m
  }
  if (!last || last[1] === 'reopened') return null
  const r = RECORD.exec(last[2].trim())
  // A marker without both halves still decides the status: the gate's own
  // *-without-evidence check is what reports it, and it can only do that if
  // the status reaches it.
  return {
    status: last[1],
    record: { revision: r ? r[1] : '', evidence: r ? r[2].trim() : '' },
  }
}

export function normalize(rows) {
  const issues = []
  for (const r of rows) {
    if (r.status === 'tombstone') continue
    let status = STATUS[r.status]
    if (!status) {
      throw new Error(`${r.id} has the beads status "${r.status}", which this adapter does not map`)
    }
    let delivery
    let integration
    if (r.status === 'in_progress') {
      const ex = execution(r.comments)
      if (ex) {
        status = ex.status
        if (ex.status === 'submitted') delivery = ex.record
        else integration = ex.record
      }
    }
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
      body: r.description || '',
      updatedAt: r.updated_at,
      createdAt: r.created_at,
      holder: r.assignee || null,
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

function main() {
  const argv = process.argv.slice(2)
  let at = null
  let exportPath = '.beads/issues.jsonl'
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--at') at = argv[++i]
    else if (argv[i] === '--export') exportPath = argv[++i]
    else if (argv[i] === '--help' || argv[i] === '-h') {
      console.log('adapter.mjs [--at <git-rev>|:0] [--export <path>]  > normalized.json')
      return 0
    } else {
      console.error(`unknown argument: ${argv[i]}`)
      return 2
    }
  }
  let issues
  try {
    issues = normalize(readExport(at, exportPath))
  } catch (e) {
    console.error(`adapter.mjs: ${e.message}`)
    return 2
  }
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
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  process.exitCode = main()
