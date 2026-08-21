import { test as base, expect as baseExpect, type Page } from '@playwright/test'

import { BASE_URL } from './base-url'
import { readStand } from './stand'

export { expect } from '@playwright/test'
export type { Page } from '@playwright/test'

/**
 * Wait until the prompt editor owns input and typing can safely begin.
 *
 * Scoped to the ACTIVE pane, not to the document. Every open tab has its own
 * `.nocx-editor-input`, so a bare locator resolves to one element with a single
 * tab and N with more — Playwright's strict mode then fails the wait rather than
 * the assertion, which reads like a product bug and is not one. That is what
 * broke every multi-tab-input case (nocx-4ff.28) when this helper met a suite
 * that opens a second tab.
 *
 * Waiting on the active pane is also the more correct statement: readiness is a
 * property of the tab under test, not of whichever editor the DOM lists first.
 */
export async function promptReady(page: Page): Promise<void> {
  const input = page.locator('.pane.active .nocx-editor-input')
  await baseExpect(input).toBeVisible({ timeout: 10_000 })
  await baseExpect(input).toBeFocused({ timeout: 10_000 })
}

/**
 * Put the sidebar on `viewId` and leave it there.
 *
 * IDEMPOTENT ON PURPOSE, because the activity-bar button is a TOGGLE and the
 * sidebar's active view is now PERSISTED (nocx-mqie.1, ADR-0033): a spec that
 * reloads mid-test comes back with its view ALREADY showing, and a second
 * unconditional click is then the thing that closes the panel it was asked to
 * open. That is what the notes spec did across its `page.reload()`, and it is
 * the single-test version of what `resetStand` handles between tests.
 *
 * `aria-selected` is the button's own account of whether its view is the one
 * on screen (ui/icon-button.tsx), so it answers the question directly rather
 * than by guessing from the panel.
 */
export async function showSidebarView(page: Page, viewId: string): Promise<void> {
  const button = page.locator(`.activity-bar button[data-view="${viewId}"]`)
  await baseExpect(button).toBeVisible({ timeout: 15_000 })
  if ((await button.getAttribute('aria-selected')) !== 'true') {
    await button.click()
  }
  await baseExpect(button).toHaveAttribute('aria-selected', 'true', { timeout: 10_000 })
}

/**
 * Wait until the Settings page has LOADED, not until it has merely mounted.
 *
 * NEVER `.ui-page__scroll` — thirteen specs used to open Settings and wait for
 * that, and the reason it looked like a readiness signal is the reason it is
 * the wrong one. Its presence reports the SCROLL MODE, and Settings changes
 * mode as it loads: with nothing selected the body falls back to `page` mode
 * and paints a scroller around "Loading settings…", then `settings.describe`
 * answers, the landing effect selects the first registry page — Connections,
 * which is `scrollMode: 'contained'` (settings.tsx) — and the scroller is
 * REPLACED by `.ui-page__contained`. So the element exists only while Settings
 * is still loading, and a spec waiting for it was racing the load: it passed
 * when it caught the loading frame and failed when it missed it, which is
 * exactly the intermittency nocx-rv53x records. The page renders every time —
 * the error-context snapshot of every failing run shows a fully painted
 * Connections page — so there is no product defect behind those thirty
 * failures, only a spec waiting on a frame that is on its way out.
 *
 * The rail is the honest signal: `<Show when={loadState() === 'ready'}>` is
 * what puts it on screen, so it appears when the data has arrived and stays
 * for every page, in either scroll mode.
 *
 * A spec that wants the SCROLLER — the scroll-ownership and settings-scroll
 * probes do — waits on this first and then opens a `page`-mode section, which
 * is what a user does and is the only state in which that element is a stable
 * fact rather than a passing frame.
 */
export async function settingsReady(page: Page): Promise<void> {
  await baseExpect(page.locator('[aria-label="Settings sections"]')).toBeVisible({
    timeout: 15_000,
  })
}

/**
 * Click into the active pane's prompt editor.
 *
 * Six specs used to spell this `page.mouse.click(box.x + box.width / 2, box.y +
 * box.height - 30)` on the pane, each with a comment saying what it meant —
 * "near the bottom of the pane, where the editor lives". The 30 is a guess at
 * how tall the editor is, and the editor's height follows the terminal font, so
 * the guess is a claim about the host rather than about the product. Its
 * sibling defect — a double-click at a fixed 120px — was landing on a space in
 * the e2e container while passing on the author's Mac (nocx-z9s9.10).
 *
 * The pane centre is deliberately still avoided, and that reason is real: it
 * lands on the xterm area whose hidden textarea takes focus, and the
 * focus-bounce handler then bails because focus is already inside the xterm
 * container. Clicking the editor itself reaches the editor's own
 * click-to-focus handler, which is the path these specs are about. It also
 * still reaches the pane's handlers — a contextmenu on the editor bubbles — so
 * the right-click paste case keeps working through the same seam.
 */
