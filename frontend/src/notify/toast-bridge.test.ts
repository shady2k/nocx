// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { subscribeNotifyToast, toastMessage } from './toast-bridge'
import { clearToasts, toasts } from '../ui/toast'
import type { NotifyToast } from '../generated/notify.toast'

/** A dispatcher that only records subscriptions and replays into them — the
 *  same fake shape feed-store.test.ts uses, for the same reason. */
function fakeDispatcher() {
  const handlers = new Map<string, Set<(p: unknown) => void>>()
  return {
    subscribe(method: string, h: (p: unknown) => void) {
      let s = handlers.get(method)
      if (!s) handlers.set(method, (s = new Set()))
      s.add(h)
      return () => {
        s.delete(h)
      }
    },
    /** Push what the backend sends. Typed with the GENERATED declaration:
     *  a hand-written fixture can want a field the wire does not carry,
     *  which is exactly how vault.status shipped one nobody sent. */
    emit(push: NotifyToast) {
      handlers.get('notify.toast')?.forEach((h) => h(push))
    },
    /** Anything at all, for the malformed cases the wire can still deliver. */
    emitRaw(params: unknown) {
      handlers.get('notify.toast')?.forEach((h) => h(params))
    },
    subscriberCount() {
      return handlers.get('notify.toast')?.size ?? 0
    },
  }
}

describe('toastMessage', () => {
  it('presents the body alone when there is no title (OSC 9 carries one field)', () => {
    expect(toastMessage('', 'build finished')).toBe('build finished')
  })

  it('presents the title alone when there is no body', () => {
    expect(toastMessage('deploy', '')).toBe('deploy')
  })

  it('joins both when there are two things to say', () => {
    expect(toastMessage('deploy failed', 'exit status 1')).toBe('deploy failed — exit status 1')
  })

  it('is empty when both are', () => {
    expect(toastMessage('', '')).toBe('')
  })
})

describe('the toast sink in the renderer', () => {
  beforeEach(() => {
    clearToasts()
    vi.clearAllMocks()
  })

  it('presents a pushed notification with the kit toast', () => {
    const d = fakeDispatcher()
    subscribeNotifyToast(d)

    d.emit({ title: 'deploy failed', body: 'exit status 1', level: 'warning' })

    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].message).toBe('deploy failed — exit status 1')
    expect(toasts()[0].level).toBe('warning')
  })

  it('carries every level the contract declares', () => {
    const d = fakeDispatcher()
    subscribeNotifyToast(d)

    for (const level of ['info', 'success', 'warning', 'danger'] as const) {
      d.emit({ title: level, body: '', level })
    }

    expect(toasts().map((t) => t.level)).toEqual(['info', 'success', 'warning', 'danger'])
  })

  // A level this build does not know means the wire and the renderer disagree
  // about a closed enum. Presenting the message without claiming a severity
  // nobody declared beats dropping a notification the backend accepted.
  it('presents an unknown level as info rather than dropping the notification', () => {
    const d = fakeDispatcher()
    subscribeNotifyToast(d)

    d.emitRaw({ title: 'from the future', body: '', level: 'catastrophe' })

    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].level).toBe('info')
  })

  // Server-initiated and unsolicited: nothing correlated it and nothing
  // checked its shape at a call site, so a malformed push must be ignored
  // rather than presented as a blank overlay or thrown out of the handler.
  it.each([
    ['null', null],
    ['not an object', 'notify'],
    ['no fields', {}],
    ['a non-string title', { title: 7, body: 'x', level: 'info' }],
    ['a non-string body', { title: 'x', body: 7, level: 'info' }],
    ['nothing to present', { title: '', body: '', level: 'info' }],
  ])('presents nothing for %s', (_name, params) => {
    const d = fakeDispatcher()
    subscribeNotifyToast(d)

    d.emitRaw(params)

    expect(toasts()).toHaveLength(0)
  })

  it('stops presenting once unsubscribed', () => {
    const d = fakeDispatcher()
    const unsubscribe = subscribeNotifyToast(d)
    unsubscribe()

    d.emit({ title: 'after', body: '', level: 'info' })

    expect(d.subscriberCount()).toBe(0)
    expect(toasts()).toHaveLength(0)
  })
})
