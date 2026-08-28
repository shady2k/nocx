// The renderer half of the CLIENT HOST (nocx-uo1k6, design D3).
//
// The coordinator is a daemon with no window: a file picker, a browser open,
// a desktop banner, a dock badge and a window raise are things only this
// client can do. So the coordinator asks (host.request) and this module
// answers (host.resolved), performing the effect through the Wails bindings
// on the way.
//
// THE RENDERER DECIDES NOTHING (AD-3). Whether the URL may be opened, whether
// a second picker may stack, which pane a click focuses -- all of it is
// settled on the coordinator's side before the ask arrives. This module
// performs and reports; the one judgement it makes is "this client has no
// native host at all", which is a fact about the environment, not a policy.
//
// Every path answers. A request dropped silently is a coordinator task
// waiting on a person who was never asked, so an unknown capability, a
// missing runtime and a thrown binding all resolve -- failed, with a
// sentence.

import { Events } from '@wailsio/runtime'
import {
  HostBadge,
  HostBanner,
  HostBounce,
  HostFocusWindow,
  HostOpenDirectory,
  HostOpenFile,
  HostOpenUrl,
} from '../bindings/github.com/shady2k/nocx/wailsapp'
import { bindingReachable } from './wails-runtime'
import type { Dispatcher } from './dispatcher'
import type { HostAttentionActivated } from './generated/host.attentionActivated'
import type { HostRequest } from './generated/host.request'
import type { HostResolved } from './generated/host.resolved'

/** The native effects this client can perform, as a seam. The default binds
 *  the generated Wails bindings; a test binds doubles and needs no runtime. */
export interface HostBindings {
  openFile(): Promise<string>
  openDirectory(): Promise<string>
  openUrl(url: string): Promise<void>
  banner(title: string, body: string, sessionId: string): Promise<void>
  badge(count: number): Promise<void>
  bounce(): Promise<void>
  focusWindow(): Promise<void>
}

/** The one binding name the reachability probe is asked about. All seven live
 *  on the same bound struct, so one answer covers the set: either this client
 *  is inside a Wails webview (or a shim that carries them) or it is not. */
const HOST_BINDING = 'main.WailsApp.HostFocusWindow'

/** The Wails event the shell emits when a person clicks a banner it
 *  presented. It carries the session id the banner was about. */
export const ATTENTION_ACTIVATED_EVENT = 'nocx:attentionActivated'

/** The Wails event seam. Narrow on purpose: this module needs exactly one
 *  subscription, and depending on the whole runtime would make it untestable
 *  outside a webview. */
export interface HostEvents {
  on(name: string, handler: (data: unknown) => void): () => void
}

const wailsBindings: HostBindings = {
  openFile: () => HostOpenFile(),
  openDirectory: () => HostOpenDirectory(),
  openUrl: (url) => HostOpenUrl(url),
  banner: (title, body, sessionId) => HostBanner(title, body, sessionId),
  badge: (count) => HostBadge(count),
  bounce: () => HostBounce(),
  focusWindow: () => HostFocusWindow(),
}

const wailsEvents: HostEvents = {
  on: (name, handler) => Events.On(name, (ev) => handler(ev)),
}

/**
 * Mount the client-host handler on the app's dispatcher. Returns the
 * unsubscribe function.
 *
 * bindings and events default to the real Wails runtime; both are injected so
 * the exchange can be exercised without one. A client with no reachable
 * bindings still mounts and still answers -- failed, saying so -- because the
 * coordinator must never be left waiting on a client that cannot act.
 */
export function mountClientHost(
  dispatcher: Dispatcher,
  bindings: HostBindings = wailsBindings,
  events: HostEvents = wailsEvents,
): () => void {
  const unsubscribeRequests = dispatcher.subscribe('host.request', (params) => {
    const p = params as HostRequest
    if (!p || !p.requestId || !p.capability) return
    void answer(dispatcher, bindings, p)
  })
  const unsubscribeEvents = events.on(ATTENTION_ACTIVATED_EVENT, (data) => {
    // The click half: the shell tells this renderer that a banner it
    // presented was activated, and the renderer tells the coordinator.
    // Nothing is done about it here -- where the focus lands is the
    // coordinator's, because only it knows which connection holds the
    // session.
    const sessionId = activatedSessionId(data)
    if (!sessionId) return
    const activated: HostAttentionActivated = { sessionId }
    dispatcher.notify('host.attentionActivated', activated)
  })
  return () => {
    unsubscribeRequests()
    unsubscribeEvents()
  }
}

