#!/usr/bin/env node
/**
 * Adapter: beads (`br`) → the normalized backlog of model.md.
 *
 * The ONLY file here that knows a tracker. It reads the tracked JSONL export
 * rather than the database, so it is safe to run at any moment and from any
 * worktree, and `--at <git-rev>` reads that revision's export instead — which
 * is how you get honest ages back after a bulk edit has rewritten them.
 *
 * MAPPING, and the two places it is not mechanical:
 *
 *   status  open → open · in_progress → active · deferred → deferred
 *           closed → closed. beads computes "blocked" rather than storing it,
 *           so it never appears here and blockedBy carries the same fact.
 *   type    epic/task/bug/chore pass through; anything else → other.
 *   edges   parent-child gives `parent`. ONLY `blocks` gives `blockedBy`.
 *           `discovered-from` is provenance and is DROPPED: it gates nothing
 *           in beads, and a hand-written script that read it as a dependency
 *           is how three brainstorms were rescued into the work queue on
 *           2026-09-17.
 *
 * Usage:
 *   node adapters/beads.mjs [--at <git-rev>] [--export <path>] > backlog.json
 */

import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';

const TYPES = new Set(['epic', 'task', 'bug', 'chore']);
const STATUS = { open: 'open', in_progress: 'active', deferred: 'deferred', closed: 'closed' };

const argv = process.argv.slice(2);
let at = null;
let exportPath = '.beads/issues.jsonl';
for (let i = 0; i < argv.length; i++) {
  if (argv[i] === '--at') at = argv[++i];
  else if (argv[i] === '--export') exportPath = argv[++i];
  else if (argv[i] === '--help' || argv[i] === '-h') {
    console.log('beads.mjs [--at <git-rev>] [--export <path>]  > normalized.json');
    process.exit(0);
  } else {
    console.error(`unknown argument: ${argv[i]}`);
    process.exit(2);
  }
}

const text = at
  ? execFileSync('git', ['show', `${at}:${exportPath}`], { encoding: 'utf8', maxBuffer: 1 << 28 })
  : readFileSync(exportPath, 'utf8');

const rows = text.trim().split('\n').filter(Boolean).map((l) => JSON.parse(l));

const issues = rows.map((r) => {
  let parent = null;
  const blockedBy = [];
  for (const d of r.dependencies || []) {
    if (d.type === 'parent-child') parent = d.depends_on_id;
    else if (d.type === 'blocks') blockedBy.push(d.depends_on_id);
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
  };
});

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
);
