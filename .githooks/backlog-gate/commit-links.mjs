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
 *   on its own anyway. A merge that brings in no linked work must name its
 *   own task, even when the imported history predates the rule. Its new
 *   merge commit is not historical. Set the merge message when landing it.
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
 *   commit-links.mjs --range <base>..<head>     every commit it introduces (CI, a PR)
 *   commit-links.mjs --introduced <tip> --by <ref> [--before <sha>]
 *                                               every commit a push of <ref> brings
 *                                               that no other remote ref reaches (CI, a
 *                                               push); none is a pass, said out loud
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

// A merge without its own link borrows links from incoming work under the rule.
// Imported old history is exempt; its newly written merge commit is not.
function incoming(base, tip, from, self) {
  const ids = []
  const list = git('rev-list', '--format=%H%x00%ct', `${base}..${tip}`)
    .split('\n')
    .filter((l) => l && !l.startsWith('commit '))
  for (const line of list) {
    const [c, ct] = line.split('\0')
    if (c === self) continue
    if (Number(ct) * 1000 < from) continue
    for (const id of linkedIds(git('log', '-1', '--format=%B', c)))
      if (!ids.includes(id)) ids.push(id)
  }
  return ids
}

// Reconstruct what a push introduces. CI sees the target ref AFTER publication,
// so exclude that ref and add its advertised old tip explicitly. Every other
// advertised remote ref counts; local tags and other remotes never count.
function introducedBy(tip, ref, before, remote) {
  const advertised = execFileSync('git', ['ls-remote', '--refs', remote], {
    encoding: 'utf8',
    maxBuffer: 1 << 28,
  })
  const held = advertised
    .split('\n')
    .filter(Boolean)
    .flatMap((line) => {
      const fields = line.split('\t')
      if (
        fields.length !== 2 ||
        !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(fields[0]) ||
        !fields[1].startsWith('refs/')
      )
        throw new Error(`git ls-remote returned an unreadable ref for ${remote}`)
      return fields[1] === ref ? [] : [fields[0]]
    })
  if (before) held.push(before)
  const checked = held.length
    ? execFileSync('git', ['cat-file', '--batch-check'], {
        input: `${held.join('\n')}\n`,
        encoding: 'utf8',
        maxBuffer: 1 << 28,
      })
    : ''
  const missing = checked
    .split('\n')
    .filter((line) => line.endsWith(' missing'))
    .map((line) => line.split(' ')[0])
  if (missing.length) {
    // Fetch exact advertised objects, never move a branch, tag or FETCH_HEAD.
    execFileSync(
      'git',
      [
        'fetch',
        '--no-tags',
        '--no-write-fetch-head',
        '--no-recurse-submodules',
        '--no-auto-maintenance',
        remote,
        ...missing,
      ],
      {
        encoding: 'utf8',
        maxBuffer: 1 << 28,
      },
    )
  }
  if (
    before &&
    spawnSync('git', ['rev-parse', '-q', '--verify', `${before}^{commit}`]).status !== 0
  )
    throw new Error(`the old tip ${before} is not a commit the remote can provide`)
  const input = [tip, ...held.map((sha) => `^${sha}`)].join('\n')
  return execFileSync('git', ['rev-list', '--reverse', '--format=%H %P%x00%ct', '--stdin'], {
    input: `${input}\n`,
    encoding: 'utf8',
    maxBuffer: 1 << 28,
  })
}

export function parsePrePush(input) {
  if (typeof input !== 'string' || !input.trim()) throw new Error('pre-push input is empty')
  const lines = input.trimEnd().split('\n')
  return lines.map((line) => {
    const fields = line.trim().split(/\s+/)
    const object = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/
    if (
      fields.length !== 4 ||
      !fields[0] ||
      !object.test(fields[1]) ||
      !object.test(fields[3]) ||
      spawnSync('git', ['check-ref-format', fields[2]]).status !== 0 ||
      (fields[0] === '(delete)' && !/^0+$/.test(fields[1]))
    )
      throw new Error(`malformed pre-push ref line: ${line}`)
    // Git also supplies HEAD, a raw object id, and (delete), not only refs/heads.
    return { localRef: fields[0], localSha: fields[1], remoteRef: fields[2], remoteSha: fields[3] }
  })
}

function tasks(exportAt) {
  let rows
  if (exportAt) rows = readExport(exportAt)
  else {
    // The pending message is checked against br's own export, which holds a
    // task filed a minute ago that no commit carries yet. Without br there is
    // no such export, and the tree's copy would refuse that task as unknown
    // for a reason nobody could see, so a missing br is an error, not a
    // fallback.
    const where = spawnSync('br', ['where', '--json'], { encoding: 'utf8' })
    if (where.error)
      throw new Error(
        `br could not be run (${where.error.code}); run make connect, which names what is missing`,
      )
    if (where.status !== 0)
      throw new Error(
        `br where failed: ${(where.stderr || '').trim() || `exit ${where.status}`}; run make connect`,
      )
    rows = readExport(null, JSON.parse(where.stdout).jsonl_path)
  }
  if (!rows.length)
    throw new Error('the export holds no task; run make connect to import the backlog')
  return normalize(rows).map((i) => ({ id: i.id, type: i.type, parent: i.parent }))
}