/** The Wails event payload is whatever the shell emitted. v3 wraps it in a
 *  WailsEvent whose `data` is the emitted value -- and a single emitted value
 *  may arrive either bare or as a one-element array, so both are read rather
 *  than one being assumed. Anything else is ignored: a click that cannot name
 *  a session is not a click this can honour. */
function activatedSessionId(data: unknown): string {
  const payload = (data as { data?: unknown } | null)?.data ?? data
  if (typeof payload === 'string') return payload
  if (Array.isArray(payload) && typeof payload[0] === 'string') return payload[0]
  return ''
}

/** What one capability produced: a picker's chosen path, a dismissal, or
 *  nothing at all for an effect that has no result. A typed outcome rather
 *  than a magic string, so 'no path' and 'the person cancelled' cannot be
 *  confused for one another by an empty value. */
interface Performed {
  path: string
  cancelled: boolean
}

async function answer(
  dispatcher: Dispatcher,
  bindings: HostBindings,
  p: HostRequest,
): Promise<void> {
  if (!bindingReachable(HOST_BINDING)) {
    // A plain browser, the dev-web harness, the headless suite: there is no
    // shell here to open a picker or raise a banner. Said once, honestly, so
    // the coordinator answers its caller rather than waiting on a client that
    // will never act.
    resolve(dispatcher, {
      requestId: p.requestId,
      outcome: 'failed',
      error: 'this client has no native host',
    })
    return
  }
  try {
    const done = await perform(bindings, p)
    if (done.cancelled) {
      resolve(dispatcher, { requestId: p.requestId, outcome: 'cancelled' })
      return
    }
    resolve(
      dispatcher,
      done.path
        ? { requestId: p.requestId, outcome: 'ok', path: done.path }
        : { requestId: p.requestId, outcome: 'ok' },
    )
  } catch (err) {
    const reason = err instanceof Error ? err.message : String(err)
    resolve(dispatcher, {
      requestId: p.requestId,
      outcome: 'failed',
      error: reason || 'the native host failed',
    })
  }
}

/** Perform one capability and say what it produced. */
async function perform(bindings: HostBindings, p: HostRequest): Promise<Performed> {
  switch (p.capability) {
    case 'dialog.file':
      return picked(await bindings.openFile())
    case 'dialog.directory':
      return picked(await bindings.openDirectory())
    case 'shell.openUrl':
      await bindings.openUrl(p.url ?? '')
      return done
    case 'attention.banner':
      await bindings.banner(p.title ?? '', p.body ?? '', p.sessionId ?? '')
      return done
    case 'attention.badge':
      await bindings.badge(p.count ?? 0)
      return done
    case 'attention.bounce':
      await bindings.bounce()
      return done
    case 'window.focus':
      await bindings.focusWindow()
      return done
    default:
      // A capability this client does not know. The vocabulary is the
      // server's and closed, so this is a version skew -- answered, never
      // dropped.
      throw new Error(`unknown host capability: ${String(p.capability)}`)
  }
}

/** The effect happened and produced nothing to report. */
const done: Performed = { path: '', cancelled: false }

/** An empty path from a picker is a dismissal, which is the contract the
 *  Wails open dialog has always had. */
function picked(path: string): Performed {
  return path === '' ? { path: '', cancelled: true } : { path, cancelled: false }
}

function resolve(dispatcher: Dispatcher, params: HostResolved): void {
  dispatcher.call('host.resolved', params).catch(() => {
    // The broker refused the resolution -- a stale request id, because the
    // ask was dropped or its client died while the effect was in flight.
    // That is the server's honest answer to a request that is gone; nothing
    // to do here but stop.
    console.warn('nocx: host resolution refused (stale request?)')
  })
}
