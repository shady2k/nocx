#!/usr/bin/env node
/**
 * Watch the disk one e2e step runs on, and say what it held (nocx-atpw5.1.40).
 *
 * WHY THIS EXISTS. The ci-e2e leg died of ENOSPC on a runner whose root disk
 * held 61 G of 72 G at the end, and the end-of-job guard read 12 G free and
 * said nothing: free space at the end does not describe the peak. The peak is
 * the only number that matters, and until a run records it, "what held the
 * space" is a guess. So this wraps the step's command, samples free space on
 * the mount for as long as it runs, and keeps the minimum — a minimum that came
 * back is still the fact that says the run nearly died.
 *
 * WHEN IT FALLS. Below the threshold it takes ONE breakdown, host and
 * container: the runner's own /var/lib/docker and cache root, and inside each
 * running e2e container its writable layer, /tmp and test-results. One, because
 * a sampler that logs on every tick is a sampler that fills the log it was
 * meant to protect.
 *
 * WHAT IT MAY NOT DO. It never changes the step's exit status, it never throws
 * past its own guard, and it never outlives the step: the command is this
 * process's child, and the sampling stops when the child does. Its output is a
 * handful of lines plus, on a bad run, one breakdown.
 *
 *   node scripts/e2e-disk-watch.mjs [--threshold-mb N] [--interval-ms N]
 *        [--mount PATH] [--state FILE] [--no-breakdown] -- <command> [args...]
 */
import { execFileSync, spawn } from 'node:child_process'
import { readFileSync, realpathSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * 6 GiB. On the 72 G runner class the leg starts with about 12 G free; the
 * image build and the run are a few GiB, and a browser's core dump is
 * unbounded. Below 6 GiB the next few GiB of anything is the difference
 * between a red test and a red job, so that is where the warning belongs.
 */
export const DEFAULT_THRESHOLD_MB = 6144
export const DEFAULT_INTERVAL_MS = 5000
/** A breakdown command may take this long, and no longer: the step is running. */
const COMMAND_TIMEOUT_MS = 20_000
const COMMAND_MAX_BUFFER = 1024 * 1024

/** Free KiB from `df -Pk <mount>`, refusing output it cannot read. A parser
 *  that answered 0 would report a full disk on every machine. */
export function parseDfFreeKb(text) {
  const lines = text.trim().split('\n')
  const row = (lines[lines.length - 1] ?? '').trim().split(/\s+/)
  const kb = Number(row[3])
  if (!Number.isFinite(kb)) {
    throw new Error(`df -Pk: cannot read free KiB from ${JSON.stringify(text)}`)
  }
  return kb
}

/** The minimum free space seen, and whether it ever fell below the threshold.
 *  `record` returns 'crossed' exactly once, so the caller logs the breakdown
 *  once however long the run stays low. */
export class DiskWatch {
  constructor({ thresholdKb, mount = '/' }) {
    this.thresholdKb = thresholdKb
    this.mount = mount
    this.minimumKb = null
    this.minimumAt = null
    this.samples = 0
    this.belowThreshold = false
    this.reported = false
  }

  record(freeKb, atMs) {
    this.samples += 1
    if (this.minimumKb === null || freeKb < this.minimumKb) {
      this.minimumKb = freeKb
      this.minimumAt = atMs
    }
    if (freeKb < this.thresholdKb) {
      this.belowThreshold = true
      if (!this.reported) {
        this.reported = true
        return 'crossed'
      }
    }
    return 'sampled'
  }
}

const mib = (kb) => Math.round(kb / 1024)
const iso = (ms) =>
  new Date(ms)
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, 'Z')

/** One line a reader can find: what the minimum was, and when. */
export function summaryLine(watch) {
  if (watch.minimumKb === null) return 'disk-watch: no sample was taken'
  return `disk-watch: minimum free ${mib(watch.minimumKb)} MiB on ${watch.mount} at ${iso(watch.minimumAt)} (${watch.samples} samples)`
}

/** The annotation for a run whose minimum fell below the threshold, or '' —
 *  this is the line that reaches a reader without opening a log, and it stays
 *  true after the space came back. */