function main() {
  const argv = process.argv.slice(2)
  const opt = {}
  if (argv[0] === '--list-introduced') {
    const args = argv.slice(1)
    const listOpt = {}
    for (let i = 0; i < args.length; i++) {
      const key = args[i]
      if (!['--introduced', '--by', '--remote', '--before'].includes(key) || !args[i + 1]) {
        console.error(`commit-links.mjs: unknown or incomplete argument ${key}`)
        return 2
      }
      listOpt[key.slice(2)] = args[++i]
    }
    if (!listOpt.introduced || !listOpt.by) return 2
    try {
      const listed = introducedBy(
        listOpt.introduced,
        listOpt.by,
        listOpt.before,
        listOpt.remote || 'origin',
      )
      for (const line of listed.split('\n').filter((l) => l && !l.startsWith('commit '))) {
        const [shas] = line.split('\0')
        console.log(shas.trim())
      }
      return 0
    } catch (e) {
      console.error(`commit-links.mjs: cannot enumerate introduced commits: ${e.message}`)
      return 2
    }
  }
  if (argv.length === 1 && argv[0] === '--parse-pre-push') {
    try {
      for (const ref of parsePrePush(readFileSync(0, 'utf8')))
        console.log([ref.localRef, ref.localSha, ref.remoteRef, ref.remoteSha].join('\t'))
      return 0
    } catch (e) {
      console.error(`commit-links.mjs: ${e.message}`)
      return 2
    }
  }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--json') opt.json = true
    else if (
      [
        '--message',
        '--range',
        '--introduced',
        '--by',
        '--before',
        '--export-at',
        '--remote',
      ].includes(a) &&
      argv[i + 1]
    )
      opt[a.slice(2)] = argv[++i]
    else {
      console.error(`commit-links.mjs: unknown or incomplete argument ${a}`)
      return 2
    }
  }
  if ([opt.message, opt.range, opt.introduced].filter(Boolean).length !== 1) {
    console.error(
      'commit-links.mjs: give exactly one of --message <file>, --range <base>..<head> or --introduced <tip> --by <ref>',
    )
    return 2
  }
  if (
    !!opt.introduced !== !!opt.by ||
    (opt.before && !opt.introduced) ||
    (opt.remote && !opt.introduced)
  ) {
    console.error(
      'commit-links.mjs: --introduced <tip> takes --by <ref>, optional --before <sha> and --remote <name>',
    )
    return 2
  }
  if (opt.introduced && !opt.remote) opt.remote = 'origin'

  let config
  try {
    config = JSON.parse(readFileSync(join(HERE, 'config.json'), 'utf8'))
  } catch (e) {
    console.error(
      `commit-links.mjs: config.json could not be read (${e.code || e.message}); run make connect, which names what is missing`,
    )
    return 2
  }
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
    if (!taskIds.length && existsSync(mergeHead)) {
      // The merge is not a commit yet, so what it brings in is HEAD..MERGE_HEAD.
      const head = readFileSync(mergeHead, 'utf8').split('\n')[0].trim()
      taskIds = incoming('HEAD', head, from, null)
    }
    commits.push({ id: 'pending message', taskIds })
  } else {
    let listed
    if (opt.range) {
      if (!/^[^.\s]+\.\.[^.\s]+$/.test(opt.range)) {
        console.error(`commit-links.mjs: --range wants <base>..<head>, got ${opt.range}`)
        return 2
      }
      listed = git('rev-list', '--reverse', '--format=%H %P%x00%ct', opt.range)
    } else {
      // The pushed tip must be local; an advertised old tip may need fetching.
      if (opt.before && !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(opt.before)) {
        console.error('commit-links.mjs: --before requires the full advertised old object ID')
        return 2
      }
      for (const rev of [opt.introduced]) {
        if (spawnSync('git', ['rev-parse', '-q', '--verify', `${rev}^{commit}`]).status !== 0) {
          console.error(`commit-links.mjs: ${rev} is not a commit git can read`)
          return 2
        }
      }
      try {
        listed = introducedBy(opt.introduced, opt.by, opt.before, opt.remote)
      } catch (e) {
        console.error(`commit-links.mjs: cannot enumerate refs held by ${opt.remote}: ${e.message}`)
        return 2
      }
    }
    const list = listed.split('\n').filter((l) => l && !l.startsWith('commit '))
    if (!list.length && opt.introduced) {
      console.log(`${opt.by} introduces no commits: nothing to check`)
      return 0
    }
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
        taskIds = incoming(parents[0], sha, from, sha)
      }
      commits.push({ id: sha, taskIds })
    }
    if (!opt['export-at']) opt['export-at'] = opt.introduced || opt.range.split('..')[1]
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
          'Name a leaf task, "(nocx-…)" at the end of the subject. No task for it? File one through /shady2k-skills:to-backlog — a stage or an epic is not a task.',
        )
      }
    } else if (report?.error) console.log(report.error)
  }
  process.stderr.write(run.stderr || '')
  return run.status ?? 2
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  process.exitCode = main()