export async function clickIntoEditor(
  page: Page,
  opts: { button?: 'left' | 'right' } = {},
): Promise<void> {
  await page.locator('.pane.active .nocx-editor-input').click({ button: opts.button ?? 'left' })
}

// vite serves the page alone, so nothing installs the wails runtime — the
// frontend reads window.go at startup and would find nothing. This supplies it,
// pointed at the stand Playwright started.
//
// Read from the stand's manifest rather than from environment variables. The
// backend mints its token at startup, and a process cannot put a value back
// into its parent's environment, so a token that travelled by env had to be
// exported by whatever shell script started the backend — which is precisely
// the second entry point this arrangement removed.
//
// A spec that needs its OWN backend overrides this afterwards with
// bindEndpoint(); init scripts apply in order, so the later one wins.
async function injectWailsShim(page: Page): Promise<void> {
  const stand = readStand()
  await page.addInitScript(
    (opts: { p: number; t: string }) => {
      ;(window as unknown as { go: unknown }).go = {
        main: {
          WailsApp: {
            GetWSPort: () => Promise.resolve(opts.p),
            GetWSToken: () => Promise.resolve(opts.t),
            CheckForUpdate: () => Promise.resolve(null),
            ReportHealthy: () => Promise.resolve(),
            ApplyUpdate: () => Promise.resolve(),
          },
        },
      }
    },
    { p: stand.port, t: stand.token },
  )
}

export const test = base.extend<object, { appReady: void }>({
  // The app answers on its port before it can serve a session, and the suite
  // used to treat those as the same moment.
  //
  // playwright.config.ts waits for the `wails dev` URL, which the webview
  // serves as soon as vite is up. The BACKEND is not up then: app.New probes
  // the OS keystore synchronously (internal/app/app.go:275), and on a macOS
  // runner with no unlocked login keychain that probe runs to its full timeout
  // — five seconds in CI run 31085068686 — before the WebSocket exists. The
  // renderer cannot open a tab without it.
  //
  // Every spec opens with expect(TAB).toHaveCount(1) on the default 5s
  // expect timeout, so all of them raced that startup and most lost: 33 of 74
  // failed on shard 1, each reporting "resolved to 0 elements" while the
  // error-context snapshot taken moments later showed the tab present. It
  // reads as a broken product and is a harness that started measuring too
  // early.
  //
  // So readiness is waited for ONCE per worker, on its own page, with a budget
  // sized for a cold start rather than for an assertion. Raising every spec's
  // expect timeout would have worked too and would have made every genuine
  // failure in the suite slower to report.
  //
  // The startup stall itself is a product defect and is filed separately: a
  // terminal should not wait on a secret store to show a prompt.
  appReady: [
    async ({ browser }, use) => {
      // newPage() inherits nothing from `use`, so baseURL is passed
      // explicitly — from the module the config reads, not a copy.
      const context = await browser.newContext({ baseURL: BASE_URL })
      const page = await context.newPage()
      try {
        await injectWailsShim(page)
        await page.goto('/')
        // AT LEAST ONE, not exactly one. This is a readiness probe — the
        // question it asks is "can the backend serve a session yet", and a tab
        // is the observable proof. It is not an assertion about how many tabs
        // there are, and it never could be from here: this fixture is
        // worker-scoped, so it runs BEFORE the first test and therefore before
        // the layout reset that establishes that precondition.
        //
        // toHaveCount(1) held only while a fresh renderer always drew exactly
        // one tab. Since nocx-isoph.4 the strip is restored from the backend
        // and the stand keeps one home for the whole run, so webkit's worker
        // opens on whatever chromium's left behind — "Received: 2", 182 polls,
        // ninety seconds, every webkit test failing at 0ms behind it.
        await baseExpect(page.locator('.nocx-tab').first()).toBeVisible({ timeout: 90_000 })
      } finally {
        await context.close()
      }
      await use()
    },
    // THE FIXTURE NEEDS ITS OWN BUDGET, and this line is why. Without an
    // explicit timeout Playwright caps a fixture's setup at the TEST timeout —
    // so the 90 seconds asked for above were silently whatever `timeout:` in
    // playwright.config.ts happened to be. That was invisible while the test
    // ceiling was 60s and the cold start usually finished sooner; lowering the
    // ceiling to 30s cut this fixture in half and every webkit test in the run
    // failed at 0ms with "Fixture appReady timeout of 30000ms exceeded during
    // setup" (2026-08-18).
    //
    // A test ceiling and a cold start are different measurements and must not
    // share a number. The ceiling is short on purpose — nothing in this suite
    // legitimately takes thirty seconds — while this runs ONCE per worker and
    // is waiting on a process to boot.
    { scope: 'worker', auto: true, timeout: 120_000 },
  ],

  page: async ({ page }, use) => {
    // BEFORE the test, not after it. A teardown answers for the test that has
    // just run and is skipped when that test dies badly; a setup answers for
    // the test that is about to run, which is the one whose result depends on
    // it. Whatever the last spec left — including a page that crashed with
    // eight tabs open — this is what the next one starts from.
    await resetStand()
    await injectWailsShim(page)
    await use(page)
  },
})

