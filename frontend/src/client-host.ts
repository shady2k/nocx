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

/** The prompt that asks a person to admit an executable and the tree it
 *  launches, and returns their answer.
 *
 *  NOT one of the bindings above, and that is the whole distinction this seam
 *  exists to keep. Every binding is an effect only the native shell can
 *  perform — a picker, a banner, a badge, a window raise — so a client without
 *  a webview honestly has none of them. This is a prompt the RENDERER draws
 *  (agent-approval-prompt.tsx), which a plain browser draws exactly as well.
 *  Modelling it as a binding gave it a default that always rejected, and a
 *  browser-hosted client answered `unavailable` for it along with the six real
 *  ones — so the whole external-coordinator feature was unreachable outside a
 *  Wails build (nocx-qlp9w), and the always-rejecting default won the race the
 *  double mount created (nocx-pighx). */
export type ApprovalSurface = (facts: ApprovalFacts) => Promise<boolean>

/** What the person is being asked to admit, one fact per member. The wire
 *  carries three because the surface words them: a path and a digest glued
 *  into one string cannot be given a row each, and a durable scope key
 *  ("tool-endpoint:workspace:default") is an identifier this renderer must
 *  not parse to find a word for a person (nocx-fu18z). */
export interface ApprovalFacts {
  /** The agent's absolute path. */
  executable: string
  /** SHA-256 of that file's bytes. */
  digest: string
  /** The workspace the answer covers, by name. */
  workspace: string
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
 * bindings still mounts and still answers — failed, saying so — because the
 * coordinator must never be left waiting on a client that cannot act.
 */
export function mountClientHost(
  dispatcher: Dispatcher,
  bindings: HostBindings = wailsBindings,
  events: HostEvents = wailsEvents,
  approveAgent?: ApprovalSurface,
): () => void {
  const unsubscribeRequests = dispatcher.subscribe('host.request', (params) => {
    const p = params as HostRequest
    if (!p || !p.requestId || !p.capability) return
    void answer(dispatcher, bindings, approveAgent, p)
  })
  const unsubscribeEvents = events.on(ATTENTION_ACTIVATED_EVENT, (data) => {
    // The click half: the shell tells this renderer that a banner it
    // presented was activated, and the renderer tells the coordinator.
    // Nothing is done about it here — where the focus lands is the
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
  approved: boolean
}

async function answer(
  dispatcher: Dispatcher,
  bindings: HostBindings,
  approveAgent: ApprovalSurface | undefined,
  p: HostRequest,
): Promise<void> {
  // Can THIS CLIENT perform THIS capability — not "is this client native".
  // The six below are native effects and nothing else can produce them; the
  // seventh is a prompt this renderer draws, so what it needs is a mounted
  // surface and never a webview. One question, answered per capability,
  // because the two capabilities have genuinely different requirements
  // (nocx-qlp9w) — this is not a special case bolted onto a uniform rule.
  const unavailable =
    p.capability === 'agent.approval'
      ? approveAgent === undefined && 'this client has no agent approval surface'
      : !bindingReachable(HOST_BINDING) && 'this client has no native host'
  if (unavailable) {
    // A plain browser, the dev-web harness, the headless suite: there is no
    // shell here to open a picker or raise a banner. Said once, honestly, so
    // the coordinator answers its caller rather than waiting on a client that
    // will never act.
    //
    // UNAVAILABLE, NOT FAILED, and the two are different facts rather than
    // two words for one (nocx-bu8fl). `failed` is an effect that was
    // ATTEMPTED and did not happen — a denied permission, a thrown binding —
    // and the coordinator is right to remember it: a notification that was
    // accepted and never arrived earns a "Not delivered" row in the
    // notification centre. This client was never able to, and never will be,
    // which is the same fact as no client attached at all; the coordinator
    // maps it to its own no-UI-host answer, which notify exempts from that
    // feed because a channel that does not exist is not a channel that lost
    // a message. Answering `failed` here put a "Not delivered" row behind
    // every banner-routed notification in every browser-hosted client.
    resolve(dispatcher, { requestId: p.requestId, outcome: 'unavailable', error: unavailable })
    return
  }
  try {
    const done = await perform(bindings, approveAgent, p)
    if (done.cancelled) {
      resolve(dispatcher, { requestId: p.requestId, outcome: 'cancelled' })
      return
    }
    resolve(
      dispatcher,
      done.path
        ? { requestId: p.requestId, outcome: 'ok', path: done.path }
        : p.capability === 'agent.approval'
          ? { requestId: p.requestId, outcome: 'ok', approved: done.approved }
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
async function perform(
  bindings: HostBindings,
  approveAgent: ApprovalSurface | undefined,
  p: HostRequest,
): Promise<Performed> {
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
    case 'agent.approval':
      return {
        path: '',
        cancelled: false,
        // Non-null: answer() has already refused this capability when no
        // surface is mounted, which is the only way it can be absent here.
        approved: await approveAgent!({
          executable: p.executable ?? '',
          digest: p.digest ?? '',
          workspace: p.workspace ?? '',
        }),
      }
    default:
      // A capability this client does not know. The vocabulary is the
      // server's and closed, so this is a version skew — answered, never
      // dropped.
      throw new Error(`unknown host capability: ${String(p.capability)}`)
  }
}

/** The effect happened and produced nothing to report. */
const done: Performed = { path: '', cancelled: false, approved: false }

/** An empty path from a picker is a dismissal, which is the contract the
 *  Wails open dialog has always had. */
function picked(path: string): Performed {
  return path === ''
    ? { path: '', cancelled: true, approved: false }
    : { path, cancelled: false, approved: false }
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
