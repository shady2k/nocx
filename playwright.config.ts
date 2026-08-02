import { defineConfig, type Project } from '@playwright/test'
import path from 'node:path'

import { createHomeIsolation } from './e2e/home-isolation'

// e2e drives the whole app, not the frontend alone: `wails dev` serves the
// built UI *and* the bound Go methods, so a test here exercises the real
// transport, the real PTY and the real renderer. That is the only place layout,
// focus and GPU behaviour are observable — jsdom has none of them.
//
// The port is env-derived rather than a constant because it is a shared
// resource. Two runs on one machine that both assume the same port do not merely
// compete: the second attaches to the first one's app and reports results for a
// tree it never built. See reuseExistingServer below.
const WAILS_PORT = process.env.NOCX_WAILS_PORT ? Number(process.env.NOCX_WAILS_PORT) : 34115
const WAILS_URL = `http://localhost:${WAILS_PORT}`

// Headless path: when NOCX_WS_PORT is set, the runner has started devharness
// (Go backend + WebSocket) and vite (frontend dev server) separately. No wails
// or GTK required — Playwright drives a plain browser against the vite URL.
// This path is also far cheaper: it skips the Go compile and frontend build that
// `wails dev` performs on every cold start.
const HEADLESS = !!process.env.NOCX_WS_PORT
const BASE_URL = HEADLESS ? process.env.NOCX_BASE_URL || 'http://localhost:5173' : WAILS_URL

// Both browsers stay declared. WebKit is not redundant coverage: nocx-q18's
// glyph corruption reproduces in WKWebView and not in Chromium, and WebKit is
// the closest Playwright can get to the real app. Dropping it from the default
// would leave that class of regression unwatched, so the lever here selects a
// SUBSET for a cheap run rather than narrowing what the suite knows about.
//
//   PW_PROJECTS=chromium   → one browser, roughly half the work
//   unset                  → both, which is what CI should keep doing
const ALL_PROJECTS: Project[] = [
  { name: 'chromium', use: { browserName: 'chromium' } },
  { name: 'webkit', use: { browserName: 'webkit' } },
]
const wanted = process.env.PW_PROJECTS?.split(',')
  .map((s) => s.trim())
  .filter(Boolean)
const projects = wanted?.length
  ? ALL_PROJECTS.filter((p) => wanted.includes(p.name!))
  : ALL_PROJECTS
if (wanted?.length && projects.length === 0) {
  throw new Error(
    `PW_PROJECTS=${process.env.PW_PROJECTS} matched no project; known: ${ALL_PROJECTS.map((p) => p.name).join(', ')}`,
  )
}

// The disposable home every backend this config launches is given. A fixed path
// under the repo rather than a fresh mkdtemp, for termic's reason: it survives
// the run, so a failure can be inspected, and it does not depend on when
// Playwright evaluates the config relative to globalSetup. `.e2e/` is ignored by
// git. Runs already serialise on the app port, so sharing it costs nothing that
// was not already shared.
//
// The env below is what stops a suite run rewriting the developer's settings,
// reinstalling their shell hooks and reading their ~/.ssh/config (nocx-ti8w).
// __dirname rather than import.meta.url: the root package.json has no
// "type": "module", so Playwright loads this config as CommonJS.
const E2E_ROOT = path.join(__dirname, '.e2e')
const homeIsolation = createHomeIsolation({ inheritedEnv: process.env, root: E2E_ROOT })
const isolatedEnv: Record<string, string> = Object.fromEntries(
  Object.entries(homeIsolation.env).filter((e): e is [string, string] => e[1] !== undefined),
)

export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,

  // Refuse to start when the disk is nearly full.
  //
  // Scope honestly: this is a floor on STARTING, not a bound on consumption. The
  // largest consumer here is not the suite at all — a crashing browser process
  // can write a multi-gigabyte core dump in seconds, which no test-side setting
  // can throttle. That is handled outside the repo by capping dump size. What
  // this guard buys is refusing to begin a run on a filesystem that is already
  // too full to survive one.
  globalSetup: './e2e/preflight.ts',

  // One worker by default, because the default must assume this process is NOT
  // alone on the machine. With one worker per run, total browsers equals the
  // number of runs — the only bound that stays predictable as concurrency grows.
  // A higher number is correct on a dedicated machine and is one env var away.
  workers: process.env.PW_WORKERS ? Number(process.env.PW_WORKERS) : 1,

  use: {
    baseURL: BASE_URL,
    trace: 'retain-on-failure',
  },
  projects,

  // The suite starts its own app: a test that silently needs a `wails dev`
  // someone remembered to launch is red on a clean machine for a reason that
  // has nothing to do with the code under test, and green only by luck.
  //
  // reuseExistingServer is OPT-IN, not "anything but CI". Attaching to a server
  // this run did not start is only safe when the operator knows it serves the
  // same tree; otherwise the suite silently measures someone else's build and
  // reports the answer as if it were about this one. A wrong green is worse than
  // a slow start, so the default builds its own and PW_REUSE_SERVER=1 opts out.
  //
  // The timeout is sized for a cold `wails dev`, which compiles the Go binary
  // and installs/builds the frontend before it ever listens.
  //
  // gracefulShutdown is load-bearing, not politeness. `wails dev` starts the
  // frontend watcher in a process group of its own (Setpgid in the wails CLI),
  // so no group kill aimed at wails can reach it; wails reaps it itself, from a
  // SIGTERM handler, on the way out. Playwright's default is to SIGKILL the
  // group — that handler never runs, vite is orphaned, and because it inherited
  // the run's stdio the pipe never closes and the run hangs long after the last
  // test. On a runner that is a job burning its timeout rather than failing.
  //
  // In headless mode the caller owns both processes; skip the webServer stanza.
  ...(!HEADLESS
    ? {
        webServer: {
          command: 'wails dev',
          // The boundary. `wails dev` passes its environment to the backend it
          // builds and runs, so this is the one place the default path can be
          // isolated — there is no fixture between Playwright and the app.
          env: isolatedEnv,
          url: WAILS_URL,
          reuseExistingServer: !!process.env.PW_REUSE_SERVER,
          timeout: 240_000,
          gracefulShutdown: { signal: 'SIGTERM', timeout: 15_000 },
          stdout: 'pipe' as const,
          stderr: 'pipe' as const,
        },
      }
    : {}),
})