/**
 * Leave the backend holding exactly one undecorated tab, the UI state at its
 * declared defaults, and neither notes nor snippets.
 *
 * THE PRODUCT NOW REMEMBERS TABS (nocx-isoph.4): the backend owns the
 * workspace → tab → pane chain, and a renderer that goes away leaves the rows
 * where they are — which is the whole feature, and is what a renderer reload
 * brings back. The suite runs ONE stand for the entire run
 * (playwright.config.ts globalSetup), so without this every spec would inherit
 * the tabs the previous one opened, and the `toHaveCount(1)` every spec opens
 * with would fail in file order.
 *
 * IT TALKS TO THE BACKEND, NOT TO THE PAGE, and the first version did the
 * opposite: it clicked the close control on every tab, swallowed its own
 * failures, and therefore had two ways of quietly doing nothing. The gate
 * found both. A click cannot reach an SSH pane, because the renderer restores
 * that row into the chain and does not draw it (PaneManager.adopt) — so those
 * rows accumulated all run, and the specs that later reordered a strip were
 * refused with "not a permutation" while the ones asserting on a toast found
 * a leftover "connection was not reopened" warning beside their own.
 *
 * So it opens the control plane the way the renderer does — same URL, same
 * token subprotocol — reads the chain and closes every pane in it. Node's
 * WebSocket sends no Origin, which LoopbackOriginPolicy admits precisely
 * because a non-browser caller still has to present the token.
 *
 * Closing the last pane makes the backend mint a replacement, so this ends on
 * one tab nobody has named, coloured or pinned — the state a fresh profile is
 * in, and the state every spec's first assertion describes.
 *
 * IT THROWS. This is the precondition of every test in the suite, not a
 * tidy-up: a reset that failed silently is how a spec inherits state, and
 * "one spec is red for a reason it states" beats "twenty are red for a reason
 * none of them mention".
 */
async function resetStand(): Promise<void> {
  const stand = readStand()
  const wire = await openControlPlane(stand.port, stand.token)
  try {
    const layout = (await wire.call('layout.read', {})) as { panes: { id: string }[] }
    for (const pane of layout.panes) {
      await wire.call('panes.close', {
        id: pane.id,
        // The replacement is consulted only if this close would leave the
        // application with no tab at all — which the last one does. Its ids
        // are durable and therefore the caller's to mint (§7), exactly as
        // they are for the renderer.
        replacement: { tabId: uuidv7(), paneId: uuidv7(), cwd: '' },
      })
    }
    // AND THE UI STATE, which is a second document and a second way for one
    // spec to decide the next one's result. Since nocx-mqie.1 the sidebar's
    // collapse and its ACTIVE VIEW are remembered across a renderer, so a
    // spec that opened Git leaves Git open — and every view button is a
    // TOGGLE, so the next spec's `click()` on that view closes the panel
    // instead of opening it. That is why the git-panel specs alternated
    // pass/fail down the file: the branch badge was in the DOM and `hidden`,
    // which is what a toggled-shut panel looks like.
    //
    // Written as the whole document, because `uistate.set` takes the whole
    // renderer half (ADR-0033) — and to the DECLARED defaults rather than to
    // "no view", because they are what a fresh profile has: the panel open on
    // the first registered view. `activeViewId: ''` is repaired to that same
    // first view on mount, so the two agree.
    await wire.call('uistate.set', {
      sidebar: { collapsed: false, activeViewId: '', width: 240 },
      activeTab: '',
    })
    // AND THE NOTES LIBRARY, which is the third durable document a spec can
    // leave behind (nocx-9jcx7). A note outlives the renderer that wrote it —
    // that IS the feature — and the suite keeps ONE stand for the whole run,
    // so chromium's notes spec left its "Standup notes" where webkit's would
    // find it and match two rows: "strict mode violation … resolved to 2
    // elements", failing in the full suite and passing when the file is run
    // alone. That signature is a shared store, not a defect in what notes do.
    //
    // Deleting is right here and would be wrong anywhere else: this is a
    // disposable home (`$HOME` is moved for the whole run, e2e/preflight.ts),
    // and the state a spec's first assertion describes is an empty library.
    const notes = (await wire.call('notes.list', {})) as { notes: { id: string }[] }
    for (const row of notes.notes) {
      await wire.call('notes.delete', { id: row.id })
    }
    // AND THE SNIPPET LIBRARY, for the same reason and one more. Snippets are
    // durable the way notes are, so they cross a spec boundary the same way —
    // "e2e fill" resolved to three rows in a local run, one per suite run that
    // had gone before. Until now nothing noticed because snippets.spec.ts
    // deletes what it made, and that is exactly the fragility: a spec that
    // fails in the middle never reaches its own tidy-up, and it poisons every
    // later run against the same home rather than only its own. A precondition
    // that depends on the previous test having succeeded is not a
    // precondition.
    const snips = (await wire.call('snippets.list', {})) as { snippets: { id: string }[] }
    for (const row of snips.snippets) {
      await wire.call('snippets.delete', { id: row.id })
    }
  } finally {
    wire.close()
  }
}

