// @vitest-environment jsdom
// Ports is a SIDEBAR VIEW (nocx-wzc4.7) — the deliverable is the
// activity-bar icon, not a palette item and not a tab. AGENTS.md rule 1:
// a user opens the view from the activity bar and sees the ports of the
// tab they are looking at; switching SSH tabs re-scopes the view; a local
// tab never shows a stale host; collapsing the sidebar pauses sampling;
// Ctrl/Cmd+Shift+O reveals-or-focuses instead of opening another tab.
//
// These start from a real PaneManager and the real mountSidebar — the panel
// never mounts in a vacuum.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import {
  PortsPanel,
  createPortsFilter,
  createPortsFilterControl,
  createPortsPauseControl,
  type PortsPanelServices,
} from './ports'
import { mountSidebar, type SidebarHandle, type SidebarViewDescriptor } from './sidebar'
import { IconButton } from './ui/icon-button'
import { PlugIcon, RefreshIcon } from './ui/icons'
import {
  createRendererMock,
  makeClient,
  makeSession,
  mountPaneManager,
} from './test-support/panes-fixtures'
import type { PortsStatusResult } from './generated/ports.status'
import type { TunnelOpenResult } from './generated/tunnel.open'
import { LOCAL_TARGET_ID } from './ports-client'

vi.mock('./renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

const PORTS_VIEW_ID = 'ports'

// ── Fixtures ──────────────────────────────────────────────────────────────

const statusFixture = (profileId: string): PortsStatusResult => ({
  profileId,
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

const listenerFixture = (port: number): PortsStatusResult['discovery']['listeners'][number] => ({
  family: 'ipv4' as const,
  address: '0.0.0.0',
  port,
  process: { evidence: 'known', name: 'sshd', pid: 123 },
})

const runningRecord = (over: Partial<TunnelOpenResult> = {}): TunnelOpenResult => ({
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
  ...over,
})

function fakeServices(over: Partial<PortsPanelServices> = {}): PortsPanelServices {
  return {
    status: vi.fn().mockResolvedValue(statusFixture('ssh:p1:1')),
    sample: vi.fn().mockResolvedValue(statusFixture('ssh:p1:1')),
    pause: vi.fn().mockResolvedValue({}),
    visible: vi.fn().mockResolvedValue({}),
    openForward: vi.fn().mockResolvedValue(runningRecord()),
    stopForward: vi
      .fn()
      .mockResolvedValue({ ...runningRecord(), state: 'stopped', stopReason: 'user', error: null }),
    ...over,
  }
}

/** The view descriptor main.tsx builds — the sidebar's first real view. The
 *  Pause control is created here too, shared between the header action and
 *  the panel, exactly as main.tsx wires it (nocx-wzc4.9). */
function portsView(
  services: PortsPanelServices,
  portsTargetId: () => string | null,
  unavailableIn: () => string = () => '',
): SidebarViewDescriptor {
  const pause = createPortsPauseControl()
  // The filter is the shell's pinned row, not the body's (nocx-708q.3):
  // one control, the field above the scroller and the rows inside it.
  const filter = createPortsFilterControl()
  return {
    id: PORTS_VIEW_ID,
    title: 'Ports',
    icon: PlugIcon,
    filter: createPortsFilter(filter),
    actions: () => (
      <IconButton
        data-testid="ports-refresh"
        size="sm"
        ariaLabel="Refresh ports"
        title="Refresh ports"
        disabled={portsTargetId() === null}
        onClick={() => void services.sample(portsTargetId() as string)}
      >
        <RefreshIcon />
      </IconButton>
    ),
    view: (props) => (
      <PortsPanel
        profileId={props.activeProfileId}
        unavailableIn={unavailableIn}
        services={services}
        visible={props.visible}
        pause={pause}
        filter={filter}
      />
    ),
    order: 0,
  }
}

const liveHandles: SidebarHandle[] = []

/** Full composition: a real PaneManager (with or without an SSH tab in
 *  front), the reactive active-profile signal main.tsx wires, and the
 *  real sidebar mounting the ports view. */
async function mountApp(
  services: PortsPanelServices,
  profileId: string | null = 'ssh:p1:1',
  unavailableIn: () => string = () => '',
) {
  const client = makeClient({
    openSSHSession: vi.fn(() => Promise.resolve(makeSession())),
    openSSHSessionByHost: vi.fn(() => Promise.resolve(makeSession())),
  })
  const { manager } = await mountPaneManager(client)
  if (profileId !== null) manager.newSSHPane(profileId, 'host.example', 'alice')

  const [portsTargetId, setPortsTargetId] = createSignal<string | null>(manager.portsTargetId())
  manager.onActivePaneChange = () => setPortsTargetId(manager.portsTargetId())

  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  const handle = mountSidebar(
    bar,
    panel,
    [
      /* eslint-disable solid/reactivity -- portsView consumes this accessor
         inside the view's and the header action's tracked scopes; the gate
         cannot see across that boundary (same contract as the arg below). */
      portsView(services, () => portsTargetId(), unavailableIn),
      /* eslint-enable solid/reactivity */
    ],
    [],
    undefined,
    /* eslint-disable solid/reactivity -- same contract as main.tsx: the
       sidebar reads this accessor inside tracked view scopes. */
    () => portsTargetId(),
  )
  liveHandles.push(handle)
  return { manager, bar, panel, handle }
}

function portsIcon(bar: HTMLElement): HTMLElement {
  const el = bar.querySelector<HTMLElement>(`button[data-view="${PORTS_VIEW_ID}"]`)
  if (!el) throw new Error('no ports activity-bar button')
  return el
}

afterEach(() => {
  for (const h of liveHandles) h.destroy()
  liveHandles.length = 0
  cleanup()
  document.body.replaceChildren()
})

// ── The acceptance: reachable from the activity bar, scoped to the tab ────

describe('ports sidebar view', () => {
  it("a user opens Ports from the activity bar and sees the ACTIVE tab's ports", async () => {
    const status = vi.fn().mockResolvedValue({
      ...statusFixture('ssh:p1:1'),
      discovery: {
        ...statusFixture('ssh:p1:1').discovery,
        listeners: [listenerFixture(22)],
      },
    })
    const services = fakeServices({ status })
    const { bar, panel } = await mountApp(services)

    // mountSidebar auto-selects the first view and starts expanded
    // (nocx-rp2j) — the panel is on screen and asked the backend for THIS
    // connection's ports.
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p1:1'))
    await vi.waitFor(() => expect(panel.textContent).toContain('0.0.0.0:22'))

    // The user path: click the activity-bar icon to close the view, then
    // click it again — the view comes back showing the same tab's ports.
    const icon = portsIcon(bar)
    icon.click()
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))
    icon.click()
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    await vi.waitFor(() => expect(panel.textContent).toContain('0.0.0.0:22'))
  })

  it('switching SSH tabs re-scopes the view to the tab in front', async () => {
    const status = vi.fn().mockResolvedValue(statusFixture('ssh:p1:1'))
    const services = fakeServices({ status })
    const { manager } = await mountApp(services)
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p1:1'))

    manager.newSSHPane('ssh:p2:2', 'other.example', 'bob')
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p2:2'))
  })
  it('switching SSH tabs clears the filter — one host query never carries over', async () => {
    const status = vi.fn().mockResolvedValue({
      ...statusFixture('ssh:p1:1'),
      discovery: {
        ...statusFixture('ssh:p1:1').discovery,
        listeners: [listenerFixture(22)],
      },
    })
    const services = fakeServices({ status })
    const { manager, panel } = await mountApp(services)
    await vi.waitFor(() => expect(panel.textContent).toContain('0.0.0.0:22'))

    const field = panel.querySelector<HTMLInputElement>('input[aria-label="Filter ports"]')
    expect(field).not.toBeNull()
    fireEvent.input(field!, { target: { value: 'nothing-matches' } })
    await vi.waitFor(() =>
      expect(panel.querySelectorAll('[data-testid="detected-row"]')).toHaveLength(0),
    )
    expect(panel.textContent).toContain('No ports match that')

    // The filter is part of the re-scoped state (decision 4): a query typed
    // for host A's ports must not meet host B's list already half-filtered.
    manager.newSSHPane('ssh:p2:2', 'other.example', 'bob')
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p2:2'))
    await vi.waitFor(() =>
      expect(panel.querySelectorAll('[data-testid="detected-row"]')).toHaveLength(1),
    )
    expect(panel.querySelector<HTMLInputElement>('input[aria-label="Filter ports"]')?.value).toBe(
      '',
    )
  })
  it("a local tab shows THIS machine's listeners, scoped to 'local'", async () => {
    const status = vi.fn().mockResolvedValue({
      ...statusFixture(LOCAL_TARGET_ID),
      host: 'my-machine',
      discovery: {
        ...statusFixture(LOCAL_TARGET_ID).discovery,
        listeners: [listenerFixture(22)],
      },
    })
    const services = fakeServices({ status })
    const { panel } = await mountApp(services, null)

    // The initial tab is a local shell: the panel asks the backend for the
    // reserved "local" target and shows this machine's listeners — never
    // the no-connection state a profile-less tab used to get.
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith(LOCAL_TARGET_ID))
    await vi.waitFor(() => expect(panel.textContent).toContain('0.0.0.0:22'))
    expect(panel.textContent).not.toContain('No active connection')
    // A local row offers copy-address, never Forward.
    expect(panel.querySelector('[data-testid="ports-forward"]')).toBeNull()
    expect(panel.querySelector('[data-testid="ports-copy"]')).not.toBeNull()
  })
  it('switching local to SSH and back re-scopes the view both ways', async () => {
    const status = vi.fn().mockResolvedValue(statusFixture(LOCAL_TARGET_ID))
    const services = fakeServices({ status })
    const { manager } = await mountApp(services, null)
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith(LOCAL_TARGET_ID))

    manager.newSSHPane('ssh:p1:1', 'host.example', 'alice')
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p1:1'))

    // Back to the local tab (index 0): the panel re-scopes to 'local' — the
    // SSH host's ports are gone, this machine's are back.
    manager.activateByIndex(0)
    const localCalls = () => status.mock.calls.filter(([pid]) => pid === LOCAL_TARGET_ID).length
    await vi.waitFor(() => expect(localCalls()).toBeGreaterThanOrEqual(2))
    expect(status.mock.calls[status.mock.calls.length - 1]?.[0]).toBe(LOCAL_TARGET_ID)
  })
  it('an alias tab — neither local nor a saved profile — shows the no-connection state', async () => {
    const status = vi.fn().mockResolvedValue(statusFixture('ssh:p1:1'))
    const services = fakeServices({ status })
    const { manager, panel } = await mountApp(services)
    await vi.waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p1:1'))

    // Alias: an SSH session with no profile id — no ports scope at all.
    manager.newSSHPane('', 'alias.example', 'bob')
    await vi.waitFor(() => expect(panel.textContent).toContain('No active connection'))
    expect(status).toHaveBeenCalledTimes(1)
  })

  it('collapsing the sidebar pauses sampling; expanding resumes it', async () => {
    const visible = vi.fn().mockResolvedValue({})
    const services = fakeServices({ visible })
    const { bar } = await mountApp(services)
    await vi.waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', true))

    const icon = portsIcon(bar)
    icon.click() // collapse — counts as not visible
    await vi.waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', false))

    icon.click() // expand
    await vi.waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', true))
  })

  it('Ctrl/Cmd+Shift+O reveals the collapsed view and focuses it when open', async () => {
    const services = fakeServices()
    const { bar, panel, handle } = await mountApp(services)
    const icon = portsIcon(bar)

    // The chord main.tsx registers: reveal-or-focus, never "open another".
    const handler = (e: KeyboardEvent): void => {
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key.toLowerCase() === 'o') {
        e.preventDefault()
        handle.revealView(PORTS_VIEW_ID)
      }
    }
    document.addEventListener('keydown', handler)
    const chord = (): KeyboardEvent => {
      const e = new KeyboardEvent('keydown', {
        key: 'O', // Shift held — the browser reports the uppercase letter
        ctrlKey: true,
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      })
      document.dispatchEvent(e)
      return e
    }

    icon.click() // collapse
    expect(panel.classList.contains('collapsed')).toBe(true)

    const reveal = chord()
    expect(reveal.defaultPrevented).toBe(true)
    expect(panel.classList.contains('collapsed')).toBe(false)

    // Already on screen — the chord focuses the view's icon.
    icon.blur()
    const focus = chord()
    expect(focus.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(icon)

    document.removeEventListener('keydown', handler)
  })

  it('pins the filter above the scroller, where it cannot scroll away (nocx-708q.3)', async () => {
    const status = vi.fn().mockResolvedValue({
      ...statusFixture('ssh:p1:1'),
      discovery: {
        ...statusFixture('ssh:p1:1').discovery,
        listeners: [listenerFixture(22)],
      },
    })
    const services = fakeServices({ status })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(panel.textContent).toContain('0.0.0.0:22'))

    // The field used to sit above the sections INSIDE the body, which is
    // the right place in a scrolling column and the wrong place full stop:
    // scroll the list and the control that narrows it goes with it (owner,
    // 2026-08-22). Pinned means the filter row, never the body.
    const field = panel.querySelector<HTMLElement>('input[aria-label="Filter ports"]')
    expect(field).not.toBeNull()
    expect(field!.closest('.ui-sidebar-view__filter')).not.toBeNull()
    expect(field!.closest('.ui-sidebar-view__body')).toBeNull()
  })

  it('offers no filter where there is no list to narrow (nocx-cdub)', async () => {
    // A search box above an explanation of why there are no ports is noise.
    // Only the panel knows the discovery state, so it reports it through
    // the shared control — the field moved out of the body but the rule it
    // was drawn under did not move with it.
    const base = statusFixture('ssh:p1:1')
    const status = vi.fn().mockResolvedValue({
      ...base,
      discovery: {
        ...base.discovery,
        state: 'unavailable',
        classification: 'No probe tool is usable on this host.',
      },
    })
    const { panel } = await mountApp(fakeServices({ status }))
    await vi.waitFor(() =>
      expect(panel.textContent).toContain('Could not determine what is listening'),
    )
    expect(panel.querySelector('input[aria-label="Filter ports"]')).toBeNull()
  })

  it('refreshing from the header asks the active target again (nocx-wzc4.11)', async () => {
    const sample = vi.fn().mockResolvedValue(statusFixture('ssh:p1:1'))
    const status = vi.fn().mockResolvedValue(statusFixture('ssh:p1:1'))
    const services = fakeServices({ sample, status })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(status).toHaveBeenCalled())

    // The action lives in the view's HEADER (SidebarViewDescriptor.actions),
    // never in the body — the body carries no second vocabulary for it.
    const refreshBtn = panel.querySelector<HTMLElement>('[data-testid="ports-refresh"]')
    expect(refreshBtn).not.toBeNull()
    expect(refreshBtn?.closest('.ui-sidebar-view__header')).not.toBeNull()
    expect(refreshBtn?.closest('.ui-sidebar-view__body')).toBeNull()

    // One sample costs ~12ms, so asking again is the useful control — not
    // protecting the host from a poll it does not notice.
    refreshBtn!.click()
    await vi.waitFor(() => expect(sample).toHaveBeenCalledWith('ssh:p1:1'))
  })

  // ── The loader (nocx-wzc4.11 report 5, re-reported 2026-08-04) ─────────
  // A user opens the view before the first sample has landed and must see
  // that nocx is working. This was "fixed" once with no test, and the owner
  // reported it unfixed twice — so the assertion is what the user sees, at
  // the two moments that matter: while the first status is in flight, and
  // while the target is connected but its first sample has not arrived.

  it('shows the spinner while the first status call is still in flight', async () => {
    let release!: (v: PortsStatusResult) => void
    const pending = new Promise<PortsStatusResult>((res) => {
      release = res
    })
    const services = fakeServices({ status: vi.fn().mockReturnValue(pending) })
    const { bar, panel } = await mountApp(services)
    portsIcon(bar).click()
    await Promise.resolve()

    expect(panel.querySelector('[data-testid="ports-loading"]')).not.toBeNull()

    release(statusFixture('ssh:p1:1'))
    await vi.waitFor(() => expect(panel.querySelector('[data-testid="ports-loading"]')).toBeNull())
  })

  it('keeps the spinner up for a connected target whose first sample has not landed', async () => {
    const settling = statusFixture('ssh:p1:1')
    settling.discovery.state = 'pending'
    const status = vi.fn().mockResolvedValue(settling)
    const services = fakeServices({ status })
    const { bar, panel } = await mountApp(services)
    portsIcon(bar).click()
    await vi.waitFor(() => expect(status).toHaveBeenCalled())

    expect(panel.querySelector('[data-testid="ports-loading"]')).not.toBeNull()
  })

  // ── The Detected section is never blank (owner, 2026-08-04) ────────────
  // A heading, a hairline and nothing under it. Whatever the discovery
  // state is, the section says SOMETHING — rows, a reason, or a spinner.
  const DISCOVERY_STATES = [
    'available',
    'available-limited',
    'unavailable',
    'failed-transiently',
    'permission-or-policy-refused',
    'pending',
  ] as const

  for (const state of DISCOVERY_STATES) {
    it(`says something under Detected when discovery is ${state}`, async () => {
      const fixture = statusFixture('ssh:p1:1')
      fixture.discovery.state = state
      fixture.discovery.listeners = []
      const status = vi.fn().mockResolvedValue(fixture)
      const { bar, panel } = await mountApp(fakeServices({ status }))
      portsIcon(bar).click()
      await vi.waitFor(() => expect(status).toHaveBeenCalled())

      const heading = [...panel.querySelectorAll('h2')].find((h) => h.textContent === 'Detected')
      const section = heading?.closest('section')
      await vi.waitFor(() => {
        const text = (section?.textContent ?? '').replace('Detected', '').trim()
        const spinner = panel.querySelector('[data-testid="ports-loading"]')
        expect(text.length > 0 || spinner !== null).toBe(true)
      })
    })
  }
  it('names an unknown discovery state instead of rendering nothing', async () => {
    const fixture = statusFixture('ssh:p1:1')
    // Deliberately off-contract: what the panel must do when the backend
    // grows a state the renderer has not learned yet. A heading with a
    // hairline and nothing under it is the shape a user reads as broken.
    ;(fixture.discovery as unknown as { state: string }).state = 'settling'
    fixture.discovery.listeners = []
    const status = vi.fn().mockResolvedValue(fixture)
    const { bar, panel } = await mountApp(fakeServices({ status }))
    portsIcon(bar).click()
    await vi.waitFor(() => expect(status).toHaveBeenCalled())

    await vi.waitFor(() => {
      const heading = [...panel.querySelectorAll('h2')].find((h) => h.textContent === 'Detected')
      const section = heading?.closest('section')
      expect(section?.textContent ?? '').toContain('settling')
    })
  })
  it('names whose listeners it is showing, so the tab title cannot be mistaken for it', async () => {
    const { bar, panel } = await mountApp(fakeServices())
    portsIcon(bar).click()
    await vi.waitFor(() => {
      const note = panel.querySelector('[data-testid="ports-target-note"]')
      expect(note?.textContent ?? '').toContain('host.example')
    })
  })

  it('says the local listeners belong to this machine, not to whatever the tab is called', async () => {
    const local = statusFixture(LOCAL_TARGET_ID)
    local.host = ''
    const { bar, panel } = await mountApp(
      fakeServices({
        status: vi.fn().mockResolvedValue(local),
        sample: vi.fn().mockResolvedValue(local),
      }),
      LOCAL_TARGET_ID,
    )
    portsIcon(bar).click()
    await vi.waitFor(() => {
      const note = panel.querySelector('[data-testid="ports-target-note"]')
      expect(note?.textContent ?? '').toContain('This machine')
    })
  })
  it('says which host it cannot see, instead of listing this machine under its tab', async () => {
    // The pane walked into a shell reached by hand: there is no managed
    // connection to it, so there is no second exec channel to ask on, and
    // reporting the local target here is what put this machine's listeners
    // under a tab sitting on a Pi (owner, 2026-08-04).
    const status = vi.fn()
    const services = fakeServices({ status })
    const pause = createPortsPauseControl()
    const { container } = render(() => (
      <PortsPanel
        profileId={() => null}
        unavailableIn={() => 'pi@192.168.0.93'}
        services={services}
        visible={() => true}
        pause={pause}
        filter={createPortsFilterControl()}
      />
    ))
    await vi.waitFor(() => {
      expect(container.textContent ?? '').toContain('pi@192.168.0.93')
      expect(container.textContent ?? '').toContain('Cannot see the ports')
    })
    expect(status).not.toHaveBeenCalled()
  })
})
