#!/usr/bin/env node
/**
 * Import a pre-store-move branch's branch-only beads into the external store
 * (nocx-fg79x.1).
 *
 * A branch cut before the tracker moved to refs/tracker/state (PR #632) still
 * carries `.beads/issues.jsonl`. When such a branch merges, the store is
 * authoritative: its rows, fields, deletions and tombstones are never replaced
 * by the branch's older snapshot. What the branch adds is branch-only — issue
 * rows the store has never seen, and comments on existing issues — and this
 * script writes exactly those rows as a JSONL for `br`'s own import.
 *
 * The output is NOT the branch export and NOT a merge:
 *   - every issue id in the store is left exactly as the store holds it; a
 *     branch edit or deletion of that issue, or an edit to one of its
 *     comments, is reported and not imported;
 *   - a branch-only issue is imported whole, preserving id, fields, deps,
 *     labels, status and comment order;
 *   - a branch-only comment on an existing issue is appended to the store's
 *     own row, preserving text, author, timestamp, issue and order. The row's
 *     `updated_at` is moved forward by the smallest amount that makes `br`
 *     apply it (a row at the store's own timestamp is skipped as up to date);
 *   - a comment id that is already used anywhere in the store is remapped on
 *     the imported comment only. Existing comments keep their ids.
 *
 * The import itself is br's, run against the store working copy:
 *
 *   BEADS_JSONL=<plan.jsonl> br sync --import-only --allow-external-jsonl --no-auto-flush
 *   br sync --flush-only
 *
 * Never `br sync --merge`: it reads rows the store has and the JSONL lacks as
 * deletions and writes tombstones. Never `--rebuild` for the same reason.
 *
 * Usage:
 *   node scripts/import-branch-beads.mjs --branch <git-ref> --out <plan.jsonl> [--store <issues.jsonl>] [--json]
 *   node scripts/import-branch-beads.mjs --file <export.jsonl>  --out <plan.jsonl> [--store <issues.jsonl>] [--json]
 *
 * Run from the checkout that has the branch. `--store` defaults to the JSONL
 * path `br where --json` names (the machine's store). The plan is written to
 * --out; the dry-run summary goes to stdout (JSON with --json). Exit 0 when a
 * plan was written (even an empty one), 2 on a usage or input error, 1 when
 * the branch or store cannot be read. This script never mutates the store.
 */

import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'

const USAGE = `Usage:
  node scripts/import-branch-beads.mjs --branch <git-ref> --out <plan.jsonl> [--store <issues.jsonl>] [--json]
  node scripts/import-branch-beads.mjs --file <export.jsonl>  --out <plan.jsonl> [--store <issues.jsonl>] [--json]`

class InputError extends Error {}

/** Read a JSONL file into records, with line-numbered errors and no duplicate ids. */
export function readJsonl(path, label) {
  const text = readFileSync(path, 'utf8')
  const records = []
  const seen = new Map()
  const lines = text.split('\n')
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index].trim()
    if (line === '') continue
    let record
    try {
      record = JSON.parse(line)
    } catch (error) {
      throw new InputError(`${label}:${index + 1}: not JSON: ${error.message}`)
    }
    if (record === null || typeof record !== 'object' || Array.isArray(record)) {
      throw new InputError(`${label}:${index + 1}: not an issue object`)
    }
    if (typeof record.id !== 'string' || record.id === '') {
      throw new InputError(`${label}:${index + 1}: issue has no string id`)
    }
    if (seen.has(record.id)) {
      throw new InputError(
        `${label}:${index + 1}: duplicate issue id ${record.id} (first at line ${seen.get(record.id)})`,
      )
    }
    seen.set(record.id, index + 1)
    records.push(validateComments(record, label, index + 1))
  }
  if (records.length === 0) throw new InputError(`${label}: no issue records`)
  return records
}