/** One JSON-RPC call at a time over a socket opened for this purpose. The
 *  data plane is not touched: every frame here is text. */
async function openControlPlane(
  port: number,
  token: string,
): Promise<{ call: (method: string, params: unknown) => Promise<unknown>; close: () => void }> {
  const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, `nocx.token.${token}`)
  await new Promise<void>((resolve, reject) => {
    const failed = (): void => reject(new Error(`e2e: control plane refused on port ${port}`))
    ws.addEventListener('open', () => resolve(), { once: true })
    ws.addEventListener('error', failed, { once: true })
    ws.addEventListener('close', failed, { once: true })
  })
  let nextId = 0
  const call = (method: string, params: unknown): Promise<unknown> =>
    new Promise((resolve, reject) => {
      const id = ++nextId
      const onMessage = (ev: MessageEvent): void => {
        if (typeof ev.data !== 'string') return
        const msg = JSON.parse(ev.data) as {
          id?: number
          result?: unknown
          error?: { message?: string }
        }
        if (msg.id !== id) return
        ws.removeEventListener('message', onMessage)
        if (msg.error) reject(new Error(`e2e: ${method} refused: ${msg.error.message ?? ''}`))
        else resolve(msg.result)
      }
      ws.addEventListener('message', onMessage)
      ws.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }))
    })
  return { call, close: () => ws.close() }
}

/**
 * A UUIDv7, because the layout wire validates the version nibble and the
 * variant and refuses anything else — crypto.randomUUID is a v4.
 *
 * A second implementation of frontend/src/layout/uuid7.ts, and deliberately
 * so: importing renderer source into the harness would pull Solid and the
 * frontend's module graph into Playwright's Node process for four lines of
 * bit-twiddling. This one needs no monotonicity — the ids it mints are for a
 * replacement tab nobody sorts.
 */
