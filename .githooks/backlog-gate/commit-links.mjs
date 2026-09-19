#!/usr/bin/env node
/**
 * Commit links: this repository's commit messages -> the input of the vendored
 * check-commits.mjs, and the call to it. The check verifies links; this file is
 * what decides what a message links to, so it is the half a stranger has to
 * trust, and it is kept small enough to read.
 *
 * THE CONVENTION (AGENTS.md, "Every commit names its bead"): the bead ids in
 * parentheses at the end of the subject. What is read is every `nocx-…` id
 * inside parentheses in the HEADER PARAGRAPH — the lines up to the first blank
 * one — because a subject that wraps carries its ids onto its second line, and
 * that happened to 5 of 593 commits in the week before this was written. Ids
 * in the body are references, not links, and are not read. Lines starting
 * with `#` are git's own template and are dropped first.
 *
 *   A revert keeps the reverted subject in quotes, ids included, so it links
 *   to the same tasks with no rule of its own.
 *   A MERGE with no id of its own is linked by the commits it brings in —
 *   those between its first parent and itself — and each of those is checked
 *   on its own anyway. A merge that brings in nothing and names nothing is
 *   unlinked, and fails like any other commit; one that brings in only commits
 *   from before the rule is reported with them. Not an exemption: GitHub writes
 *   "Merge pull request #N" and a person cannot add a line to it.
 *
 * WHICH COMMITS: those committed at or after `commitLinksFrom` in config.json,
 * the moment this check was switched on. Older ones were made under no rule;
 * they are listed as such and fail nothing. The committer date is the
 * author's to set, so this is honesty, not enforcement.
 *
 * WHICH TASKS: every issue of the tracker, closed ones included. Locally that
 * is the export `br` writes after every mutation (`br where`), which is the
 * database's own view — a bead created a minute ago resolves. In CI it is the
 * export at the head of the range, so a branch must publish the beads its
 * commits name, which AGENTS.md asks of it anyway.
 *
 * Usage:
 *   commit-links.mjs --message <file>           the pending message (commit-msg)
 *   commit-links.mjs --range <base>..<head>     every commit it introduces (CI)
 *     [--export-at <rev>]                       tasks from the export at <rev>
 *     [--json]
 * Exit 0 linked, 1 missing or invalid links, 2 misuse or an unreadable source.
 */

