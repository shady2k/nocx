import { statfsSync } from 'node:fs'

/**
 * Refuse to start the suite when the disk is nearly full.
 *
 * A `workers` cap limits how fast a run consumes disk; it does not bound the
 * total. This does. The failure it prevents is not a red test run — running out
 * of space mid-suite can take down whatever else shares the filesystem, and the
 * resulting damage is unrelated to anything the tests were measuring.
 *
 * Deliberately advisory when the platform cannot answer: statfs is unavailable
 * on some runtimes, and refusing to run because a disk stat could not be read
 * would be a worse failure than the one this guards against.
 */

const DEFAULT_MIN_FREE_GB = 3
const BYTES_PER_GB = 1024 ** 3

export default function preflight(): void {
  const raw = process.env.PW_MIN_FREE_GB
  const minFreeGb = raw ? Number(raw) : DEFAULT_MIN_FREE_GB

  if (!Number.isFinite(minFreeGb) || minFreeGb <= 0) {
    console.warn(`nocx e2e preflight: ignoring unusable PW_MIN_FREE_GB=${raw}`)
    return
  }

  let freeBytes: number
  try {
    const stat = statfsSync(process.cwd())
    freeBytes = Number(stat.bavail) * Number(stat.bsize)
  } catch (err) {
    console.warn(
      `nocx e2e preflight: could not read disk stats (${(err as Error).message}) — skipping the free-space check`,
    )
    return
  }

  const freeGb = freeBytes / BYTES_PER_GB
  if (freeGb >= minFreeGb) return

  throw new Error(
    [
      `nocx e2e preflight: refusing to start — ${freeGb.toFixed(2)} GB free, ${minFreeGb} GB required.`,
      '',
      'A full disk does not merely fail the run; it can break unrelated processes',
      'sharing the filesystem. Free space, or lower the bar deliberately with',
      'PW_MIN_FREE_GB=<gb>.',
      '',
      'Usual suspects: the Go build cache, node_modules across worktrees,',
      'test-results/, and Playwright browser downloads.',
    ].join('\n'),
  )
}
