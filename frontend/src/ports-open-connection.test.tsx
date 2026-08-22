// @vitest-environment jsdom
// W2 — "Open as connection" end to end (AGENTS.md rule 1): the panel's
// unavailable-host empty state offers the action, and activating it runs the
// REAL PaneManager path the composition root wires — adoptAliasProfile →
// profiles.create (the backend mints the id) → a NEW tab on the saved
// profile, so the user lands on a tab where Ports works and Forward exists.
// The failure path is the paired test (rule 3): profile creation rejects and
// the user is told, on screen.
//
// The PaneManager here is constructed with a profileClient whose
// createProfile the test controls — the fixture's stub only knows
// listProfiles/listGroups, so the manager is built directly rather than via
// mountPaneManager.
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
// test-support must initialize before the static './tabs' import: its
// createRendererMock backs the hoisted xterm mock, and tabs transitively
// loads terminal-content, which imports the mocked xterm module.
import {
  createRendererMock,
  makeBanner,
  makeClient,
  makeClipboard,
  setupTabBarDOM,
  makeLayoutStore,
  makeUIStateBackend,
} from './test-support/panes-fixtures'
import { PaneManager } from './panes'
import { HorizontalTabStrip } from './tab-strip'
import { ClipboardGate } from './clipboard'
import type { ProfileClient } from './profiles'
import {
  PortsPanel,
  createPortsFilterControl,
  createPortsPauseControl,
  type PortsPanelServices,
} from './ports'
import { ToastHost } from './ui/toast'
import type { PortsStatusResult } from './generated/ports.status'
import type { TunnelOpenResult } from './generated/tunnel.open'
import { LOCAL_TARGET_ID } from './ports-client'