import { execFileSync, spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { normalize, readExport } from './adapter.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const ID = /\bnocx-[a-z0-9]+(?:\.\d+)*\b/g

const git = (...args) => execFileSync('git', args, { encoding: 'utf8', maxBuffer: 1 << 28 })

// The ids a message links to, by the convention above.
export function linkedIds(message) {
  const lines = message.split('\n').filter((l) => !l.startsWith('#'))
  while (lines.length && !lines[0].trim()) lines.shift()
  const end = lines.findIndex((l) => !l.trim())
  const header = (end < 0 ? lines : lines.slice(0, end)).join('\n')
  const ids = []
  for (const group of header.match(/\([^()]*\)/g) || []) {
    for (const id of group.match(ID) || []) if (!ids.includes(id)) ids.push(id)
  }
  return ids
}

// What a merge with no id of its own links to: the links of the commits it
// brings in (`base..tip`, the merge itself excluded), counting only those made
// under the rule. null when it brings some in and every one predates it — such a merge is
// history arriving, and it is reported with the other old commits.
function incoming(base, tip, from, self) {
  const ids = []
  let underRule = 0
  const list = git('rev-list', '--format=%H%x00%ct', `${base}..${tip}`)
    .split('\n')
    .filter((l) => l && !l.startsWith('commit '))
  let brought = 0
  for (const line of list) {
    const [c, ct] = line.split('\0')
    if (c === self) continue
    brought++
    if (Number(ct) * 1000 < from) continue
    underRule++
    for (const id of linkedIds(git('log', '-1', '--format=%B', c)))
      if (!ids.includes(id)) ids.push(id)
  }
  // A merge that brings in nothing has nothing to borrow a link from.
  return underRule || !brought ? ids : null
}

function tasks(exportAt) {
  let rows
  if (exportAt) rows = readExport(exportAt)
  else {
    let path = '.beads/issues.jsonl'
    const where = spawnSync('br', ['where', '--json'], { encoding: 'utf8' })
    if (where.status === 0) path = JSON.parse(where.stdout).jsonl_path
    rows = readExport(null, path)
  }
  return normalize(rows).map((i) => ({ id: i.id, type: i.type, parent: i.parent }))
}

function main() {
  const argv = process.argv.slice(2)
  const opt = {}
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--json') opt.json = true
    else if (['--message', '--range', '--export-at'].includes(a) && argv[i + 1])
      opt[a.slice(2)] = argv[++i]
    else {
      console.error(`commit-links.mjs: unknown or incomplete argument ${a}`)
      return 2
    }
  }
  if (!!opt.message === !!opt.range) {
    console.error(
      'commit-links.mjs: give exactly one of --message <file> or --range <base>..<head>',
    )
    return 2
  }

  const config = JSON.parse(readFileSync(join(HERE, 'config.json'), 'utf8'))
  const from = Date.parse(config.commitLinksFrom)
  if (!Number.isFinite(from)) {
    console.error('commit-links.mjs: config.json has no readable commitLinksFrom')
    return 2
  }

  const commits = []
  const before = []
  if (opt.message) {
    const message = readFileSync(opt.message, 'utf8')
    const mergeHead = git('rev-parse', '--git-path', 'MERGE_HEAD').trim()
    let taskIds = linkedIds(message)
    let old = false
    if (!taskIds.length && existsSync(mergeHead)) {
      // The merge is not a commit yet, so what it brings in is HEAD..MERGE_HEAD.
      const head = readFileSync(mergeHead, 'utf8').split('\n')[0].trim()
      const ids = incoming('HEAD', head, from, null)
      if (ids === null) old = true
      else taskIds = ids
    }
    if (old) before.push('pending merge')
    else commits.push({ id: 'pending message', taskIds })
  } else {
    if (!/^[^.\s]+\.\.[^.\s]+$/.test(opt.range)) {
      console.error(`commit-links.mjs: --range wants <base>..<head>, got ${opt.range}`)
      return 2
    }
    const list = git('rev-list', '--reverse', '--format=%H %P%x00%ct', opt.range)
      .split('\n')
      .filter((l) => l && !l.startsWith('commit '))
    if (!list.length) {
      console.error(
        `commit-links.mjs: the range ${opt.range} introduces no commit, so there is nothing it could have checked`,
      )
      return 2
    }
    for (const line of list) {
      const [shas, ct] = line.split('\0')
      const [sha, ...parents] = shas.trim().split(' ')
      if (Number(ct) * 1000 < from) {
        before.push(sha)
        continue
      }
      let taskIds = linkedIds(git('log', '-1', '--format=%B', sha))
      if (!taskIds.length && parents.length > 1) {
        const ids = incoming(parents[0], sha, from, sha)
        if (ids === null) {
          before.push(sha)
          continue
        }
        taskIds = ids
      }
      commits.push({ id: sha, taskIds })
    }
    if (!opt['export-at']) opt['export-at'] = opt.range.split('..')[1]
  }

  if (before.length) {
    console.log(
      `${before.length} commit(s) predate commitLinksFrom (${config.commitLinksFrom}), or are merges bringing in only such commits, and are not checked`,
    )
  }
  if (!commits.length) {
    console.log('no commit in the range was made under the rule; nothing to check')
    return 0
  }

  let issues
  try {
    issues = tasks(opt['export-at'])
  } catch (e) {
    console.error(`commit-links.mjs: the tracker could not be read: ${e.message}`)
    return 2
  }
  const args = [join(HERE, 'check-commits.mjs'), '-']
  const run = spawnSync(process.execPath, args, {
    input: JSON.stringify({ issues, commits }),
    encoding: 'utf8',
  })
  const out = run.stdout || ''
  if (opt.json) process.stdout.write(out)
  else {
    let report
    try {
      report = JSON.parse(out)
    } catch {
      process.stdout.write(out)
    }
    if (report?.violations) {
      for (const v of report.violations) {
        const subject =
          v.id === 'pending message'
            ? v.id
            : `${v.id.slice(0, 10)} ${git('log', '-1', '--format=%s', v.id).trim()}`
        console.log(`  ${subject}: ${v.task ? `${v.task} — ` : ''}${v.reason}`)
      }
      console.log(
        `${report.checked} commit(s) checked, ${report.violations.length} link problem(s)`,
      )
      if (report.violations.length) {
        console.log(
          'Name a leaf task, "(nocx-…)" at the end of the subject. No task for it? br create one — a stage or an epic is not a task.',
        )
      }
    } else if (report?.error) console.log(report.error)
  }
  process.stderr.write(run.stderr || '')
  return run.status ?? 2
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  process.exitCode = main()