function uuidv7(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  const now = Date.now()
  bytes[0] = Math.floor(now / 2 ** 40) & 0xff
  bytes[1] = Math.floor(now / 2 ** 32) & 0xff
  bytes[2] = Math.floor(now / 2 ** 24) & 0xff
  bytes[3] = Math.floor(now / 2 ** 16) & 0xff
  bytes[4] = Math.floor(now / 2 ** 8) & 0xff
  bytes[5] = now & 0xff
  bytes[6] = 0x70 | (bytes[6] & 0x0f)
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

// ── Vault e2e helper: managed devharness lifecycle ───────────────────
//
// VaultBackend wraps a devharness child process so a spec can stop and
// restart the backend with a fresh token (which changes per launch). The
// caller provides the binary path; start() returns the WS port and token.
//
// The XDG dirs passed to the constructor are used for every instance, so
// vault state (DB, sealed vault files) survives restart.
//
// Usage:
//   const backend = new VaultBackend('/tmp/nocx-devharness',
//     { data: '/tmp/vt/data', config: '/tmp/vt/config', cache: '/tmp/vt/cache' })
//   const { port, token } = await backend.start()
//   // … test …
//   const { port: p2, token: t2 } = await backend.restart()

import { spawn, execSync, type ChildProcess } from 'node:child_process'
import { existsSync, readFileSync, openSync, mkdirSync, copyFileSync } from 'node:fs'
import { resolve, basename, join } from 'node:path'

import { createHomeIsolation, type HomeIsolation } from './home-isolation'

/**
 * A disposable directory the caller owns and cleans up. The backend's whole
 * home is placed inside it, so its settings, profiles, vault documents, shell
 * integration and rc files all land there and nowhere else.
 *
 * This replaced an XDG_CONFIG_HOME/DATA/CACHE trio. Two reasons, and the second
 * is why it was worth the churn: the home covers ~/.nocx, the rc files and
 * ~/.ssh/config, which the trio never did — and the trio is Linux-only, because
 * internal/storage's darwin resolver goes straight to os.UserHomeDir() and
 * never looks at XDG. On a Mac the vault specs believed they were isolated and
 * were writing the developer's real Application Support directory.
 */
export interface DisposableRoot {
  root: string
}

export interface BackendEndpoint {
  port: number
  token: string
}

/**
 * Where the backend keeps its documents — settings, profiles, the vault — under
 * a given isolated home.
 *
 * A spec that seeds or reads one of those files has to resolve the same
 * directory internal/storage does, and that directory is NOT the same shape on
 * every platform: internal/storage/paths.go sends darwin to
 * `~/Library/Application Support/<app>` via os.UserHomeDir(), and everything
 * else to os.UserConfigDir(), which with the XDG variables stripped (as the
 * home boundary strips them) is `~/.config/<app>`.
 *
 * connection-password.spec.ts hardcoded the `.config` form with a comment
 * asserting the XDG reasoning, which is true on Linux and false on macOS. The
 * spec passed in the bash container and failed on every Mac: it wrote a profile
 * where nothing read it, the Connections page stayed empty, and the button it
 * waited for never appeared. Exactly the split e2e/harness.ts's DisposableRoot
 * comment already records for the vault specs — the second time this repo has
 * paid for one directory being derived twice.
 *
 * The app name carries the `-dev` suffix because e2e never builds with
 * `-tags release` (internal/storage/appdir_dev.go).
 */
export function documentDir(isolatedHome: string): string {
  const app = 'nocx-dev'
  return process.platform === 'darwin'
    ? join(isolatedHome, 'Library', 'Application Support', app)
    : join(isolatedHome, '.config', app)
}

/**
 * Point the page at a backend THIS SPEC started, by supplying the two wails
 * bindings the frontend reads at startup.
 *
 * Legal only on the headless path, and it refuses everywhere else. Under
 * `wails dev` the HTML wails serves is:
 *
 *   <script src="/wails/ipc.js">                  <- classic
 *   <script src="/wails/runtime.js">              <- classic
 *   <script type="module" src="/@vite/client">
 *   <script type="module" src="/src/main.tsx">
 *
 * Classic scripts run before any deferred module, so wails installs the REAL
 * window.go after Playwright's init script and before main.tsx reads it — and
 * wailsjs/go/main/WailsApp.js resolves window['go'] at CALL time, so by
 * main.tsx:82 the stub is simply gone. The app then connects to the wails
 * backend on 34115, the one every other spec shares.
 *
 * That is not a flake and not a race worth trying to win; it is the documented
 * arrangement (wails discussion #4205: `wails dev` serves the app on 34115
 * "with backend bindings included"). An override there is asking wails not to
 * be wails. So the refusal is the contract, and it is an exception rather than
 * a skip because a spec that quietly measures the wrong backend is the failure
 * this exists to prevent: in CI run 31115207733 seven specs asserted vault
 * setup, imports and password prompts against a backend they had never
 * touched, while the devharness each of them started logged its port and never
 * saw a single client (nocx-w4vy).
 *
 * That refusal used to live here as a thrown error, because the suite had a
 * second arrangement this call was illegal on. It has one now — vite serves
 * the page alone and nothing overwrites the stub — so the branch is gone
 * rather than kept as a condition that can never be true. What remains true is
 * the reason it existed: a spec that quietly measures the wrong backend is a
 * green run about nothing, so a spec using this should assert, after binding,
 * that the page reports the port it meant to reach.
 *
 * The specs that need this need it because they RESTART their backend
 * mid-test — vault surviving a restart is the thing under test — and `wails
 * dev` owns exactly one backend whose lifecycle Playwright cannot touch. The
 * requirement was never "override the bindings"; it was "run headless".
 */
export async function bindEndpoint(page: Page, endpoint: BackendEndpoint): Promise<void> {
  await page.context().addInitScript(
    (opts: { p: number; t: string }) => {
      const w = window as unknown as { go?: Record<string, unknown> }
      w.go = {
        main: {
          WailsApp: {
            GetWSPort: () => Promise.resolve(opts.p),
            GetWSToken: () => Promise.resolve(opts.t),
            CheckForUpdate: () => Promise.resolve(null),
            ReportHealthy: () => Promise.resolve(),
            ApplyUpdate: () => Promise.resolve(),
          },
        },
      }
    },
    { p: endpoint.port, t: endpoint.token },
  )
}

/**
 * What an AI endpoint is created with, through the dialog a person uses.
 *
 * NOT to be confused with `bindEndpoint` above, whatever the two names
 * suggest: that one points the PAGE at a backend's WebSocket, and has
 * nothing to do with the assistant. The collision is worth a sentence
 * because the plan for nocx-rikz5 read one as the other and briefed a
 * `bindEndpoint(page, { baseURL, model, assignRole: false })` that has never
 * existed.
 */
export interface AiEndpointSpec {
  /** The endpoint's name — what the row, the Roles selects and the model
   *  chip's first half all show. */
  name: string
  /** The OpenAI-compatible base URL: FakeOpenAI.baseUrl() in this suite. */
  baseUrl: string
  /** The model ids the endpoint offers, in order. At least one: an endpoint
   *  offering none is a different rung of the ladder ('no-models'). */
  models: string[]
  /** The key typed into the form — minted into the vault, never stored in
   *  the record. */
  key: string
  /** The passphrase to answer the vault setup sheet with, IF this save is
   *  the one that has to create the vault. */
  vaultPassphrase: string
}

/**
 * Create an AI endpoint and STOP THERE — no role assigned, no default
 * chosen.
 *
 * Stopping is the point. Until nocx-rikz5 there was no way to have an
 * endpoint without also naming the model that answers, so every spec that
 * wanted one configured both in a single gesture (agent-ask.spec.ts's
 * assignAnsweringRole, inline). The state BETWEEN the two — a valid key and
 * no model chosen — is the one the readiness ladder exists for, and it is
 * unobservable while the two are one step. `setDefaultModel` below is the
 * second step, deliberately a separate call.
 *
 * Additive: nothing that existed before this behaves differently, because
 * no spec had a shared helper for this at all — endpoints-through-the-form
 * was open-coded in agent-ask.spec.ts and vault-sealed-probe.spec.ts. This
 * is where the third copy would have gone.
 *
 * THE VAULT BRANCH IS READ, NOT ASSUMED. A fresh home has no vault and the
 * key is minted INTO one (design §4.5.3), so the first save stops on the
 * setup sheet and is retried once the vault exists; a later save on the same
 * home just lands. Which of the two happened is decided by polling the two
 * observable states — a sheet on screen, or the dialog gone — never by
 * waiting out a duration.
 */
export async function createAiEndpoint(page: Page, spec: AiEndpointSpec): Promise<void> {
  const dialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
  // The dialog may ALREADY be open: the model chip's endpoints destination
  // opens it (main.tsx onCreateEndpoint → startNewEndpoint), and clicking
  // "+ New endpoint" on top of it would be a second one. One read of a state
  // the caller has already waited for — not a poll for a frame in flight.
  if (!(await dialog.isVisible())) {
    await page.getByRole('button', { name: '+ New endpoint' }).first().click()
  }
  await baseExpect(dialog).toBeVisible({ timeout: 10_000 })
  await dialog.locator('#endpoint-name').fill(spec.name)
  await dialog.locator('#endpoint-base-url').fill(spec.baseUrl)
  await dialog.locator('#endpoint-key').fill(spec.key)
  for (const [i, model] of spec.models.entries()) {
    await dialog.getByRole('button', { name: 'Add model' }).click()
    await dialog.locator(`#endpoint-model-${i}-name`).fill(model)
    // The model field is a SuggestionField, and the form's silent discovery
    // probe (endpoints-section discoverModels) fills it from the endpoint's
    // own /models. With the list open its options COVER the buttons below —
    // "Add model" and "Create Endpoint" both take the click on an <li>
    // instead, which in the container was 51 retries and a timeout rather
    // than a failed assertion.
    //
    // Focus moves to the row's own alias field, which is what a person does
    // next and is what closes the list (suggestion-field onBlur). NOT
    // Escape: that closes the list only while it is EXPANDED, so a model id
    // matching no suggestion would send the key to the native <dialog> and
    // shut the form the caller is still filling in.
    await dialog.locator(`#endpoint-model-${i}-alias`).click()
  }
  await dialog.getByRole('button', { name: 'Create Endpoint', exact: true }).click()

  const setupSheet = page
    .locator('.ui-prompt-overlay')
    .filter({ has: page.locator('#vault-setup-passphrase') })
  await baseExpect
    .poll(
      async () => {
        if (await setupSheet.isVisible()) return 'vault-setup'
        if (!(await dialog.isVisible())) return 'saved'
        return 'pending'
      },
      { timeout: 15_000 },
    )
    .not.toBe('pending')

  if (await setupSheet.isVisible()) {
    await page.locator('#vault-setup-passphrase').fill(spec.vaultPassphrase)
    await page.locator('#vault-setup-confirm').fill(spec.vaultPassphrase)
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()
    // The recovery code, then Done — the sheet's own two steps.
    await baseExpect(page.locator('.ui-vault-code-block-wrap .ui-code-block')).toBeVisible({
      timeout: 10_000,
    })
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()
    await baseExpect(setupSheet).not.toBeVisible({ timeout: 10_000 })
    await baseExpect(dialog).not.toBeVisible({ timeout: 10_000 })
  }

  // The record exists — which is all this helper can honestly claim, and all
  // its callers need before going on. It used to wait for a green "Key
  // saved" on the row and read that as proof the key had landed; a caption
  // is not evidence of a secret, and the owner struck the caption anyway.
  // Whether the KEY landed is proved where it can be: a probe through the
  // fake endpoint, which records the material it was sent.
  await baseExpect(page.locator('.ui-collection-row').filter({ hasText: spec.name })).toBeVisible({
    timeout: 10_000,
  })
}

/**
 * Choose the DEFAULT model on Settings → Roles — the one choice the whole
 * ladder exists to lead a person to (nocx-rikz5).
 *
 * The caller brings the Roles page: in the readiness spec the model chip
 * itself is what opens it, and a helper that navigated would hide exactly
 * the thing under test. It waits for the control, not for a duration.
 *
 * Two selects, endpoint first, because a half pair is never written
 * (roles-section onDefaultEndpointChange).
 *
 * The confirmation is the control's OWN selects, and that is not the test
 * reading back what it typed: every write re-adopts the table the backend
 * returned (roles-section `adopt`), so a select holding the pair is the
 * store's answer, never the draft. The answering row used to carry a green
 * "As default: …" sentence and this waited on that; the owner struck it as
 * a restatement of the control above, and a spec that waits on a line the
 * product no longer has is the defect AGENTS.md names, not a regression to
 * revert.
 */
export async function setDefaultModel(
  page: Page,
  endpointName: string,
  model: string,
): Promise<void> {
  const control = page.locator('.roles-default')
  await baseExpect(control).toBeVisible({ timeout: 10_000 })
  await control.locator('select').first().selectOption({ label: endpointName })
  const modelSelect = control.locator('select').nth(1)
  await baseExpect(modelSelect).toBeEnabled()
  await modelSelect.selectOption({ label: model })
  await baseExpect(control.locator('select').first()).toHaveValue(/.+/, { timeout: 10_000 })
  await baseExpect(modelSelect).toHaveValue(/.+/, { timeout: 10_000 })
  // And the answering role, which has no pair of its own, stops refusing:
  // its warning line is gone because the default now carries it.
  const answering = page.locator('.roles-role').filter({ hasText: 'Answering' })
  await baseExpect(answering.locator('.roles-role__state')).toHaveCount(0, { timeout: 10_000 })
}

export class VaultBackend {
  private proc: ChildProcess | null = null
  private logPath = ''

  /** Where this backend is writing. A spec that wants to read the log asks for
   *  it rather than rebuilding the name from a port it no longer chooses. */
  get logFile(): string {
    if (!this.logPath) throw new Error('backend has not been started yet')
    return this.logPath
  }

  /** The canonical home this backend was given, once it has been started. */
  private isolation: HomeIsolation | null = null

  constructor(
    private readonly binary: string,
    private readonly disposable: DisposableRoot,
    /**
     * Cut the backend off from the session bus, so its system provider probes
     * as unavailable no matter what is running around the test.
     *
     * A case that needs "no OS keychain" cannot get it by assuming: run the
     * suite inside the dbus-run-session the keyring case requires and the
     * passphrase cases fail, because setup silently succeeds and the dialog
     * they wait for never appears. That is a true result reported as the wrong
     * defect. Pointing DBUS_SESSION_BUS_ADDRESS at nothing makes the condition
     * explicit and identical in both environments.
     */
    private readonly withoutSecretService = false,
  ) {
    if (!existsSync(binary)) {
      throw new Error(`devharness binary not found: ${binary}`)
    }
  }

  /** Start devharness on the given port, wait for WSPORT/WSTOKEN. */
  /** Start the backend. The port defaults to 0, which asks the OS for a free
   *  one and reads back what it got — devharness prints WSPORT either way.
   *
   *  It used to be required, and every spec picked a constant by hand. Three
   *  pairs collided: vault-settings and recall-search both claimed 19880,
   *  history-persistence and home-boundary-live both 19878, prompt-vault and
   *  connection-password both 19901. In isolation each passed; in a full run
   *  whichever went second could find the port still held and come up with no
   *  backend at all, which surfaces as "no tab ever appeared" somewhere else
   *  entirely. A port is a shared resource and hand-assignment does not scale
   *  past the first person who forgets to check (nocx-z9s9.11). */
  async start(port = 0): Promise<BackendEndpoint> {
    if (this.proc) throw new Error('backend already running; call stop() first')
    this.logPath = resolve(this.disposable.root, `devharness-${port || 'auto'}.log`)
    const logFd = openSync(this.logPath, 'w')

    const overrideEnv: Record<string, string> = { NOCX_WS_ADDR: `127.0.0.1:${port}` }
    if (this.withoutSecretService) {
      overrideEnv.DBUS_SESSION_BUS_ADDRESS = 'unix:path=/nonexistent/nocx-e2e-no-secret-service'
      // The portable half. The line above is a LINUX mechanism: on macOS
      // go-keyring goes to the Security framework and ignores it entirely, so
      // these cases were not arranging "no keystore" there at all — and with a
      // disposable $HOME the framework found no login keychain under it and put
      // a "Keychain not found" dialog on the developer's screen, once per
      // backend start (nocx-o4hg). Both are set: the env var states the premise
      // on every platform, and the dbus one keeps stating it for anything that
      // reads the bus directly.
      overrideEnv.NOCX_NO_SYSTEM_KEYSTORE = '1'
    }

    // The same boundary the default path gets from playwright.config.ts. Built
    // per start() rather than per instance so a restart re-derives it: if the
    // root were ever swapped underneath, the refusals fire again rather than a
    // stale environment being replayed.
    this.isolation = createHomeIsolation({
      inheritedEnv: process.env,
      overrideEnv,
      root: this.disposable.root,
    })
    const env = this.isolation.env as Record<string, string>

    this.proc = spawn(this.binary, [], { env, stdio: ['ignore', logFd, logFd], detached: false })

    // Wait for WSTOKEN line (printed after WSPORT).
    const timeoutMs = 15_000
    const pollIntervalMs = 200
    const deadline = Date.now() + timeoutMs

    while (Date.now() < deadline) {
      if (!this.proc || (!this.proc.killed && this.proc.exitCode !== null)) {
        const code = this.proc?.exitCode
        const log = readFileSync(this.logPath, 'utf8')
        throw new Error(`devharness exited early (code=${code}):\n${log}`)
      }
      const log = readFileSync(this.logPath, 'utf8')
      const m = log.match(/^WSTOKEN=(.+)$/m)
      if (m) {
        const p = log.match(/^WSPORT=(\d+)$/m)
        return { port: p ? Number(p[1]) : port, token: m[1] }
      }
      const { promise, resolve: later } = Promise.withResolvers<void>()
      setTimeout(later, pollIntervalMs)
      await promise
    }

    throw new Error(`devharness did not print WSTOKEN within ${timeoutMs}ms`)
  }

  /**
   * Copy this backend's log where a failed CI run can actually read it.
   *
   * The log lives beside the disposable root — a mkdtemp nobody keeps and no
   * artifact step collects — so when a spec failed on the runner, the one
   * account of what the backend did was thrown away with the temp directory.
   * Every diagnosis then had to be guessed from the DOM. test-results/ is
   * already uploaded on failure (ci.yml), so that is where it goes.
   *
   * Best-effort by construction: a harness that throws while trying to explain
   * a failure replaces the failure with its own.
   */
  private preserveLog(): void {
    if (!this.logPath) return
    try {
      const dir = resolve(process.cwd(), 'test-results', 'devharness')
      mkdirSync(dir, { recursive: true })
      copyFileSync(this.logPath, resolve(dir, basename(this.logPath)))
    } catch {
      /* the log is a courtesy; never fail a run over it */
    }
  }

  /** The backend's log so far, for a test that wants to say WHY it failed. */
  logTail(maxBytes = 4000): string {
    if (!this.logPath) return '(backend never started)'
    try {
      const all = readFileSync(this.logPath, 'utf8')
      return all.length <= maxBytes ? all : `…${all.slice(-maxBytes)}`
    } catch (err) {
      return `(backend log unreadable: ${String(err)})`
    }
  }

  /** Stop the running devharness. */
  stop(): void {
    this.preserveLog()
    if (!this.proc) return
    const p = this.proc
    this.proc = null
    try {
      p.kill('SIGTERM')
    } catch {
      /* already dead */
    }
    // Give it 2 s to shut down gracefully, then SIGKILL.
    try {
      execSync(`timeout 2 sh -c 'while kill -0 ${p.pid} 2>/dev/null; do sleep 0.1; done'`)
    } catch {
      /* the wait timed out — fall through to SIGKILL */
    }
    try {
      p.kill('SIGKILL')
    } catch {
      /* fine */
    }
  }
  /** Restart with a fresh token. Same port policy as start(): 0 asks the OS. */
  async restart(port = 0): Promise<BackendEndpoint> {
    this.stop()
    // Brief quiescent period so the OS releases the old listen socket.
    const { promise, resolve: wait } = Promise.withResolvers<void>()
    setTimeout(wait, 500)
    await promise
    return this.start(port)
  }

  get running(): boolean {
    return this.proc !== null && this.proc.exitCode === null
  }

  /**
   * The canonical home this backend was launched with, for a spec that wants to
   * assert the backend actually resolved it rather than trust that it was
   * handed over. Throws before the first start(), because there is no honest
   * answer then and returning a guess is how an unchecked boundary starts.
   */
  get isolatedHome(): string {
    if (!this.isolation) throw new Error('backend has not been started yet')
    return this.isolation.isolatedHome
  }
}
