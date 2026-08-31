// @vitest-environment jsdom
import { onCleanup, onMount, type Component } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { Dispatcher, type ConnectionState } from './dispatcher'
import { mountSidebar, type SidebarHandle, type SidebarViewDescriptor } from './sidebar'
import { SettingsIcon } from './ui/icons'

let activeDispatcher: Dispatcher | null = null

const observedView: Component = () => {
  const registration = Symbol('observer')
  let unsubscribe: (() => void) | undefined
  onMount(() => {
    registrations.add(registration)
    unsubscribe = activeDispatcher?.onConnect(() => {
      connectNotifications += 1
    })
  })
  onCleanup(() => {
    registrations.delete(registration)
    unsubscribe?.()
  })
  return document.createElement('div')
}

const view: SidebarViewDescriptor = {
  id: 'observed',
  title: 'Observed',
  icon: SettingsIcon,
  view: observedView,
  order: 0,
}

const registrations = new Set<symbol>()
let connectNotifications = 0

async function flushSolid(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

function emitState(dispatcher: Dispatcher, state: ConnectionState): void {
  const internal = dispatcher as unknown as {
    setConnectionState: (next: ConnectionState) => void
  }
  internal.setConnectionState(state)
}

function fireConnect(dispatcher: Dispatcher): void {
  const internal = dispatcher as unknown as { fireConnect: () => void }
  internal.fireConnect()
}

afterEach(() => {
  activeDispatcher = null
  registrations.clear()
  connectNotifications = 0
  document.body.replaceChildren()
})

describe('composition root disposal contract', () => {
  it('returns Dispatcher subscribers and sidebar observers to baseline across three remounts', async () => {
    const dispatcher = new Dispatcher({
      resolve: () =>
        Promise.resolve({
          ok: false,
          failure: { kind: 'no-server', message: 'missing', remedy: 'retry' },
        }),
    })
    activeDispatcher = dispatcher
    const bar = document.createElement('div')
    const panel = document.createElement('div')
    document.body.append(bar, panel)

    let sidebar: SidebarHandle | null = null
    const mount = () => {
      sidebar = mountSidebar(bar, panel, [view], [])
    }
    dispatcher.onConnectionStateChange((state) => {
      if (state.kind === 'online') {
        if (!sidebar) mount()
        return
      }
      sidebar?.destroy()
      sidebar = null
    })

    const internals = dispatcher as unknown as {
      connectionStateHandlers: Set<unknown>
      connectHandlers: Set<unknown>
    }
    const stateSubscriberBaseline = internals.connectionStateHandlers.size
    const connectSubscriberBaseline = internals.connectHandlers.size
    const observerBaseline = registrations.size

    for (let remount = 0; remount < 3; remount += 1) {
      emitState(dispatcher, { kind: 'online' })
      await flushSolid()
      expect(registrations.size).toBe(observerBaseline + 1)
      expect(internals.connectHandlers.size).toBe(connectSubscriberBaseline + 1)

      const notificationsBefore = connectNotifications
      fireConnect(dispatcher)
      expect(connectNotifications).toBe(notificationsBefore + 1)

      emitState(dispatcher, { kind: 'waiting', backoffMs: 250 })
      await flushSolid()
      expect(registrations.size).toBe(observerBaseline)
      expect(internals.connectHandlers.size).toBe(connectSubscriberBaseline)
      fireConnect(dispatcher)
      expect(connectNotifications).toBe(notificationsBefore + 1)
      expect(internals.connectionStateHandlers.size).toBe(stateSubscriberBaseline)
    }
  })
})