vi.mock('./renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

afterEach(() => {
  cleanup()
  document.body.replaceChildren()
})

// ── Fixtures ──────────────────────────────────────────────────────────────

const statusFixture = (): PortsStatusResult => ({
  profileId: 'ssh:p1:1',
  host: 'host.example',
  discovery: {
    state: 'available',
    listeners: [],
    probe: 'ss',
    probesTried: ['ss'],
    classification: '',
    stderr: '',
    lastSampleAt: null,
    paused: false,
    visible: true,
    connLost: false,
  },
  forwards: [],
})

const runningRecord = (): TunnelOpenResult => ({
  id: 'fwd-1',
  direction: 'local',
  requestedBind: { host: '127.0.0.1', port: 6768 },
  actualBind: { host: '127.0.0.1', port: 6768 },
  destination: 'host.example:6768',
  caveat: '',
  scope: 'ports:ssh:p1:1',
  state: 'running',
  stopReason: null,
  error: null,
})

function fakeServices(over: Partial<PortsPanelServices> = {}): PortsPanelServices {
  return {
    status: vi.fn<PortsPanelServices['status']>().mockResolvedValue(statusFixture()),
    sample: vi.fn<PortsPanelServices['sample']>().mockResolvedValue(statusFixture()),
    pause: vi.fn<PortsPanelServices['pause']>().mockResolvedValue({}),
    visible: vi.fn<PortsPanelServices['visible']>().mockResolvedValue({}),
    openForward: vi.fn<PortsPanelServices['openForward']>().mockResolvedValue(runningRecord()),
    stopForward: vi.fn<PortsPanelServices['stopForward']>().mockResolvedValue({
      ...runningRecord(),
      state: 'stopped',
      stopReason: 'user',
      error: null,
    }),
    ...over,
  }
}

/** The manager main.tsx builds, but with a profileClient the test controls:
 *  createProfile is the seam under test; the rest of the surface it needs
 *  (listProfiles/listGroups) is a no-op. */
async function mountManager(createProfile: Mock) {
  const { bar, panes } = setupTabBarDOM()
  const client = makeClient()
  const profileClient = {
    listProfiles: vi.fn().mockResolvedValue([]),
    listGroups: vi.fn().mockResolvedValue([]),
    createProfile,
  } as unknown as ProfileClient
  const manager = new PaneManager(
    bar,
    bar,
    panes,
    client as never,
    makeClipboard(),
    new ClipboardGate(),
    makeBanner(),
    profileClient,
    new HorizontalTabStrip(),
    makeLayoutStore().store,
    makeUIStateBackend().newClient(),
  )
  await manager.openInitialPane()
  return manager
}

/** The panel wired as main.tsx wires it, starting in the W1 unavailable
 *  state: the ports target is null and unavailableIn names the host, and
 *  onActivePaneChange feeds later re-scopes exactly as the composition root
 *  does. A ToastHost makes outcomes assertable as rendered toasts. */
function mountPanel(
  manager: PaneManager,
  services: PortsPanelServices,
  onOpenAsConnection: (host: string, user: string | undefined) => void,
) {
  const [pid, setPid] = createSignal<string | null>(null)
  manager.onActivePaneChange = () => setPid(manager.portsTargetId())
  const pause = createPortsPauseControl()
  const filter = createPortsFilterControl()
  render(() => (
    <>
      <ToastHost />
      <PortsPanel
        profileId={pid}
        unavailableIn={() => 'pi@192.168.0.93'}
        onOpenAsConnection={onOpenAsConnection}
        services={services}
        visible={() => true}
        pause={pause}
        filter={filter}
      />
    </>
  ))
}

// ── The seam, end to end ──────────────────────────────────────────────────

describe('PortsPanel — open as connection (W2)', () => {
  it('creates the profile via adoptAliasProfile and opens a tab on the id the backend mints', async () => {
    const createProfile = vi.fn().mockResolvedValue({
      id: 'ssh:created:1',
      name: '192.168.0.93',
      type: 'ssh',
      options: { host: '192.168.0.93', user: 'pi' },
    })
    const manager = await mountManager(createProfile)
    const status = vi.fn<PortsPanelServices['status']>().mockResolvedValue(statusFixture())
    mountPanel(manager, fakeServices({ status }), (host, user) =>
      manager.openAsConnection(host, user),
    )

    const action = await waitFor(() => {
      const el = document.querySelector('[data-testid="ports-open-as-connection"]')
      expect(el).not.toBeNull()
      return el as HTMLElement
    })
    fireEvent.click(action)

    await waitFor(() => expect(createProfile).toHaveBeenCalledTimes(1))
    // adoptAliasProfile's shape: an empty id the backend mints, the alias as
    // host, user set, and NO port — a hand-typed ssh tells us nothing about
    // the port, and "not set" must stay not set.
    expect(createProfile).toHaveBeenCalledWith({
      id: '',
      name: '192.168.0.93',
      type: 'ssh',
      options: { host: '192.168.0.93', user: 'pi' },
    })

    // The user lands on a tab whose ports target is the minted id — Ports
    // works there. The panel re-scopes to it without a tab switch.
    await waitFor(() => expect(manager.portsTargetId()).toBe('ssh:created:1'))
    await waitFor(() => expect(status).toHaveBeenCalledWith('ssh:created:1'))
  })

  it('tells the user on screen when profile creation rejects, and opens nothing', async () => {
    const createProfile = vi.fn().mockRejectedValue(new Error('vault locked'))
    const manager = await mountManager(createProfile)
    mountPanel(manager, fakeServices(), (host, user) => manager.openAsConnection(host, user))

    const action = await waitFor(() => {
      const el = document.querySelector('[data-testid="ports-open-as-connection"]')
      expect(el).not.toBeNull()
      return el as HTMLElement
    })
    fireEvent.click(action)

    await waitFor(() => expect(createProfile).toHaveBeenCalledTimes(1))
    // The failure is a danger toast — sticky, because an error the user was
    // not looking at is an error they never saw.
    await waitFor(() => {
      expect(document.body.textContent ?? '').toContain('Could not connect to 192.168.0.93')
      expect(document.body.textContent ?? '').toContain('vault locked')
    })
    // Nothing opened: the user is still on the hand-typed tab, whose ports
    // target stays the reserved local one.
    expect(manager.paneCount).toBe(1)
    expect(manager.portsTargetId()).toBe(LOCAL_TARGET_ID)
  })
})