export function annotationFor(watch) {
  if (!watch.belowThreshold || watch.minimumKb === null) return ''
  return (
    `::error::this e2e leg's free space fell to ${mib(watch.minimumKb)} MiB on ${watch.mount} ` +
    `at ${iso(watch.minimumAt)}, below the ${mib(watch.thresholdKb)} MiB threshold — it came back, ` +
    'but a run that reaches this line was one allocation from ENOSPC (nocx-atpw5.1.40)'
  )
}

/** Run a command for its output, never for its status: a breakdown that fails
 *  is a line in the log, not a second failure. */
function capture(label, command, args, options = {}) {
  try {
    const out = execFileSync(command, args, {
      encoding: 'utf8',
      timeout: COMMAND_TIMEOUT_MS,
      maxBuffer: COMMAND_MAX_BUFFER,
      stdio: ['ignore', 'pipe', 'pipe'],
      ...options,
    })
    return out.trimEnd()
  } catch (err) {
    const why = err.killed
      ? `timed out after ${COMMAND_TIMEOUT_MS} ms`
      : (err.message ?? String(err))
    return `${label}: (${why})`
  }
}

/** The containers this run's step is running, by the image ci.yml hands it. */
function e2eContainers() {
  const ids = capture('docker ps', 'docker', ['ps', '-q', '--filter', 'ancestor=nocx-e2e:local'])
    .split('\n')
    .filter(Boolean)
  return ids
}

/** ONE breakdown: host and container, small enough to read. */
function breakdown(watch) {
  const lines = [
    `disk-watch: free space fell below ${mib(watch.thresholdKb)} MiB (${mib(watch.minimumKb)} MiB at ${iso(watch.minimumAt)}) — one breakdown:`,
    '-- host --',
    capture('df', 'df', ['-h', watch.mount]),
    capture('du docker', 'sh', ['-c', 'du -sh /var/lib/docker 2>/dev/null || true']),
    capture('du cache', 'sh', [
      '-c',
      'for d in "$RUNNER_TEMP/nocx-e2e-cache" test-results .e2e; do [ -e "$d" ] && du -sh "$d"; done 2>/dev/null || true',
    ]),
  ]
  const ids = e2eContainers()
  if (ids.length === 0) {
    lines.push('-- container -- none running at this sample')
  }
  for (const id of ids) {
    lines.push(`-- container ${id.slice(0, 12)} (writable layer and what it holds) --`)
    lines.push(
      capture('docker ps -s', 'docker', [
        'ps',
        '-s',
        '--no-trunc',
        '--filter',
        `id=${id}`,
        '--format',
        '{{.Names}} size={{.Size}}',
      ]),
    )
    lines.push(
      capture('docker exec', 'docker', [
        'exec',
        id,
        'sh',
        '-c',
        'df -h /tmp /work 2>/dev/null; du -sh /tmp /work/test-results /work/.e2e 2>/dev/null; du -sh /tmp/* 2>/dev/null | sort -h | tail -10',
      ]),
    )
  }
  process.stdout.write(`${lines.filter(Boolean).join('\n')}\n`)
}

/**
 * The job's end: read every state file the steps wrote and say the peak. A
 * separate mode because the step that dipped may have finished long before the
 * job does, and because "the space came back" is exactly the case this must
 * still report.
 */
export function reportLines(states) {
  const lines = []
  const dipped = []
  for (const state of states) {
    if (!state) continue
    lines.push(
      `disk-watch: minimum free ${state.minimumFreeMiB} MiB on ${state.mount} at ${state.minimumAt}` +
        ` (threshold ${state.thresholdMiB} MiB, ${state.samples} samples, step exit ${state.exit})`,
    )
    if (state.belowThreshold) dipped.push(state)
  }
  if (dipped.length > 0) {
    const worst = dipped.reduce((a, b) => (a.minimumFreeMiB <= b.minimumFreeMiB ? a : b))
    lines.push(
      `::error::an e2e step's free space fell to ${worst.minimumFreeMiB} MiB on ${worst.mount} at ` +
        `${worst.minimumAt}, below the ${worst.thresholdMiB} MiB threshold — it came back, and free ` +
        'space at the end does not describe the peak (nocx-atpw5.1.40)',
    )
  }
  return lines
}