function validateComments(record, label, line) {
  if (record.comments === undefined) return record
  if (!Array.isArray(record.comments)) {
    throw new InputError(`${label}:${line}: ${record.id}: comments is not an array`)
  }
  for (const comment of record.comments) {
    if (comment === null || typeof comment !== 'object') {
      throw new InputError(`${label}:${line}: ${record.id}: comment is not an object`)
    }
    if (!Number.isInteger(comment.id)) {
      throw new InputError(`${label}:${line}: ${record.id}: comment id is not an integer`)
    }
    if (comment.issue_id !== record.id) {
      throw new InputError(
        `${label}:${line}: ${record.id}: comment ${comment.id} names issue ${comment.issue_id}`,
      )
    }
    for (const field of ['author', 'text', 'created_at']) {
      if (typeof comment[field] !== 'string') {
        throw new InputError(
          `${label}:${line}: ${record.id}: comment ${comment.id} has no string ${field}`,
        )
      }
    }
  }
  return record
}

/** Read a branch's `.beads/issues.jsonl` at its head, without checking it out. */
export function readBranchFile(ref) {
  const shown = spawnSync('git', ['show', `${ref}:.beads/issues.jsonl`], {
    encoding: 'utf8',
    maxBuffer: 512 * 1024 * 1024,
  })
  if (shown.error) throw new Error(`git show ${ref}: ${shown.error.message}`)
  if (shown.status !== 0) {
    throw new InputError(
      `branch ${ref} has no .beads/issues.jsonl at its head (already moved?) — ${shown.stderr.trim()}`,
    )
  }
  const dir = mkdtempSync(join(tmpdir(), 'nocx-branch-export-'))
  const path = join(dir, 'issues.jsonl')
  writeFileSync(path, shown.stdout)
  return { path, cleanup: () => rmSync(dir, { recursive: true, force: true }) }
}

/** The store export `br where --json` names, or a refusal. */
export function storeFromBr() {
  const where = spawnSync('br', ['where', '--json'], { encoding: 'utf8' })
  if (where.error) throw new Error(`cannot run br to find the store: ${where.error.message}`)
  if (where.status !== 0) throw new Error(`br where failed: ${where.stderr.trim()}`)
  let payload
  try {
    payload = JSON.parse(where.stdout)
  } catch (error) {
    throw new Error(`br where --json did not answer JSON: ${error.message}`)
  }
  if (typeof payload.jsonl_path !== 'string' || payload.jsonl_path === '') {
    throw new Error('br where --json named no jsonl_path')
  }
  return payload.jsonl_path
}

function commentIdentity(comment) {
  // Timestamps are compared as instants: branches and the store spell the same
  // second as +00:00 and Z, and that is one comment, not a new one.
  return [
    comment.issue_id,
    comment.author,
    parsedTime(comment.created_at, 'comment created_at'),
    comment.text,
  ].join('\u0000')
}

function parsedTime(text, what) {
  const ms = Date.parse(text)
  if (!Number.isFinite(ms)) throw new InputError(`${what}: not an RFC3339 timestamp: ${text}`)
  return ms
}

