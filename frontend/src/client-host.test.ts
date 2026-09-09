// The renderer half of the client host (nocx-uo1k6, design D3): a request
// arrives on the dispatcher, this client performs the native effect through
// its Wails bindings, and the resolution is sent. Asserted from the renderer
// side, per capability, with the failure of every binding paired to its
// success — a request that is dropped silently is a coordinator task waiting
// on a person who was never asked.

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mountClientHost, ATTENTION_ACTIVATED_EVENT } from './client-host'
import type { ApprovalSurface, HostBindings, HostEvents } from './client-host'
import type { Dispatcher } from './dispatcher'
import type { HostResolved } from './generated/host.resolved'

// The reachability probe answers "is there a native host in this client at
// all". It is mocked because jsdom is a plain browser — no shim bridge, no
// webview — and the honest answer there is no, which is its own test below.
const reachable = vi.hoisted(() => ({ value: true }))
vi.mock('./wails-runtime', () => ({
  bindingReachable: () => reachable.value,
}))

interface ScriptedDispatcher {
  subscribe: (method: string, h: (params: unknown) => void) => () => void
  call: ReturnType<typeof vi.fn>
  notify: ReturnType<typeof vi.fn>
  calls: { method: string; params: unknown }[]
  notifications: { method: string; params: unknown }[]
  handlers: Map<string, (params: unknown) => void>
}

function scriptedDispatcher(callResult: () => Promise<unknown> = () => Promise.resolve({})) {
  const handlers = new Map<string, (params: unknown) => void>()
  const calls: { method: string; params: unknown }[] = []
  const notifications: { method: string; params: unknown }[] = []
  const subscribe = (method: string, h: (params: unknown) => void) => {
    handlers.set(method, h)
    return () => {
      handlers.delete(method)
    }
  }
  const call = vi.fn((method: string, params: unknown) => {
    calls.push({ method, params })
    return callResult()
  })
  const notify = vi.fn((method: string, params: unknown) => {
    notifications.push({ method, params })
  })
  return { subscribe, call, notify, calls, notifications, handlers }
}

function scriptedBindings(overrides: Partial<HostBindings> = {}): HostBindings & {
  seen: string[]
} {
  const seen: string[] = []
  return {
    seen,
    openFile: vi.fn(() => {
      seen.push('openFile')
      return Promise.resolve('/home/dev/key')
    }),
    openDirectory: vi.fn(() => {
      seen.push('openDirectory')
      return Promise.resolve('/home/dev/projects')
    }),
    openUrl: vi.fn((url: string) => {
      seen.push(`openUrl:${url}`)
      return Promise.resolve()
    }),
    banner: vi.fn((title: string, body: string, sessionId: string) => {
      seen.push(`banner:${title}|${body}|${sessionId}`)
      return Promise.resolve()
    }),
    badge: vi.fn((count: number) => {
      seen.push(`badge:${count}`)
      return Promise.resolve()
    }),
    bounce: vi.fn(() => {
      seen.push('bounce')
      return Promise.resolve()
    }),
    focusWindow: vi.fn(() => {
      seen.push('focusWindow')
      return Promise.resolve()
    }),
    ...overrides,
  }
}

/** An events source a test drives by hand. */
function scriptedEvents() {
  let handler: ((data: unknown) => void) | undefined
  const source: HostEvents = {
    on: (_name, h) => {
      handler = h
      return () => {
        handler = undefined
      }
    },
  }
  return { source, emit: (data: unknown) => handler?.(data) }
}

function mount(
  d: ScriptedDispatcher,
  bindings: HostBindings,
  events: HostEvents = { on: () => () => {} },
  approveAgent?: ApprovalSurface,
) {
  return mountClientHost(d as unknown as Dispatcher, bindings, events, approveAgent)
}

/** An approval surface a test drives by hand. It is NOT one of the bindings:
 *  the prompt it raises is the renderer's own, which is the whole point of
 *  nocx-qlp9w. */
