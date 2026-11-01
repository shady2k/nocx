#!/usr/bin/env node
// The backlog gate where tracker state is PUBLISHED (nocx-fg79x.3).
//
// A code push or pull request carries no tracker state. The store lives on a
// ref no branch contains, and the sync mechanism publishes it from each
// machine; the gate therefore belongs to that ref's history, not to a code
// change. This compares the published store head with the last store head the
// gate ACCEPTED:
//
//   node .githooks/backlog-gate/publish-gate.mjs --baseline <store-rev> --head <store-rev>
//
// BLOCK-NEW KEEPS ITS MEANING. This is the same check.mjs, with the same
// `block-new` strength the hooks used: a violation present in the head but
// absent from the baseline fails; everything the baseline already carried is
// printed and fails nothing. What moved is where the baseline comes from — the
// previous CHECKED head, not the previous code revision and not the head's
// parent. Publishes are frequent and a scheduled check is not, so comparing
// only the last publish would forget a violation introduced two publishes ago.
// The workflow advances refs/tracker-gate/checked only when this exits 0, so a
// violation keeps every later run red until the store is fixed.
//
// IT DOES NOT REFUSE THE WRITE, and that is a limit of the ground, not of the
// check: GitHub starts no workflow for a push to a ref that is not a branch or
// a tag, and the mechanism runs its own git calls with the project's hooks
// disabled, so there is no project-side point at which a bad publish can be
// stopped before it happens. The upstream note beside this change asks the
// mechanism for one; this job makes each publish accountable instead.
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))

/** The tracker's state at a published store revision. */
export function storeState(repo, revision) {
  const shown = spawnSync('git', ['-C', repo, 'show', `${revision}:.beads/issues.jsonl`], {
    encoding: 'utf8',
    maxBuffer: 1 << 28,
  })
  if (shown.error) throw new Error(`could not run git: ${shown.error.message}`)
  if (shown.status !== 0)
    throw new Error(
      `${revision} holds no .beads/issues.jsonl (${(shown.stderr || '').trim() || `git show exited ${shown.status}`}); name a published store commit`,
    )
  return shown.stdout
}

/** The normalized backlog model the gate reads, for one store revision.
 *
 * The adapter is the one owner of the tracker's fields, as it is for the code
 * hooks: the store's export is written to a file and handed to it with
 * `--export`, so it reads that revision's state rather than the working tree.
 */
export function modelAt(repo, revision, dir, name = 'state') {
  const exportFile = join(dir, `${name}.issues.jsonl`)
  writeFileSync(exportFile, storeState(repo, revision))
  const adapted = spawnSync(process.execPath, [join(HERE, 'adapter.mjs'), '--export', exportFile], {
    cwd: repo,
    encoding: 'utf8',
    maxBuffer: 1 << 28,
  })
  if (adapted.error) throw new Error(`could not run the adapter: ${adapted.error.message}`)
  if (adapted.status !== 0)
    throw new Error(
      `the adapter could not read ${revision}: ${(adapted.stderr || '').trim() || `exit ${adapted.status}`}`,
    )
  return adapted.stdout
}

/** Run the gate for one published delta. Returns check.mjs's exit status. */
export function gate({ repo = process.cwd(), baseline, head, config = join(HERE, 'config.json') }) {
  if (!baseline) throw new Error('--baseline needs the store revision the gate last accepted')
  if (!head) throw new Error('--head needs the published store revision')
  const dir = mkdtempSync(join(tmpdir(), 'tracker-gate-'))
  try {
    const baselineFile = join(dir, 'baseline.json')
    writeFileSync(baselineFile, modelAt(repo, baseline, dir, 'baseline'))
    const headFile = join(dir, 'head.json')
    writeFileSync(headFile, modelAt(repo, head, dir, 'head'))
    const checked = spawnSync(
      process.execPath,
      [join(HERE, 'check.mjs'), '--config', config, '--baseline', baselineFile, headFile],
      { cwd: repo, stdio: 'inherit' },
    )
    if (checked.error) throw new Error(`could not run the gate: ${checked.error.message}`)
    return checked.status ?? 2
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

function usage() {
  return [
    'Usage: node .githooks/backlog-gate/publish-gate.mjs --baseline <store-rev> --head <store-rev>',
    '       [--config <config.json>] [--repo <checkout>]',
    '',
    '--baseline is the published store commit the gate last accepted; --head is the',
    'published store commit being checked. Both are refs/tracker/state commits.',
  ].join('\n')
}

function main() {
  const argv = process.argv.slice(2)
  const opt = {}
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i]
    if (
      !['--baseline', '--head', '--config', '--repo'].includes(flag) ||
      argv[i + 1] === undefined
    ) {
      process.stderr.write(`publish-gate: unknown or incomplete argument ${flag}\n${usage()}\n`)
      return 2
    }
    if (argv[i + 1].startsWith('--')) {
      process.stderr.write(`publish-gate: ${flag} needs a value\n${usage()}\n`)
      return 2
    }
    opt[flag.slice(2)] = argv[++i]
  }
  if (!opt.baseline || !opt.head) {
    process.stderr.write(`publish-gate: both --baseline and --head are required\n${usage()}\n`)
    return 2
  }
  const repo = resolve(opt.repo || process.cwd())
  const count = spawnSync(
    'git',
    ['-C', repo, 'rev-list', '--count', `${opt.baseline}..${opt.head}`],
    {
      encoding: 'utf8',
    },
  )
  const delta = count.status === 0 ? ` (${count.stdout.trim()} published commit(s) since)` : ''
  process.stdout.write(
    `tracker publish gate: ${opt.baseline.slice(0, 12)} -> ${opt.head.slice(0, 12)}${delta}\n`,
  )
  try {
    return gate({
      repo,
      baseline: opt.baseline,
      head: opt.head,
      ...(opt.config ? { config: resolve(opt.config) } : {}),
    })
  } catch (error) {
    process.stderr.write(`publish-gate: ${error instanceof Error ? error.message : error}\n`)
    return 2
  }
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url)
  process.exitCode = main()
