/**
 * Browser path for the Wails v3 runtime (dev-web and the headless e2e suite).
 *
 * The v3 runtime's default transport fetches /wails/runtime, which only the
 * Wails asset server serves — a plain browser has no such endpoint, so every
 * binding call would throw and the app would fall back to "no Wails runtime"
 * even where a shim exists to say otherwise. The two browser entry points
 * (vite.dev-view.config.ts for `make dev-web`, e2e/harness.ts for the suite)
 * inject a `window.go` bridge carrying exactly the four bound methods the
 * frontend reads at startup (ResolveBackend/CheckForUpdate/ReportHealthy/
 * ApplyUpdate). Log is deliberately not among them: in a browser a frontend
 * log line belongs in the browser console, which is where v2 put it for the
 * same reason — see `bindingReachable` below.
 *
 * This adapter installs a custom transport that routes binding calls
 * (object 0 / method 0 of the runtime protocol) into that `window.go` bridge
 * by fully-qualified method name — the bindings are generated with -names for
 * exactly this reason. Anything else (clipboard, environment, …) is rejected
 * the way a missing backend would reject it, so the app's existing fallbacks
 * (navigator.clipboard, user-agent platform) engage unchanged.
 *
 * In the packaged webview `window.go` does not exist — v3 never injects it —
 * so the adapter is a no-op there and the default HTTP transport is used.
 * `window.go` presence is therefore the exact marker of "shimmed browser".
 */
import { setTransport } from '@wailsio/runtime'

type GoBridge = Record<string, Record<string, Record<string, (...args: unknown[]) => unknown>>>

function goBridge(): GoBridge | undefined {
  if (typeof window === 'undefined') return undefined
  return (window as unknown as { go?: GoBridge }).go
}

/** Look one fully-qualified binding name up in the shim bridge. The
 *  bindings are generated with -names, so `main.WailsApp.Log` is the key
 *  both the transport and `bindingReachable` split the same way. */
function bridgeMethod(
  bridge: GoBridge,
  methodName: string,
): ((...args: unknown[]) => unknown) | undefined {
  const [pkg, struct, ...rest] = methodName.split('.')
  return bridge[pkg]?.[struct]?.[rest.join('.')]
}

/**
 * True inside the Wails webview — the one environment where a call to the
 * real runtime reaches the backend, whether it is a generated binding
 * (log.ts, via `bindingReachable` below) or a runtime service (clipboard.ts).
 * This module owns that question for both; there is no second derivation.
 *
 * The probe is the native invoke bridge the v3 runtime itself uses
 * (window.chrome.webview on Windows, window.webkit.messageHandlers.external
 * on macOS and WebKitGTK — runtime_linux.go injects the invoke wrapper over
 * exactly that bridge): it exists only inside the webview, never in a plain
 * browser and never in jsdom.
 */
export function hasWailsWebview(): boolean {
  if (typeof window === 'undefined') return false
  const w = window as unknown as {
    chrome?: { webview?: { postMessage?: unknown } }
    webkit?: { messageHandlers?: { external?: { postMessage?: unknown } } }
  }
  return Boolean(w.chrome?.webview?.postMessage ?? w.webkit?.messageHandlers?.external?.postMessage)
}

/**
 * Whether calling the named binding can actually reach a backend — answered
 * synchronously, from what is on `window`, without making the call.
 *
 * A caller with a local fallback (logging is the one: the console) must be
 * able to choose that fallback in the same tick, not one rejected promise
 * later. Awaiting the rejection is observably different — a console line
 * that arrives a microtask after the event that produced it is not there
 * for the code, the test or the person looking at the moment it mattered.
 *
 * Three environments, and the answer differs in each:
 *   - shimmed browser (`window.go`): only what the shim carries is routable;
 *   - packaged webview: no shim, but the invoke bridge is there;
 *   - plain browser / jsdom: neither, so nothing is reachable.
 */
export function bindingReachable(methodName: string): boolean {
  const bridge = goBridge()
  if (bridge) return typeof bridgeMethod(bridge, methodName) === 'function'
  return hasWailsWebview()
}

export function installBrowserTransport(): void {
  const bridge = goBridge()
  if (!bridge) return
  setTransport({
    // Not `async`: the shim awaits nothing, and the bridge methods already
    // return promises. Rejections are built rather than thrown for the
    // same reason.
    call: (objectID, method, _windowName, args) => {
      if (objectID !== 0 || method !== 0) {
        // Only binding calls ride the shim. Runtime calls (clipboard,
        // system environment, dialogs) fail the way they would with no
        // backend, and the app's fallbacks engage.
        return Promise.reject(new Error('nocx: no Wails runtime for this call in a browser'))
      }
      const opts = args as { methodName?: string; args?: unknown[] }
      const fn = bridgeMethod(bridge, opts.methodName ?? '')
      if (typeof fn !== 'function') {
        return Promise.reject(new ReferenceError(`nocx: unknown binding ${opts.methodName}`))
      }
      return Promise.resolve(fn(...(opts.args ?? [])))
    },
  })
}
