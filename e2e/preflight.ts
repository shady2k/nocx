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

/**
 * Refuse to start when the caller believes they are driving a backend of
 * their own.
 *
 * There is one stand and Playwright owns it (e2e/stand.ts). `NOCX_WS_PORT`
 * used to be how a runner said "I started the backend, here is where it is",
 * and it does not say that any more — nocx-server binds a port the OS picks
 * and hands the address out over its discovery socket, so nothing in this
 * suite reads that variable. A run with it set is therefore a run where
 * somebody's belief about which backend is under test is already wrong, and
 * the specs would report results for a process they never touched.
 *
 * Refusing is the point, and this is the second thing it has refused for the
 * same reason. It used to guard the home boundary: the suite ran happily
 * against the developer's real home, resetting their theme and rewriting
 * their profile on every pass (nocx-ti8w), and stayed green throughout. That
 * boundary is now structural — e2e/stand.ts and e2e/harness.ts both build the
 * environment through createHomeIsolation, which RAISES rather than warns, so
 * there is no launcher left that could forget it — and `NOCX_E2E_HOME_DIR`
 * still travels with every backend the suite starts as the record of which
 * home it got.
 *
 * A red run with an instruction in it is strictly better than a green run
 * about the wrong process, so this is an error rather than a warning.
 */
function refuseAHandStartedBackend(): void {
  if (!process.env.NOCX_WS_PORT) return

  throw new Error(
    [
      'nocx e2e preflight: refusing to start with NOCX_WS_PORT set.',
      '',
      'Nothing in this suite reads it. The backend is cmd/nocx-server, started',
      'by e2e/stand.ts from globalSetup; it binds loopback on a port the OS',
      'picks and reports it over its discovery socket, so a port cannot be',
      'chosen from outside. If this variable is set, whatever you meant it to',
      'point at is not the process the specs are about to measure.',
      '',
      'It is most likely left over from `make dev-web`, which exports it for',
      'vite. Unset it and run:',
      '',
      '  npx playwright test',
      '',
      'The home boundary the suite used to check here is applied by the stand',
      'itself (e2e/home-isolation.ts), which refuses rather than warns.',
    ].join('\n'),
  )
}

export default function preflight(): void {
  refuseAHandStartedBackend()

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
