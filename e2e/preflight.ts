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
 * Refuse to start the headless path unless the runner declared a home boundary.
 *
 * On the default path playwright.config.ts owns the backend and applies the
 * boundary itself. On the headless path the runner starts devharness and vite
 * outside Playwright, so the suite cannot apply anything — it can only decline
 * to run against a backend nobody isolated.
 *
 * Refusing is the point. This suite used to run happily against the developer's
 * real home, resetting their theme and rewriting their profile on every pass
 * (nocx-ti8w), and it stayed green throughout. A red run with an instruction in
 * it is strictly better than a green run that quietly rewrites somebody's
 * settings, so the missing boundary is an error rather than a warning.
 */
function assertHeadlessRunnerDeclaredABoundary(): void {
  if (!process.env.NOCX_WS_PORT) return
  if (process.env.NOCX_E2E_HOME_DIR) return

  throw new Error(
    [
      'nocx e2e preflight: refusing to start the headless path with no home boundary.',
      '',
      'NOCX_WS_PORT is set, so the backend was started by you rather than by',
      'Playwright, and nothing here can isolate it. Without a boundary a run',
      'writes the real home: settings, SSH profiles, vault documents, ~/.nocx',
      'and the shell rc files.',
      '',
      'Start devharness with a disposable home and export it, e.g.',
      '',
      '  export NOCX_E2E_HOME_DIR="$(mktemp -d)/home" && mkdir -p "$NOCX_E2E_HOME_DIR"',
      '  HOME="$NOCX_E2E_HOME_DIR" \\',
      '    XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= ZDOTDIR= BASH_ENV= ENV= \\',
      '    NOCX_WS_ADDR=127.0.0.1:9876 ./devharness',
      '',
      'The same variables e2e/home-isolation.ts strips, for the same reasons:',
      'XDG_CONFIG_HOME outranks $HOME, and the shell entry points let the login',
      'shell a PTY spawns read back out of the boundary.',
    ].join('\n'),
  )
}

export default function preflight(): void {
  assertHeadlessRunnerDeclaredABoundary()

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