function rfc3339(ms) {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

function changedFields(storeRecord, branchRecord) {
  const ignored = new Set(['comments', 'updated_at'])
  const keys = new Set([...Object.keys(storeRecord), ...Object.keys(branchRecord)])
  const changed = []
  for (const key of keys) {
    if (ignored.has(key)) continue
    if (JSON.stringify(storeRecord[key]) !== JSON.stringify(branchRecord[key])) changed.push(key)
  }
  return changed.sort()
}

/**
 * Classify a branch export against the store and return the rows to import.
 * Pure: no filesystem, no br, no network.
 */
export function planImport({ branchRecords, storeRecords, branchLabel, storeLabel }) {
  const storeById = new Map(storeRecords.map((record) => [record.id, record]))
  const branchIds = new Set(branchRecords.map((record) => record.id))
  const storeCommentIds = new Set()
  const storeCommentIdentities = new Set()
  const storeCommentKeys = new Set()
  let commentIdHigh = 0
  for (const record of storeRecords) {
    for (const comment of record.comments ?? []) {
      storeCommentIds.add(comment.id)
      storeCommentIdentities.add(commentIdentity(comment))
      storeCommentKeys.add(`${comment.issue_id}\u0000${comment.id}`)
      commentIdHigh = Math.max(commentIdHigh, comment.id)
    }
  }
  for (const record of branchRecords) {
    for (const comment of record.comments ?? []) commentIdHigh = Math.max(commentIdHigh, comment.id)
  }
  let nextCommentId = commentIdHigh + 1
  const usedCommentIds = new Set(storeCommentIds)
  const remappedCommentIds = []
  const usedCommentId = (comment) => {
    let id = comment.id
    if (usedCommentIds.has(id)) {
      while (usedCommentIds.has(nextCommentId)) nextCommentId++
      id = nextCommentId++
      remappedCommentIds.push({ issueId: comment.issue_id, from: comment.id, to: id })
    }
    usedCommentIds.add(id)
    return { ...comment, id }
  }

  const rows = []
  const branchOnlyIssues = []
  const commentsOnExistingIssues = []
  const ignoredBranchEdits = []
  const ignoredCommentEdits = []
  const branchOnlyTombstonesSkipped = []
  const storeTombstonesPresent = []
  let importedComments = 0
  let sharedIssues = 0

  for (const branchRecord of branchRecords) {
    const storeRecord = storeById.get(branchRecord.id)
    if (storeRecord === undefined) {
      if (branchRecord.status === 'tombstone') {
        branchOnlyTombstonesSkipped.push(branchRecord.id)
        continue
      }
      const comments = (branchRecord.comments ?? []).map(usedCommentId)
      rows.push({
        ...structuredClone(branchRecord),
        ...(branchRecord.comments ? { comments } : {}),
      })
      branchOnlyIssues.push(branchRecord.id)
      importedComments += comments.length
      continue
    }
    sharedIssues++
    if (storeRecord.status === 'tombstone') {
      storeTombstonesPresent.push(branchRecord.id)
      continue
    }
    const changed = changedFields(storeRecord, branchRecord)
    if (changed.length > 0) ignoredBranchEdits.push({ id: branchRecord.id, fields: changed })
    const fresh = []
    for (const comment of branchRecord.comments ?? []) {
      if (storeCommentIdentities.has(commentIdentity(comment))) continue
      // Same issue AND same comment id, different content: the branch edited a
      // comment the store already holds. The store's text wins; the edit is
      // reported, never imported (the same rule as issue fields).
      if (storeCommentKeys.has(`${branchRecord.id}\u0000${comment.id}`)) {
        ignoredCommentEdits.push({ issueId: branchRecord.id, id: comment.id })
        continue
      }
      fresh.push(comment)
    }
    if (fresh.length === 0) continue
    const appended = fresh.map(usedCommentId)
    const row = structuredClone(storeRecord)
    const existing = row.comments ?? []
    row.comments = [...existing, ...appended]
    const storeTime = parsedTime(storeRecord.updated_at, `${storeRecord.id}: store updated_at`)
    const newestComment = Math.max(
      ...fresh.map((comment) =>
        parsedTime(comment.created_at, `${comment.issue_id}: comment created_at`),
      ),
    )
    row.updated_at = rfc3339(Math.max(storeTime + 1000, newestComment))
    rows.push(row)
    commentsOnExistingIssues.push({ id: branchRecord.id, comments: fresh.length })
    importedComments += fresh.length
  }

  const summary = {
    branch: {
      source: branchLabel,
      issues: branchRecords.length,
      comments: branchRecords.reduce((n, record) => n + (record.comments?.length ?? 0), 0),
    },
    store: {
      source: storeLabel,
      issues: storeRecords.length,
      comments: storeRecords.reduce((n, record) => n + (record.comments?.length ?? 0), 0),
    },
    plan: {
      issues: rows.length,
      comments: importedComments,
      branchOnlyIssues,
      commentsOnExistingIssues,
      remappedCommentIds,
      ignoredBranchEdits,
      ignoredCommentEdits,
      branchOnlyTombstonesSkipped,
      storeTombstonesPresent,
      sharedIssues,
      storeOnlyIssues: storeRecords.filter((record) => !branchIds.has(record.id)).length,
    },
    notes: [
      'The store is authoritative: branch edits and deletions of existing issues and comments are reported, never imported.',
      'A branch-only comment on an existing issue is appended to the store row; its text, author, timestamp, issue and order are preserved.',
      'Only colliding imported comment ids are remapped; existing comments keep their ids.',
      "The import is br's: --import-only (then --flush-only). Never --merge or --rebuild.",
    ],
  }
  return { rows, summary }
}

function formatList(values) {
  return values.length === 0 ? 'none' : values.join(', ')
}

function printSummary(summary, outPath) {
  const plan = summary.plan
  const lines = [
    `branch: ${summary.branch.source} (${summary.branch.issues} issues, ${summary.branch.comments} comments)`,
    `store:  ${summary.store.source} (${summary.store.issues} issues, ${summary.store.comments} comments)`,
    '',
    `would import ${plan.issues} issue row(s) and ${plan.comments} comment(s) into ${outPath}`,
    `  branch-only issues (${plan.branchOnlyIssues.length}): ${formatList(plan.branchOnlyIssues)}`,
    `  new comments on existing issues (${plan.commentsOnExistingIssues.length}): ` +
      formatList(plan.commentsOnExistingIssues.map((entry) => `${entry.id} (${entry.comments})`)),
    `  remapped comment ids (${plan.remappedCommentIds.length}): ` +
      formatList(
        plan.remappedCommentIds.map((entry) => `${entry.from} -> ${entry.to} on ${entry.issueId}`),
      ),
    `  ignored comment edits (${plan.ignoredCommentEdits.length}): ` +
      formatList(plan.ignoredCommentEdits.map((entry) => `${entry.id} on ${entry.issueId}`)),
    `  ignored branch edits (${plan.ignoredBranchEdits.length}): ` +
      formatList(
        plan.ignoredBranchEdits
          .slice(0, 20)
          .map((entry) => `${entry.id}[${entry.fields.join(',')}]`),
      ) +
      (plan.ignoredBranchEdits.length > 20
        ? ` … and ${plan.ignoredBranchEdits.length - 20} more`
        : ''),
    `  branch-only tombstones skipped (${plan.branchOnlyTombstonesSkipped.length}): ` +
      formatList(plan.branchOnlyTombstonesSkipped),
    `  store tombstones matching a branch id (${plan.storeTombstonesPresent.length}): ` +
      formatList(plan.storeTombstonesPresent),
    `  shared issues already in the store (${plan.sharedIssues}); store-only issues kept (${plan.storeOnlyIssues})`,
    '',
    'notes:',
    ...summary.notes.map((note) => `  - ${note}`),
    '',
    'next (against the store working copy):',
    `  BEADS_JSONL=${outPath} br sync --import-only --allow-external-jsonl --no-auto-flush`,
    '  br sync --flush-only',
  ]
  process.stdout.write(lines.join('\n') + '\n')
}

export function main(argv) {
  const args = { json: false }
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index]
    switch (arg) {
      case '--branch':
      case '--file':
      case '--store':
      case '--out':
        if (index + 1 >= argv.length) throw new InputError(`${arg} needs a value`)
        args[arg.slice(2)] = argv[++index]
        break
      case '--json':
        args.json = true
        break
      case '--help':
      case '-h':
        process.stdout.write(USAGE + '\n')
        return 0
      default:
        throw new InputError(`unknown argument ${arg}`)
    }
  }
  if ((args.branch === undefined) === (args.file === undefined)) {
    throw new InputError('give exactly one of --branch or --file')
  }
  if (args.out === undefined) throw new InputError('--out is required')

  let cleanup = () => {}
  let branchPath
  let branchLabel
  if (args.file !== undefined) {
    branchPath = args.file
    branchLabel = args.file
  } else {
    const branch = readBranchFile(args.branch)
    branchPath = branch.path
    branchLabel = args.branch
    cleanup = branch.cleanup
  }
  try {
    const storePath = args.store ?? storeFromBr()
    const branchRecords = readJsonl(branchPath, branchLabel)
    const storeRecords = readJsonl(storePath, storePath)
    const { rows, summary } = planImport({
      branchRecords,
      storeRecords,
      branchLabel,
      storeLabel: storePath,
    })
    writeFileSync(
      args.out,
      rows.map((row) => JSON.stringify(row)).join('\n') + (rows.length ? '\n' : ''),
    )
    if (args.json) process.stdout.write(JSON.stringify(summary, null, 2) + '\n')
    else printSummary(summary, args.out)
    return 0
  } finally {
    cleanup()
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    process.exitCode = main(process.argv.slice(2))
  } catch (error) {
    const code = error instanceof InputError ? 2 : 1
    process.stderr.write(`import-branch-beads: ${error.message}\n`)
    if (code === 2) process.stderr.write(USAGE + '\n')
    process.exitCode = code
  }
}
