#!/usr/bin/env node
/**
 * Adapter: beads (`br`) -> the normalized backlog the gate's rules read.
 *
 * The ONLY file in this directory that knows a tracker. `check.mjs` beside it
 * is vendored verbatim from the shady2k-skills plugin and names no project and
 * no tracker; everything this repository is, is here and in config.json.
 *
 * It reads the TRACKED JSONL EXPORT rather than the database, deliberately:
 * `br` never runs git, so the export is the only copy git can show at another
 * revision — which is where a baseline comes from, and the only honest source
 * of ages once a bulk edit has rewritten them. The database is also the main
 * checkout's from every worktree, so reading it would answer about the wrong
 * branch.
 *
 * MAPPING, and the two places it is not mechanical:
 *
 *   status  open -> open - in_progress -> active - deferred -> deferred
 *           closed -> closed. beads computes "blocked" rather than storing it,
 *           so it never appears here and blockedBy carries the same fact.
 *   type    epic/task/bug/chore pass through; anything else -> other.
 *   edges   parent-child gives `parent`. ONLY `blocks` gives `blockedBy`.
 *           `discovered-from` (246 edges on 2026-09-18), `related`, `tracks`
 *           and `supersedes` are provenance and are DROPPED: they gate nothing
 *           in beads, and a hand-written script that read one as a dependency
 *           is how three brainstorms were rescued into the work queue.
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

const TYPES = new Set(['epic', 'task', 'bug', 'chore'])
const STATUS = {
  open: 'open',
  in_progress: 'active',
  deferred: 'deferred',
  closed: 'closed',
}

const argv = process.argv.slice(2)
let at = null
let exportPath = '.beads/issues.jsonl'
for (let i = 0; i < argv.length; i++) {
  if (argv[i] === '--at') at = argv[++i]
  else if (argv[i] === '--export') exportPath = argv[++i]
  else if (argv[i] === '--help' || argv[i] === '-h') {
    console.log('adapter.mjs [--at <git-rev>|:0] [--export <path>]  > normalized.json')
    process.exit(0)
  } else {
    console.error(`unknown argument: ${argv[i]}`)
    process.exit(2)
  }
}

// `git show :0:<path>` is the staged copy and `git show HEAD:<path>` the last
// committed one; both take the same spelling, so one flag serves the hook and
// the ages-from snapshot alike.
const text = at
  ? execFileSync('git', ['show', `${at}:${exportPath}`], {
      encoding: 'utf8',
      maxBuffer: 1 << 28,
    })
  : readFileSync(exportPath, 'utf8')

const rows = text
  .trim()
  .split('\n')
  .filter(Boolean)
  .map((l) => JSON.parse(l))

const issues = rows.map((r) => {
  let parent = null
  const blockedBy = []
  for (const d of r.dependencies || []) {
    if (d.type === 'parent-child') parent = d.depends_on_id
    else if (d.type === 'blocks') blockedBy.push(d.depends_on_id)
  }
  return {
    id: r.id,
    title: r.title || '',
    type: TYPES.has(r.issue_type) ? r.issue_type : 'other',
    status: STATUS[r.status] || 'other',
    labels: r.labels || [],
    parent,
    blockedBy,
    body: r.description || '',
    updatedAt: r.updated_at,
  }
})

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
