/**
 * The e2e home boundary: one place that decides what a backend the suite
 * launches is allowed to see as `$HOME`.
 *
 * # Why a boundary rather than an isolated config directory
 *
 * The first fix for nocx-ti8w moved the profile directory by build tag, so no
 * build from this repo resolves the installed app's documents. That is the
 * floor, and it covers the interactive dev stand, which has no fixture at all.
 * It does not cover everything a backend reaches through the home directory:
 *
 *   ~/.config/nocx-dev     settings, profiles, vault documents
 *   ~/.nocx + rc files     shell integration, installed on every App.Start()
 *   ~/.ssh/config          read by the composition root
 *
 * Redirecting `$HOME` moves all three at once, needs no production code, and —
 * unlike `XDG_CONFIG_HOME` — works on macOS, where `os.UserHomeDir()` honours
 * `$HOME` and `internal/storage`'s darwin resolver never looks at XDG.
 *
 * The design is orca's (tests/e2e/helpers/electron-home-isolation.ts), adopted
 * after comparing it with tabby (no tests at all) and termic (build-chosen data
 * dir plus a debug-only env override). Three of its properties are the point,
 * and each answers a way the previous arrangement failed:
 *
 *  - Restricted variables are STRIPPED from the inherited environment, not
 *    merely overridden. `XDG_CONFIG_HOME` outranks `$HOME` on Linux, so one
 *    leaked from the developer's shell would route the config dir straight back
 *    out of the boundary.
 *  - `ZDOTDIR`, `BASH_ENV` and `ENV` are restricted too. A terminal spawns the
 *    user's login shell; without these the shell reads the user's rc files from
 *    inside a run that is supposed to be isolated.
 *  - An attempt to override any of them RAISES. A boundary a caller can quietly
 *    opt out of is the arrangement that produced nocx-ti8w in the first place,
 *    where correct isolation existed and three specs out of twenty-five used it.
 */
import { mkdirSync, realpathSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'

/**
 * Variables that decide where "home" is, or that let a shell reach back into
 * the real one. Stripped from what a launched backend inherits, and refused if
 * a caller tries to set one.
 *
 * Deliberately no Windows entries: `storage.NewOSPaths` fails outright on any
 * platform but darwin and linux, so `USERPROFILE` and friends would be
 * speculative rather than defensive.
 */
const RESTRICTED_ENV_KEYS = new Set([
  'HOME',
  'XDG_CONFIG_HOME',
  'XDG_DATA_HOME',
  'XDG_CACHE_HOME',
  'ZDOTDIR',
  'BASH_ENV',
  'ENV',
  'NOCX_E2E_HOME_DIR',
])

export interface HomeIsolation {
  /** The environment a launched backend should be given, boundary applied. */
  env: NodeJS.ProcessEnv
  /** The canonical disposable home. */
  isolatedHome: string
  /** The real home the boundary exists to keep out of reach. */
  realHome: string
}

export interface HomeIsolationOptions {
  /** Usually `process.env` — the environment the runner itself was given. */
  inheritedEnv: NodeJS.ProcessEnv
  /** Extra variables the caller wants the backend to have. */
  overrideEnv?: NodeJS.ProcessEnv
  /** A disposable directory owned by the caller; the home is `<root>/home`. */
  root: string
  /** Overridable so the boundary's own behaviour can be asserted. */
  realHome?: string
}

function comparable(p: string): string {
  return path.resolve(p)
}

export function isSameHome(left: string, right: string): boolean {
  return comparable(left) === comparable(right)
}

function refuseBoundaryOverride(overlay: NodeJS.ProcessEnv, overlayName: string): void {
  const offending = Object.keys(overlay).find((key) => RESTRICTED_ENV_KEYS.has(key.toUpperCase()))
  if (offending) {
    throw new Error(
      `${overlayName}.${offending} cannot override the e2e home boundary — ` +
        'give the backend a disposable root instead',
    )
  }
}

function withoutRestricted(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  return Object.fromEntries(
    Object.entries(env).filter(([key]) => !RESTRICTED_ENV_KEYS.has(key.toUpperCase())),
  )
}

/**
 * Build the environment for a backend the suite launches.
 *
 * Throws rather than degrading: an unusable root or an attempted override is a
 * setup mistake, and the failure this guards against is one where continuing
 * looks exactly like success.
 */
export function createHomeIsolation({
  inheritedEnv,
  overrideEnv = {},
  root,
  realHome = os.homedir(),
}: HomeIsolationOptions): HomeIsolation {
  refuseBoundaryOverride(overrideEnv, 'overrideEnv')

  const requested = path.join(root, 'home')
  mkdirSync(requested, { recursive: true, mode: 0o700 })

  // Canonical, because a tmpdir path is an alias on macOS (/var → /private/var)
  // and a home the backend resolves differently from the one the suite asserts
  // is a boundary that cannot be checked.
  const isolatedHome = realpathSync.native(requested)

  if (isSameHome(isolatedHome, realHome)) {
    throw new Error('refusing to launch the e2e backend with the developer home as its HOME')
  }

  return {
    isolatedHome,
    realHome,
    env: {
      ...withoutRestricted(inheritedEnv),
      ...overrideEnv,
      HOME: isolatedHome,
      NOCX_E2E_HOME_DIR: isolatedHome,
    },
  }
}

/**
 * Verify a backend actually resolved the isolated home.
 *
 * Building the environment proves what was handed over, not what was used — a
 * binary that reads the home some other way satisfies the first and fails the
 * second, and that gap is the whole reason this function exists separately.
 *
 * The message names neither home: a failed safety assertion lands in shared CI
 * artifacts, and the real path carries a developer's username.
 */
export function assertResolvedIsolatedHome(
  actualHome: string,
  isolation: Pick<HomeIsolation, 'isolatedHome' | 'realHome'>,
): void {
  if (
    !isSameHome(actualHome, isolation.isolatedHome) ||
    isSameHome(actualHome, isolation.realHome)
  ) {
    throw new Error('the e2e backend escaped the disposable home boundary')
  }
}