function scriptedApproval(answer = true) {
  const seen: string[] = []
  const surface: ApprovalSurface = (executable, scope) => {
    seen.push(`approveAgent:${executable}|${scope}`)
    return Promise.resolve(answer)
  }
  return { surface, seen }
}

/** Deliver one host.request and let the async answer settle. */
async function request(d: ScriptedDispatcher, params: Record<string, unknown>): Promise<void> {
  d.handlers.get('host.request')!(params)
  await vi.waitFor(() => expect(d.calls.length).toBeGreaterThan(0))
}

function lastResolution(d: ScriptedDispatcher): HostResolved {
  const last = d.calls[d.calls.length - 1]
  expect(last.method).toBe('host.resolved')
  return last.params as HostResolved
}

beforeEach(() => {
  reachable.value = true
})

describe('mountClientHost — the renderer performs what the coordinator asks', () => {
  it('opens the file picker and answers with the chosen path', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r1', capability: 'dialog.file' })
    expect(b.seen).toEqual(['openFile'])
    expect(lastResolution(d)).toEqual({ requestId: 'r1', outcome: 'ok', path: '/home/dev/key' })
  })

  it('opens the directory picker and answers with the chosen path', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r2', capability: 'dialog.directory' })
    expect(b.seen).toEqual(['openDirectory'])
    expect(lastResolution(d)).toEqual({
      requestId: 'r2',
      outcome: 'ok',
      path: '/home/dev/projects',
    })
  })

  it('opens a URL and answers ok, carrying no path', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r3', capability: 'shell.openUrl', url: 'https://example.com/x' })
    expect(b.seen).toEqual(['openUrl:https://example.com/x'])
    expect(lastResolution(d)).toEqual({ requestId: 'r3', outcome: 'ok' })
  })

  it('presents a banner with the title, body and session the coordinator sent', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, {
      requestId: 'r4',
      capability: 'attention.banner',
      title: 'done',
      body: 'the build finished',
      sessionId: 's-1',
    })
    expect(b.seen).toEqual(['banner:done|the build finished|s-1'])
    expect(lastResolution(d)).toEqual({ requestId: 'r4', outcome: 'ok' })
  })

  it('sets the badge and requests the bounce', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r5', capability: 'attention.badge', count: 0 })
    expect(b.seen).toEqual(['badge:0'])
    d.calls.length = 0
    await request(d, { requestId: 'r6', capability: 'attention.bounce' })
    expect(b.seen).toEqual(['badge:0', 'bounce'])
  })

  it('raises the window when the coordinator asks', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r7', capability: 'window.focus' })
    expect(b.seen).toEqual(['focusWindow'])
    expect(lastResolution(d)).toEqual({ requestId: 'r7', outcome: 'ok' })
  })

  it('asks the person to admit the executable tree and reports the answer', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    const a = scriptedApproval()
    mount(d, b, undefined, a.surface)
    await request(d, {
      requestId: 'r-approval',
      capability: 'agent.approval',
      executable: '/usr/local/bin/agent',
      scope: 'tool-endpoint:workspace-1',
    })
    expect(a.seen).toEqual(['approveAgent:/usr/local/bin/agent|tool-endpoint:workspace-1'])
    expect(b.seen).toEqual([])
    expect(lastResolution(d)).toEqual({ requestId: 'r-approval', outcome: 'ok', approved: true })
  })

  it('reports a dismissed picker as cancelled, not as a failure', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings({ openFile: vi.fn(() => Promise.resolve('')) })
    mount(d, b)
    await request(d, { requestId: 'r8', capability: 'dialog.file' })
    expect(lastResolution(d)).toEqual({ requestId: 'r8', outcome: 'cancelled' })
  })

  // The failure path of every binding, paired with its success above: the
  // platform said no, and the coordinator hears why instead of waiting.
  it.each([
    ['dialog.file', 'openFile'],
    ['dialog.directory', 'openDirectory'],
    ['shell.openUrl', 'openUrl'],
    ['attention.banner', 'banner'],
    ['attention.badge', 'badge'],
    ['attention.bounce', 'bounce'],
    ['window.focus', 'focusWindow'],
  ])('answers failed when the %s binding rejects', async (capability, method) => {
    const d = scriptedDispatcher()
    const b = scriptedBindings({
      [method]: vi.fn(() => Promise.reject(new Error('no D-Bus session'))),
    })
    mount(d, b)
    await request(d, { requestId: 'r9', capability })
    expect(lastResolution(d)).toEqual({
      requestId: 'r9',
      outcome: 'failed',
      error: 'no D-Bus session',
    })
  })

  it('answers failed for a capability it does not know, rather than dropping it', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r10', capability: 'something.new' })
    const res = lastResolution(d)
    expect(res.outcome).toBe('failed')
    expect(res.error).toContain('something.new')
    expect(b.seen).toEqual([])
  })

  // The dev-web harness, the headless suite, a plain browser: there is no
  // shell here. Said once, honestly, rather than leaving the coordinator
  // waiting on a client that will never act.
  // Absence, not failure, and the difference is visible in the product. A
  // plain browser has no OS banner and never will, so the coordinator has
  // nobody who can present one — the same fact as no client attached at all.
  // Answering `failed` here made every notification the coordinator routed to
  // the banner channel land a "Not delivered" row in the notification centre
  // (nocx-bu8fl), because notify's one exemption from the failure feed is
  // written against absence.
  it('answers unavailable when this client has no native host at all', async () => {
    reachable.value = false
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r11', capability: 'dialog.file' })
    expect(b.seen).toEqual([])
    expect(lastResolution(d)).toEqual({
      requestId: 'r11',
      outcome: 'unavailable',
      error: 'this client has no native host',
    })
  })

  // And every capability, not just the picker: the banner is the one the
  // regression was found on, and a per-capability branch is exactly what must
  // not exist here.
  it.each([
    'dialog.file',
    'dialog.directory',
    'shell.openUrl',
    'attention.banner',
    'attention.badge',
    'attention.bounce',
    'window.focus',
  ] as const)('answers unavailable for %s with no native host', async (capability) => {
    reachable.value = false
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    mount(d, b)
    await request(d, { requestId: 'r-na', capability })
    expect(b.seen).toEqual([])
    expect(lastResolution(d).outcome).toBe('unavailable')
  })

  // ...and the one capability that is NOT native (nocx-qlp9w). The list above
  // is the whole native vocabulary — a picker, a URL, a banner, a badge, a
  // bounce, a window raise — and agent.approval is not in it: what it needs is
  // agent-approval-prompt.tsx, a prompt of this renderer's own, which a plain
  // browser draws as well as a webview does. Refusing it for want of a Wails
  // binding is nocx-bu8fl's wrong equation a second time: no native bindings
  // == cannot act. It could act; it just could not act NATIVELY, and this
  // capability never asked it to.
  it('answers agent.approval from its own prompt when there is no native host at all', async () => {
    reachable.value = false
    const d = scriptedDispatcher()
    const b = scriptedBindings()
    const a = scriptedApproval()
    mount(d, b, undefined, a.surface)
    await request(d, {
      requestId: 'r-na-approval',
      capability: 'agent.approval',
      executable: '/usr/local/bin/claude',
      scope: 'tool-endpoint:workspace-1',
    })
    expect(a.seen).toEqual(['approveAgent:/usr/local/bin/claude|tool-endpoint:workspace-1'])
    expect(lastResolution(d)).toEqual({
      requestId: 'r-na-approval',
      outcome: 'ok',
      approved: true,
    })
  })

  // The person said no, and a no is an ANSWER: ok with approved false, never
  // cancelled and never failed. The endpoint stores the refusal; a client that
  // reported it as a failure would leave the question askable again.
  it('carries a refusal back as an answer, not as a failure', async () => {
    reachable.value = false
    const d = scriptedDispatcher()
    const a = scriptedApproval(false)
    mount(d, scriptedBindings(), undefined, a.surface)
    await request(d, { requestId: 'r-no', capability: 'agent.approval' })
    expect(lastResolution(d)).toEqual({ requestId: 'r-no', outcome: 'ok', approved: false })
  })

  // A client that mounted no prompt cannot ask anybody, and says exactly that.
  // UNAVAILABLE, for nocx-bu8fl's reason: nothing was attempted, so this is
  // absence and not a lost effect. It replaces a default binding that rejected
  // — which answered `failed` for a question that was never put to a person,
  // and, because main() mounted the host twice, was the answer that WON
  // (nocx-pighx).
  it('answers unavailable when no approval surface is mounted, native host or not', async () => {
    for (const native of [true, false]) {
      reachable.value = native
      const d = scriptedDispatcher()
      mount(d, scriptedBindings())
      await request(d, { requestId: 'r-nosurface', capability: 'agent.approval' })
      expect(lastResolution(d)).toEqual({
        requestId: 'r-nosurface',
        outcome: 'unavailable',
        error: 'this client has no agent approval surface',
      })
    }
  })

  // The true positive the fix must not remove: a client that HAS a native
  // host and whose binding throws did attempt the effect and lose it, so it
  // still answers failed and still earns its "Not delivered" row.
  it('still answers failed when a reachable binding throws', async () => {
    const d = scriptedDispatcher()
    const b = scriptedBindings({
      banner: () => Promise.reject(new Error('notification permission denied')),
    })
    mount(d, b)
    await request(d, { requestId: 'r11b', capability: 'attention.banner' })
    expect(lastResolution(d)).toEqual({
      requestId: 'r11b',
      outcome: 'failed',
      error: 'notification permission denied',
    })
  })

  it('ignores a request with no id or no capability', () => {
    const d = scriptedDispatcher()
    mount(d, scriptedBindings())
    d.handlers.get('host.request')!({ capability: 'dialog.file' })
    d.handlers.get('host.request')!({ requestId: 'r12' })
    expect(d.calls).toEqual([])
  })

  // A stale resolution is the server's honest answer to an ask that is gone;
  // it must not become an unhandled rejection in the renderer.
  it('survives a refused resolution', async () => {
    const d = scriptedDispatcher(() => Promise.reject(new Error('Unknown request id')))
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    mount(d, scriptedBindings())
    await request(d, { requestId: 'r13', capability: 'window.focus' })
    await vi.waitFor(() => expect(warn).toHaveBeenCalled())
    warn.mockRestore()
  })

  it('stops answering after the returned unsubscribe runs', () => {
    const d = scriptedDispatcher()
    const dispose = mount(d, scriptedBindings())
    dispose()
    expect(d.handlers.has('host.request')).toBe(false)
  })
})

describe('mountClientHost — the click half', () => {
  it('forwards a banner activation to the coordinator and decides nothing', () => {
    const d = scriptedDispatcher()
    const events = scriptedEvents()
    const b = scriptedBindings()
    mount(d, b, events.source)

    events.emit({ name: ATTENTION_ACTIVATED_EVENT, data: 's-9' })
    expect(d.notifications).toEqual([
      { method: 'host.attentionActivated', params: { sessionId: 's-9' } },
    ])
    // Nothing local happened: where the focus lands is the coordinator's.
    expect(b.seen).toEqual([])
  })

  it('reads the session id whether the payload arrives bare or wrapped', () => {
    const d = scriptedDispatcher()
    const events = scriptedEvents()
    mount(d, scriptedBindings(), events.source)

    events.emit({ data: ['s-1'] })
    events.emit('s-2')
    expect(d.notifications.map((n) => (n.params as { sessionId: string }).sessionId)).toEqual([
      's-1',
      's-2',
    ])
  })

  it('ignores an activation that names no session', () => {
    const d = scriptedDispatcher()
    const events = scriptedEvents()
    mount(d, scriptedBindings(), events.source)

    events.emit({ data: 42 })
    events.emit({})
    expect(d.notifications).toEqual([])
  })
})
