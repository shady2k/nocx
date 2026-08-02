import { test as base, expect as baseExpect, type Page } from '@playwright/test'

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

// Shared e2e harness. When the suite runs against the headless
// vite + devharness shim (NOCX_WS_PORT set) instead of `wails dev`, inject the
// Wails GetWSPort binding the frontend expects before any app code runs. Under
// `wails dev` the real binding is present and NOCX_WS_PORT is unset, so this is
// a no-op — the same specs run unchanged in CI.
export const test = base.extend({
  page: async ({ page }, use) => {
    const port = process.env.NOCX_WS_PORT
    const token = process.env.NOCX_WS_TOKEN
    if (port) {
      if (!token) {
        throw new Error(
          'NOCX_WS_PORT set but NOCX_WS_TOKEN is missing; ' +
            'the token is the auth gate and an empty string is rejected. ' +
            'Export both or use `wails dev`.',
        )
      }
      await page.addInitScript(
        (opts: { p: string; t: string }) => {
          ;(window as unknown as { go: unknown }).go = {
            main: {
              WailsApp: {
                GetWSPort: () => Promise.resolve(Number(opts.p)),
                GetWSToken: () => Promise.resolve(opts.t),
                CheckForUpdate: () => Promise.resolve(null),
                ReportHealthy: () => Promise.resolve(),
                ApplyUpdate: () => Promise.resolve(),
              },
            },
          }
        },
        { p: port, t: token },
      )
    }
    await use(page)
  },
})

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
//   const { port, token } = await backend.start(firstPort)
//   // … test …
//   const { port: p2, token: t2 } = await backend.restart(secondPort)

import { spawn, execSync, type ChildProcess } from 'node:child_process'
import { existsSync, readFileSync, openSync } from 'node:fs'
import { resolve } from 'node:path'

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

export class VaultBackend {
  private proc: ChildProcess | null = null
  private logPath = ''

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
  async start(port: number): Promise<BackendEndpoint> {
    if (this.proc) throw new Error('backend already running; call stop() first')
    this.logPath = resolve(this.disposable.root, `devharness-${port}.log`)
    const logFd = openSync(this.logPath, 'w')

    const overrideEnv: Record<string, string> = { NOCX_WS_ADDR: `127.0.0.1:${port}` }
    if (this.withoutSecretService) {
      overrideEnv.DBUS_SESSION_BUS_ADDRESS = 'unix:path=/nonexistent/nocx-e2e-no-secret-service'
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

  /** Stop the running devharness. */
  stop(): void {
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
  async restart(port: number): Promise<BackendEndpoint> {
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
