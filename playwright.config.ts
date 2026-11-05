import { defineConfig, type Project } from '@playwright/test'

import { BASE_URL } from './e2e/base-url'

// e2e drives the whole app, not the frontend alone: the stand this config
// starts is cmd/nocx-server — the real Go backend on a real PTY, the real
// transport — with vite serving the real renderer. That is the only place
// layout, focus and GPU behaviour are observable; jsdom has none of them.
//
// ONE arrangement. There used to be two — `wails dev` here and a shell script
// that started nocx-server and set NOCX_WS_PORT — and seven specs could only
// run on the second, kept off the first by a hand-written list. That is how
// seven files failed on their first line while the shards stayed green
// (nocx-azxe.2), and how "where is the home" became a question with two right
// answers. e2e/stand.ts owns the lifecycle now and e2e/check-coverage.mjs is
// the receipt that no spec file is left uncollected.
//
// wails is not started here. What the desktop host proves — the assets it
// serves and the real window.go it injects — is its own subject, and it has
// its own project rather than being the platform every spec pays for.
//
// Ports and URLs live in e2e/base-url.ts: the harness needs the same answer for
// the worker-scoped readiness page it opens itself, and a second copy here
// would agree until someone changed one of them.

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

export default defineConfig({
  testDir: './e2e',

  // The first-command regression is specific to macOS zsh. Linux e2e runs as
  // bash, so it cannot exercise that failure; keep the spec in the collected
  // set only on macOS, where its missing-zsh/plugin guards remain active.
  ...(process.platform === 'linux' ? { testIgnore: ['**/first-command-completes.spec.ts'] } : {}),

  // A HUNG TEST IS CUT OFF IN HALF A MINUTE, not a whole one. Nothing in this
  // suite legitimately takes 30 seconds: the slowest honest spec is about seven,
  // and a run of 338 cases pays this ceiling only for tests that are already
  // broken.
  timeout: 30_000,

  // 20 seconds was raised from Playwright's 5 for a real, measured failure —
  // 86 assertions of 200 in CI run 31087876366 reporting "resolved to 0
  // elements" while the snapshot taken moments later showed the tab present.
  // The cause was named precisely: "Under `wails dev` each page.goto is a full
  // reload: vite transforms modules on demand, the renderer re-establishes the
  // WebSocket, and the backend spawns a PTY."
  //
  // THAT ARRANGEMENT NO LONGER EXISTS. This file's own header says so — there
  // is ONE stand now, cmd/nocx-server plus vite, and `wails is not started
  // here`. The number outlived the machine it was measured against, which is
  // how a workaround becomes a constant.
  //
  // What it costs is paid entirely on red. A passing assertion returns the
  // moment its state arrives; a failing one waits out the whole budget. At 20
  // seconds a run with 21 failures burns seven minutes doing nothing but
  // waiting for something that will not happen — measured here on 2026-08-17,
  // and paid again on every debugging round.
  //
  // So it goes back to the library default. If the first-tab assertion turns
  // out to need more than five seconds on the nocx-server stand, THAT is the
  // defect: AGENTS.md's rule is that a test waits on an observable state change
  // and never on a duration, and an assertion that needs a long budget is one
  // that is not waiting on the right thing. Raise the waiting, not the number.
  expect: { timeout: 5_000 },

  // Refuse to start when the disk is nearly full.
  //
  // Scope honestly: this is a floor on STARTING, not a bound on consumption. The
  // largest consumer here is not the suite at all — a crashing browser process
  // can write a multi-gigabyte core dump in seconds, which no test-side setting
  // can throttle. That is handled outside the repo by capping dump size. What
  // this guard buys is refusing to begin a run on a filesystem that is already
  // too full to survive one.
  // The stand — cmd/nocx-server plus vite — is brought up here and taken down
  // in globalTeardown, so `npx playwright test` is the whole command on a
  // developer's machine and in CI. preflight's disk floor runs first.
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',

  // One worker by default, because the default must assume this process is NOT
  // alone on the machine. With one worker per run, total browsers equals the
  // number of runs — the only bound that stays predictable as concurrency grows.
  // A higher number is correct on a dedicated machine and is one env var away.
  workers: process.env.PW_WORKERS ? Number(process.env.PW_WORKERS) : 1,

  // WHAT A RUN KEEPS (nocx-atpw5.1.40). The default is 'always': every test's
  // output directory survives, passing or failing. Measured on this machine, a
  // green three-case spec left two backends' debug logs and their helpers'
  // behind (17 MB), and the memory soak's two heap snapshots are 74 MB for a
  // test that passed. Nothing reads a passing test's artifacts, and the
  // failure block is printed at the moment of failure from the live files, so
  // 'failures-only' removes bytes and no evidence: Playwright deletes the
  // directory of a test that passed, and keeps it whole for one that did not.
  preserveOutput: 'failures-only',

  use: {
    baseURL: BASE_URL,
    // THE TRACE IS RECORDED FOR EVERY TEST AND KEPT ONLY FOR FAILURES, and
    // what dominates its size is the screencast: measured here, one spec (10
    // cases, 3 minutes) peaked at 56 MB of trace with screenshots and 31 MB
    // without — the screenshots alone were 45% of it — while the longest case
    // of the heaviest spec (transcript-scroll-budget, 4 minutes) peaked at
    // 656 MB. The DOM snapshots, sources and network stay: they are what
    // `show-trace` is for. The visual timeline is not, and the failure block
    // never reads the trace at all.
    trace: { mode: 'retain-on-failure', screenshots: false },
  },
  projects,
})