function parseArgs(argv) {
  const options = {
    thresholdMb: DEFAULT_THRESHOLD_MB,
    intervalMs: DEFAULT_INTERVAL_MS,
    mount: '/',
    state: '',
    breakdown: true,
    report: null,
    command: [],
  }
  let i = 0
  for (; i < argv.length; i++) {
    const arg = argv[i]
    if (arg === '--') {
      options.command = argv.slice(i + 1)
      break
    }
    const value = () => {
      const next = argv[++i]
      if (next === undefined) throw new Error(`${arg} needs a value`)
      return next
    }
    switch (arg) {
      case '--report':
        options.report = argv.slice(i + 1)
        return options
      case '--threshold-mb':
        options.thresholdMb = Number(value())
        break
      case '--interval-ms':
        options.intervalMs = Number(value())
        break
      case '--mount':
        options.mount = value()
        break
      case '--state':
        options.state = value()
        break
      case '--no-breakdown':
        options.breakdown = false
        break
      default:
        throw new Error(`unknown option ${arg}`)
    }
  }
  if (!options.report && options.command.length === 0) throw new Error('no command after --')
  return options
}

function main() {
  let options
  try {
    options = parseArgs(process.argv.slice(2))
  } catch (err) {
    process.stderr.write(`disk-watch: ${err.message}\n`)
    process.exit(2)
  }
  if (options.report) {
    const states = []
    for (const file of options.report) {
      try {
        states.push(JSON.parse(readFileSync(file, 'utf8')))
      } catch (err) {
        process.stdout.write(`disk-watch: ${file}: no usable record (${err.message})\n`)
      }
    }
    for (const line of reportLines(states)) process.stdout.write(`${line}\n`)
    process.exit(0)
  }
  const startedAt = Date.now()
  const watch = new DiskWatch({ thresholdKb: options.thresholdMb * 1024, mount: options.mount })

  process.stdout.write('disk-watch: df -h at start\n')
  process.stdout.write(`${capture('df -h', 'df', ['-h'])}\n`)
  process.stdout.write(
    `disk-watch: sampling ${options.mount} every ${options.intervalMs} ms against a ` +
      `${options.thresholdMb} MiB threshold\n`,
  )

  const [command, ...args] = options.command
  const child = spawn(command, args, { stdio: 'inherit' })
  let childExit = null

  const sample = () => {
    try {
      const at = Date.now()
      const crossed = watch.record(parseDfFreeKb(capture('df', 'df', ['-Pk', options.mount])), at)
      if (crossed === 'crossed' && options.breakdown) breakdown(watch)
    } catch (err) {
      process.stdout.write(`disk-watch: sample skipped (${err.message})\n`)
    }
  }
  sample()
  const timer = setInterval(sample, options.intervalMs)
  timer.unref()

  const finish = (status) => {
    clearInterval(timer)
    sample()
    const endedAt = Date.now()
    process.stdout.write(`${summaryLine(watch)}\n`)
    const annotation = annotationFor(watch)
    if (annotation) process.stdout.write(`${annotation}\n`)
    if (options.state) {
      try {
        writeFileSync(
          options.state,
          `${JSON.stringify(
            {
              mount: watch.mount,
              thresholdMiB: options.thresholdMb,
              minimumFreeMiB: watch.minimumKb === null ? null : mib(watch.minimumKb),
              minimumAt: watch.minimumAt === null ? null : new Date(watch.minimumAt).toISOString(),
              samples: watch.samples,
              belowThreshold: watch.belowThreshold,
              startedAt: new Date(startedAt).toISOString(),
              endedAt: new Date(endedAt).toISOString(),
              exit: status,
            },
            null,
            2,
          )}\n`,
        )
      } catch (err) {
        process.stdout.write(`disk-watch: state not written (${err.message})\n`)
      }
    }
    process.exit(status)
  }

  child.on('error', (err) => {
    process.stdout.write(`disk-watch: cannot run ${command}: ${err.message}\n`)
    finish(127)
  })
  child.on('exit', (code, signal) => {
    childExit = code ?? (signal ? 128 + 15 : 1)
    finish(childExit)
  })
  for (const [signal, number] of [
    ['SIGINT', 2],
    ['SIGTERM', 15],
  ]) {
    process.on(signal, () => {
      try {
        child.kill(signal)
      } catch {
        /* already gone */
      }
      clearInterval(timer)
      process.exit(128 + number)
    })
  }
}

// Only when run as the program: the unit test imports the pure parts, and an
// unguarded main() would parse the test runner's argv and exit 2.
if (
  process.argv[1] &&
  realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))
) {
  main()
}
