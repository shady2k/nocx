// @vitest-environment jsdom
// PortsPanel tests (nocx-wzc4.2, nocx-wzc4.9). Rule 1 of AGENTS.md: assert
// what a user can do, not what the code renders — the panel is reachable
// from the state a user starts in, the forward action on a detected row
// reaches the client method, and the row moves to Forwarded afterwards; a
// hidden tab stops sampling; a permission-denied probe renders the
// explanation; a probe-less host says so. Loading (nocx-wzc4.9): the view
// shows it is loading before the first sample lands, a refresh never blanks
// a populated list, a failure state offers exactly one Retry, and the body
// carries no second vocabulary for Pause or Retry.

import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, fireEvent, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { clearToasts } from './ui/toast'
import type { TunnelOpenResult } from './generated/tunnel.open'
import { LOCAL_TARGET_ID } from './ports-client'
import type { PortsStatusResult } from './generated/ports.status'
import {
  PortsPanel,
  POLL_INTERVAL_MS,
  createPortsFilter,
  createPortsFilterControl,
  createPortsPauseControl,
  type PortsPanelProps,
  type PortsPanelServices,
} from './ports'
const listingToasts: { level?: string; message: string }[] = []
vi.mock('./ui/toast', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./ui/toast')>()
  return {
    ...actual,
    showToast: (t: { level?: string; message: string }) => {
      listingToasts.push(t)
    },
  }
})
afterEach(cleanup)
afterEach(() => {
  listingToasts.length = 0
})

const discoveryFixture = (
  over: Partial<PortsStatusResult['discovery']> = {},
): PortsStatusResult['discovery'] => ({
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
  ...over,
})

const statusFixture = (
  over: Partial<PortsStatusResult['discovery']> = {},
  extra: Partial<PortsStatusResult> = {},
): PortsStatusResult => ({
  profileId: 'ssh:p1:1',
  host: 'host.example',
  discovery: discoveryFixture(over),
  forwards: [],
  ...extra,
})

const listenerFixture = (
  port: number,
  evidence: 'known' | 'permission-denied' | 'unsupported' = 'known',
) => ({
  family: 'ipv4' as const,
  address: '0.0.0.0',
  port,
  process:
    evidence === 'known' ? { evidence, name: 'node', pid: 123 } : { evidence, name: '', pid: 0 },
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
    status: vi.fn().mockResolvedValue(statusFixture()),
    sample: vi.fn().mockResolvedValue(statusFixture()),
    pause: vi.fn().mockResolvedValue({}),
    visible: vi.fn().mockResolvedValue({}),
    openForward: vi.fn().mockResolvedValue(runningRecord()),
    stopForward: vi
      .fn()
      .mockResolvedValue({ ...runningRecord(), state: 'stopped', stopReason: 'user', error: null }),
    ...over,
  }
}

/** The panel plus the two controls it shares with the shell — Pause, which
 *  the header action drives (nocx-wzc4.9), and Filter, whose field the
 *  shell pins ABOVE the scrolling body (nocx-708q.3).
 *
 *  The filter component is rendered here rather than left out, because it
 *  is no longer part of the panel: the panel narrows its rows by the
 *  control and reports whether there is a list to narrow, and the field
 *  that writes the query is the shell's. Rendering both is what keeps
 *  `getByLabelText('Filter ports')` a test of the seam a user reaches
 *  rather than of markup that no longer exists. */
function renderPanel(services: PortsPanelServices, over: Partial<PortsPanelProps> = {}) {
  // The controls are created ONCE, outside the JSX. Solid wraps every prop
  // expression in a getter, so `pause={createPortsPauseControl()}` builds a
  // fresh control on every read — `sync` would write one instance while the
  // view read another, and the panel could never show a pause it did not
  // itself initiate (nocx-wzc4.10).
  const pause = createPortsPauseControl()
  const filter = over.filter ?? createPortsFilterControl()
  const PortsFilter = createPortsFilter(filter)
  return render(() => (
    <>
      <PortsFilter />
      <PortsPanel
        profileId={() => 'ssh:p1:1'}
        services={services}
        visible={() => true}
        pause={pause}
        {...over}
        filter={filter}
      />
    </>
  ))
}

// ── Detected → Forwarded in one action ───────────────────────────────────
describe('PortsPanel — detected rows', () => {
  it('explains hidden owners once above the rows, not on every row', async () => {
    const services = fakeServices({
      status: vi
        .fn()
        .mockResolvedValue(
          statusFixture({ listeners: [listenerFixture(22, 'permission-denied')] }),
        ),
    })
    renderPanel(services)
    // The privilege is the probe's, not the row's — one statement above the
    // list, never a banner repeated down a 240px rail (nocx-wzc4.11).
    await waitFor(() => expect(screen.getByTestId('ports-owners-note')).toBeTruthy())
    expect(screen.getByTestId('ports-owners-note').textContent).toMatch(/run as root/)
    expect(screen.getByText('0.0.0.0:22')).toBeTruthy()
    expect(screen.queryAllByText(/run as root/)).toHaveLength(1)
  })

  it('a probe-less host says so — and never claims "nothing is listening"', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture({
          state: 'unavailable',
          classification: 'no probe tool usable on this host',
        }),
      ),
    })
    renderPanel(services)
    await waitFor(() =>
      expect(screen.getByText('Could not determine what is listening')).toBeTruthy(),
    )
    expect(screen.queryByText('Nothing is listening')).toBeNull()
  })

  it('"Nothing is listening" appears only when the sample truly was empty', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ state: 'available' })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('Nothing is listening')).toBeTruthy())
  })

  it('a refused host renders the refusal and offers Retry', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture({
          state: 'permission-or-policy-refused',
          classification: 'additional sessions refused',
        }),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('Discovery refused on this host')).toBeTruthy())
    expect(screen.getByText('additional sessions refused')).toBeTruthy()
  })

  it('the forward action on a detected row reaches the client method and changes that row in place', async () => {
    const openForward = vi.fn().mockResolvedValue(runningRecord())
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
      openForward,
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-forward'))

    // The row's one action: the destination dials the REMOTE host for a
    // wildcard bind, with the same numeric port.
    await waitFor(() =>
      expect(openForward).toHaveBeenCalledWith('ssh:p1:1', 'host.example:6768', 6768),
    )
    // The SAME row now shows the usable local address and its action is
    // Stop — one port, one row (W2). The old behavior left this row
    // unchanged and added a second row under "Forwarded".
    await waitFor(() => expect(screen.getByTestId('ports-stop')).toBeTruthy())
    expect(screen.getByText(/127.0.0.1:6768/)).toBeTruthy()
    const rows = [...document.querySelectorAll('.ports-row')].filter((r) =>
      (r.textContent ?? '').includes('6768'),
    )
    expect(rows).toHaveLength(1)
    expect(screen.queryByTestId('forwarded-row')).toBeNull()
    // The orphan section has no subject here: the forward lives on its own
    // row, so the section must not render — its old "No active forwards"
    // fallback announced an empty section directly under a forward shown
    // live on the row above it (W2 revision). Assert the text, not just a
    // testid: the testid absence passed while the empty state was on screen.
    expect(screen.queryByText('No active forwards')).toBeNull()
    expect(screen.queryByText('Orphaned forwards')).toBeNull()
  })

  it('a busy local port falls back to an allocated loopback port', async () => {
    const openForward = vi
      .fn()
      .mockRejectedValueOnce(new Error('listen tcp 127.0.0.1:6768: bind: address already in use'))
      .mockResolvedValueOnce(
        runningRecord({
          requestedBind: { host: '127.0.0.1', port: 0 },
          actualBind: { host: '127.0.0.1', port: 43210 },
        }),
      )
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
      openForward,
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-forward'))

    await waitFor(() =>
      expect(openForward).toHaveBeenCalledWith('ssh:p1:1', 'host.example:6768', 6768),
    )
    await waitFor(() =>
      expect(openForward).toHaveBeenCalledWith('ssh:p1:1', 'host.example:6768', 0),
    )
    await waitFor(() => expect(screen.getByTestId('ports-stop')).toBeTruthy())
    expect(screen.getByText(/127.0.0.1:43210/)).toBeTruthy()
    const rows = [...document.querySelectorAll('.ports-row')].filter((r) =>
      (r.textContent ?? '').includes('6768'),
    )
    expect(rows).toHaveLength(1)
  })
})

// ── Loading and refresh (nocx-wzc4.9) ────────────────────────────────────

describe('PortsPanel — loading and refresh (nocx-wzc4.9)', () => {
  it('shows it is loading before the first sample lands', async () => {
    let resolve!: (st: PortsStatusResult) => void
    const services = fakeServices({
      status: vi.fn(
        () =>
          new Promise<PortsStatusResult>((res) => {
            resolve = res
          }),
      ),
    })
    renderPanel(services)

    // First open, no data yet: the panel says it is working, not blank.
    expect(screen.getByTestId('ports-loading')).toBeTruthy()

    resolve(statusFixture({ state: 'available', listeners: [listenerFixture(22)] }))
    await waitFor(() => expect(screen.getByText('0.0.0.0:22')).toBeTruthy())
    expect(screen.queryByTestId('ports-loading')).toBeNull()
  })

  it('a refresh never blanks a populated list', async () => {
    vi.useFakeTimers()
    try {
      let resolveSecond!: (st: PortsStatusResult) => void
      const status = vi
        .fn()
        .mockResolvedValueOnce(
          statusFixture({ state: 'available', listeners: [listenerFixture(22)] }),
        )
        .mockImplementationOnce(
          () =>
            new Promise<PortsStatusResult>((res) => {
              resolveSecond = res
            }),
        )
      const services = fakeServices({ status })
      renderPanel(services)
      await vi.advanceTimersByTimeAsync(0)
      expect(screen.getByText('0.0.0.0:22')).toBeTruthy()

      // One poll interval later a refresh is in flight but unanswered — the
      // populated list must not blank to a spinner; it is what the user is
      // watching.
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS)
      expect(screen.getByText('0.0.0.0:22')).toBeTruthy()
      expect(screen.queryByTestId('ports-loading')).toBeNull()

      // The late answer swaps the row in place.
      resolveSecond(statusFixture({ state: 'available', listeners: [listenerFixture(8080)] }))
      await vi.advanceTimersByTimeAsync(0)
      expect(screen.getByText('0.0.0.0:8080')).toBeTruthy()
      expect(screen.queryByText('0.0.0.0:22')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('a backend-reported pause stops the status poll (nocx-wzc4.11)', async () => {
    // The header action is Refresh now, so nothing in the renderer flips this;
    // the control only REFLECTS a pause the backend reports, and the poll must
    // still honour it.
    vi.useFakeTimers()
    try {
      const status = vi.fn().mockResolvedValue(statusFixture({ state: 'available', paused: true }))
      const services = fakeServices({ status })
      renderPanel(services)
      await vi.advanceTimersByTimeAsync(0)
      const callsAfterFirst = status.mock.calls.length
      expect(callsAfterFirst).toBeGreaterThan(0)

      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 2)
      expect(status.mock.calls.length).toBe(callsAfterFirst)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a failure state offers exactly one Retry, and the body offers no toolbar copy', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture({
          state: 'permission-or-policy-refused',
          classification: 'additional sessions refused',
        }),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('Discovery refused on this host')).toBeTruthy())

    // Retry exists exactly where it belongs: inside the failure state. The
    // toolbar copy (ports-retry) and the body Pause button are gone.
    expect(screen.getAllByText('Retry')).toHaveLength(1)
    expect(screen.queryByTestId('ports-retry')).toBeNull()
    expect(screen.queryByTestId('ports-pause')).toBeNull()
  })

  it('shows no sample age at all, and says "paused" only while paused (nocx-wzc4.10)', async () => {
    // The timestamp told the user nothing they could act on: the list
    // refreshes itself, and a failed sample shows the failure instead of
    // stale rows, so the rows on screen are never older than they look.
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture({
          state: 'available',
          listeners: [listenerFixture(6768)],
          lastSampleAt: '2026-08-04T12:00:00Z',
        }),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByTestId('detected-row')).toBeTruthy())
    expect(screen.queryByTestId('ports-meta')).toBeNull()
    expect(document.body.textContent).not.toContain('last sample')
  })

  it('names the one state where the rows stop tracking the host', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture({
          state: 'available',
          paused: true,
          listeners: [listenerFixture(6768)],
          lastSampleAt: '2026-08-04T12:00:00Z',
        }),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByTestId('ports-meta')).toBeTruthy())
    const meta = screen.getByTestId('ports-meta')
    expect(meta.textContent).toContain('paused')
    expect(meta.textContent).not.toContain('last sample')
    expect(meta.querySelector('.ui-badge')).toBeNull()
  })

  it('a failed status fetch raises one Toast, not one per poll tick (nocx-8sudy)', async () => {
    vi.useFakeTimers()
    try {
      const status = vi.fn().mockRejectedValue(new Error('rpc: tunnel is closed'))
      const services = fakeServices({ status })
      renderPanel(services)
      // The first fetch fails immediately (refresh runs on mount).
      await vi.advanceTimersByTimeAsync(0)
      await waitFor(() => expect(listingToasts.length).toBe(1))
      expect(listingToasts[0].level).toBe('danger')
      // The outcome is a sentence at the surface, and the wire's reason is
      // mapped, not dropped (AGENTS.md: a soft degrade must be visible;
      // nocx-8sudy BRIEF-2). A dropped connection is one case.
      expect(listingToasts[0].message).toBe('Could not read the ports — the connection was lost.')
      expect(listingToasts[0].message).not.toContain('rpc:')
      // The failure persists across polls — the toast must not restack.
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3)
      expect(listingToasts.length).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a listing failure is not a document-flow badge — the body carries no status line', async () => {
    const services = fakeServices({
      status: vi.fn().mockRejectedValue(new Error('down')),
    })
    renderPanel(services)
    await waitFor(() => expect(listingToasts.length).toBe(1))
    // The unclassifiable reason is never swallowed: the open-set fallback
    // appends it to a person's sentence, so the diagnostic survives
    // (BRIEF-2 — we cannot claim to map an open set).
    expect(listingToasts[0].message).toBe('Could not read the ports (down).')
    expect(document.body.textContent).not.toContain('down')
    expect(document.querySelector('.ui-badge[data-tone="danger"]')).toBeNull()
  })
})

// ── Forwarded / Stopped lifecycle ─────────────────────────────────────────

describe('PortsPanel — forwards', () => {
  it('with no forwards at all, no forwards section and no "No active forwards"', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())
    // Nothing was ever forwarded, so the section has no subject: no heading
    // and no empty state under it — the old fallback announced "No active
    // forwards" below rows that already said so (W2 revision).
    expect(screen.queryByText('Orphaned forwards')).toBeNull()
    expect(screen.queryByText('No active forwards')).toBeNull()
    expect(screen.queryByTestId('forwarded-row')).toBeNull()
  })
  it('stopping a forwarded row returns it to its un-forwarded state and adds nothing anywhere else', async () => {
    const status = vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] }))
    const stopForward = vi.fn().mockResolvedValue({
      ...runningRecord(),
      state: 'stopped',
      stopReason: 'user',
      error: null,
    })
    const services = fakeServices({
      status,
      openForward: vi.fn().mockResolvedValue(runningRecord()),
      stopForward,
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())
    fireEvent.click(screen.getByTestId('ports-forward'))
    await waitFor(() => expect(screen.getByTestId('ports-stop')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-stop'))
    await waitFor(() => expect(stopForward).toHaveBeenCalledWith('fwd-1'))
    await waitFor(() => {
      // The row is back to its un-forwarded state: Forward is live again.
      expect(screen.queryByTestId('ports-stop')).toBeNull()
      expect(screen.getByTestId('ports-forward')).toBeTruthy()
      // And nothing was added anywhere else: no stopped row exists for any
      // stop reason, and no Stopped section exists at all — the section is
      // deleted, not hidden (W2).
      expect(screen.queryByTestId('stopped-row')).toBeNull()
    })
    expect(screen.queryByText('Stopped')).toBeNull()
    // And no orphan section either — the row owns the state, and the section
    // renders only when it holds a forward with no detected row. Its old
    // "No active forwards" fallback was on screen for the whole flow above,
    // contradicting the row's own forwarded state (W2 revision).
    expect(screen.queryByText('Orphaned forwards')).toBeNull()
    expect(screen.queryByText('No active forwards')).toBeNull()
  })

  it('a forward stopped with connection lost shows the reason and a working Retry on the port row', async () => {
    const openForward = vi
      .fn()
      .mockResolvedValue(runningRecord({ id: 'fwd-retried', destination: 'host.example:5432' }))
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          { listeners: [listenerFixture(5432)] },
          {
            forwards: [
              {
                ...runningRecord({
                  id: 'fwd-lost',
                  destination: 'host.example:5432',
                  requestedBind: { host: '127.0.0.1', port: 5432 },
                }),
                state: 'stopped' as const,
                stopReason: 'connection lost' as const,
                error: 'connection closed',
              },
            ],
          },
        ),
      ),
      openForward,
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText(/connection lost/)).toBeTruthy())
    // The raw reason is the row's hover, not a line of the row: a stopped
    // forward IS stopped, the badge says so persistently, and the reason
    // rides the same title hover the API tree's unreadable rows use
    // (commit 1e264610) — out of the document flow (nocx-8sudy 3/4).
    const row = screen.getByTestId('detected-row')
    expect(row.getAttribute('title')).toBe('connection closed')
    expect(row.textContent).not.toContain('connection closed')
    expect(screen.getByTestId('ports-retry-forward')).toBeTruthy()
    // The port's own row is the single owner of its state: exactly one row
    // mentions the port, and the Stopped section is gone.
    const rows = [...document.querySelectorAll('.ports-row')].filter((r) =>
      (r.textContent ?? '').includes('5432'),
    )
    expect(rows).toHaveLength(1)
    expect(screen.queryByTestId('stopped-row')).toBeNull()
    // The failure lives on the port's own row; the orphan section is absent —
    // its empty fallback used to contradict the row's failure state.
    expect(screen.queryByText('Orphaned forwards')).toBeNull()
    expect(screen.queryByText('No active forwards')).toBeNull()

    // Retry re-opens the forward for the same destination and port.
    fireEvent.click(screen.getByTestId('ports-retry-forward'))
    await waitFor(() =>
      expect(openForward).toHaveBeenCalledWith('ssh:p1:1', 'host.example:5432', 5432),
    )
  })

  it('a self-stopped forward whose port is no longer detected stays visible, with its reason and Retry', async () => {
    const openForward = vi.fn().mockResolvedValue(runningRecord())
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          { listeners: [] },
          {
            forwards: [
              {
                ...runningRecord({
                  id: 'fwd-lost',
                  destination: 'host.example:5432',
                  requestedBind: { host: '127.0.0.1', port: 5432 },
                }),
                state: 'stopped' as const,
                stopReason: 'error' as const,
                error: 'remote refused the channel',
              },
            ],
          },
        ),
      ),
      openForward,
    })
    renderPanel(services)
    // The port has no detected row, so the failure lives in the forwards
    // section — the one place that is not a resurrected Stopped, and never
    // silent (W2).
    await waitFor(() => expect(screen.getByTestId('forwarded-row')).toBeTruthy())
    // The section names what it holds — forwards with no detected row to own
    // them — and holds exactly this one.
    expect(screen.getByText('Orphaned forwards')).toBeTruthy()
    expect(screen.queryAllByTestId('forwarded-row')).toHaveLength(1)
    // The state marker stays on the row; the raw reason rides the row's
    // hover (commit 1e264610's treatment), not a line beside the badge.
    const row = screen.getByTestId('forwarded-row')
    expect(row.getAttribute('title')).toBe('remote refused the channel')
    expect(row.textContent).not.toContain('remote refused the channel')
    expect(screen.getByTestId('ports-retry-forward')).toBeTruthy()
    expect(screen.queryByTestId('stopped-row')).toBeNull()
  })

  it('a running forward supersedes an earlier failure for the SAME destination — one orphan row, not two (W7 revision)', async () => {
    // The webkit CI shape that sent the width spec back: a first
    // connection's stored forward stopped with "connection lost" while a
    // reconnect's replay ran, and the panel showed BOTH as orphaned rows —
    // a failed one and a running one, two truths about one destination,
    // exactly the shape the epic removed from Detected/Forwarded. Once a
    // forward for a destination is running, an earlier failure for the same
    // destination is no longer news: the running row carries the
    // destination's state, and the stale row's Retry would offer to redo
    // what has already been done.
    const dest = '192.168.0.93:9993'
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          { listeners: [] },
          {
            forwards: [
              {
                ...runningRecord({
                  id: 'fwd-first',
                  destination: dest,
                  requestedBind: { host: '127.0.0.1', port: 39871 },
                }),
                state: 'stopped' as const,
                stopReason: 'connection lost' as const,
                error: 'ssh: connection lost',
              },
              runningRecord({ id: 'fwd-live', destination: dest }),
            ],
          },
        ),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('Orphaned forwards')).toBeTruthy())
    // One destination, one row — the running one. The stale failure adds no
    // second row, no reason text, and no Retry beside a live forward.
    const rows = [...document.querySelectorAll('[data-testid="forwarded-row"]')]
    expect(rows).toHaveLength(1)
    expect(rows[0].getAttribute('data-state')).toBe('forwarded')
    expect(screen.getByText(dest)).toBeTruthy()
    expect(screen.queryByText(/connection lost/)).toBeNull()
    expect(screen.queryByTestId('ports-retry-forward')).toBeNull()
  })

  it('a detected row shows the RUNNING forward, never a stale failure for the same destination', async () => {
    const dest = 'host.example:6768'
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          { listeners: [listenerFixture(6768)] },
          {
            forwards: [
              {
                ...runningRecord({
                  id: 'fwd-lost',
                  destination: dest,
                  requestedBind: { host: '127.0.0.1', port: 6768 },
                }),
                state: 'stopped' as const,
                stopReason: 'connection lost' as const,
                error: 'connection closed',
              },
              runningRecord({ id: 'fwd-live', destination: dest }),
            ],
          },
        ),
      ),
    })
    renderPanel(services)
    // The port's own row carries the LIVE forward: Stop is reachable, the
    // address is the live bind, and the stale failure neither takes the row
    // over nor adds a second row anywhere.
    await waitFor(() => expect(screen.getByTestId('ports-stop')).toBeTruthy())
    const rows = [...document.querySelectorAll('.ports-row')]
    expect(rows).toHaveLength(1)
    expect(rows[0].getAttribute('data-state')).toBe('forwarded')
    expect(screen.getByText(/127\.0\.0\.1:6768/)).toBeTruthy()
    expect(screen.queryByText('Orphaned forwards')).toBeNull()
    expect(screen.queryByTestId('ports-retry-forward')).toBeNull()
  })

  it('a user stop offers no retry — the row is simply back to un-forwarded', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          { listeners: [listenerFixture(5432)] },
          {
            forwards: [
              {
                ...runningRecord({ id: 'fwd-user', destination: 'host.example:5432' }),
                state: 'stopped' as const,
                stopReason: 'user' as const,
                error: null,
              },
            ],
          },
        ),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('0.0.0.0:5432')).toBeTruthy())
    // The row is its normal self: Forward is live, nothing failed is shown.
    expect(screen.getByTestId('ports-forward')).toBeTruthy()
    expect(screen.queryByTestId('ports-retry-forward')).toBeNull()
    expect(screen.queryByTestId('stopped-row')).toBeNull()
  })

  it('a running forward with no matching detected listener stays visible and its Stop reaches the client', async () => {
    const stopForward = vi.fn().mockResolvedValue({
      ...runningRecord({ id: 'fwd-orphan' }),
      state: 'stopped',
      stopReason: 'user',
      error: null,
    })
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          { listeners: [] },
          {
            forwards: [
              runningRecord({
                id: 'fwd-orphan',
                destination: 'host.example:9999',
                actualBind: { host: '127.0.0.1', port: 9999 },
              }),
            ],
          },
        ),
      ),
      stopForward,
    })
    renderPanel(services)
    // The host may have stopped listening, discovery may be degraded, or the
    // forward may have been replayed — either way it must not vanish: the
    // orphan row keeps Stop reachable (W2).
    await waitFor(() => expect(screen.getByTestId('forwarded-row')).toBeTruthy())
    // The section appears, titled for what it holds, holding exactly this row.
    expect(screen.getByText('Orphaned forwards')).toBeTruthy()
    expect(screen.queryAllByTestId('forwarded-row')).toHaveLength(1)
    expect(screen.getByText(/host.example:9999/)).toBeTruthy()
    fireEvent.click(screen.getByTestId('ports-stop'))
    await waitFor(() => expect(stopForward).toHaveBeenCalledWith('fwd-orphan'))
  })

  it('a -R forward whose bind sshd replaced shows the caveat as a caution, never as "failed"', async () => {
    const caveat =
      'bind address 0.0.0.0 requested but not verified: the server may have bound a different address (GatewayPorts), so a URL built from this forward may only work on the server'
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture(
          {},
          {
            forwards: [
              {
                ...runningRecord({
                  id: 'fwd-remote',
                  direction: 'remote',
                  destination: 'host.example:5901',
                }),
                caveat,
              },
            ],
          },
        ),
      ),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByTestId('forwarded-row')).toBeTruthy())
    expect(screen.getByText('Orphaned forwards')).toBeTruthy()

    // The caveat is the backend's Caveat() verbatim: the bind was requested and
    // is not verified — a caution, never an error. Nothing failed; the forward
    // is running.
    const note = screen.getByText(/requested but not verified/)
    expect(note.textContent).toContain('not verified')
    expect(note.textContent).not.toMatch(/failed/i)
    expect(note.closest('.ui-marker-list__item')?.getAttribute('data-tone')).toBe('note')
  })

  it('a clean bind renders no caveat chrome', async () => {
    const { container } = renderPanel(
      fakeServices({
        status: vi
          .fn()
          .mockResolvedValue(statusFixture({}, { forwards: [runningRecord({ id: 'fwd-clean' })] })),
      }),
    )
    await waitFor(() => expect(screen.getByTestId('forwarded-row')).toBeTruthy())
    expect(screen.queryByText(/not verified/)).toBeNull()
    expect(container.querySelector('.ui-marker-list')).toBeNull()
  })
})

// ── The panel follows the ACTIVE tab (nocx-wzc4.7) ───────────────────────

describe('PortsPanel — active-tab scope', () => {
  it('a tab with no ports scope (Settings, alias) shows the no-connection state, never a stale host', async () => {
    const status = vi
      .fn()
      .mockResolvedValue(statusFixture({ state: 'available', listeners: [listenerFixture(22)] }))
    const services = fakeServices({ status })
    const [pid, setPid] = createSignal<string | null>('ssh:p1:1')
    renderPanel(services, { profileId: pid })
    await waitFor(() => expect(screen.getByText('0.0.0.0:22')).toBeTruthy())

    setPid(null)
    await waitFor(() => expect(screen.getByText('No active connection')).toBeTruthy())
    expect(screen.queryByText('0.0.0.0:22')).toBeNull()
    // No further backend calls while unscoped — nothing to sample for.
    expect(status).toHaveBeenCalledTimes(1)
  })

  it('switching profile discards the previous connection and re-scopes', async () => {
    const status = vi
      .fn()
      .mockResolvedValueOnce(
        statusFixture({ state: 'available', listeners: [listenerFixture(22)] }),
      )
      .mockResolvedValue(statusFixture({ state: 'available' }))
    const services = fakeServices({ status })
    const [pid, setPid] = createSignal<string | null>('ssh:p1:1')
    renderPanel(services, { profileId: pid })
    await waitFor(() => expect(screen.getByText('0.0.0.0:22')).toBeTruthy())

    setPid('ssh:p2:2')
    await waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p2:2'))
    // The first profile's listeners are gone; the second profile says nothing
    // is listening — the panel never shows one host under another's scope.
    await waitFor(() => expect(screen.getByText('Nothing is listening')).toBeTruthy())
    expect(screen.queryByText('0.0.0.0:22')).toBeNull()
  })

  it('a late response for a previous profile never paints over the new scope', async () => {
    let resolveP1!: (st: PortsStatusResult) => void
    const status = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<PortsStatusResult>((resolve) => {
            resolveP1 = resolve
          }),
      )
      .mockResolvedValue(statusFixture({ state: 'available' }))
    const services = fakeServices({ status })
    const [pid, setPid] = createSignal<string | null>('ssh:p1:1')
    renderPanel(services, { profileId: pid })

    setPid('ssh:p2:2')
    await waitFor(() => expect(status).toHaveBeenCalledWith('ssh:p2:2'))
    // The in-flight p1 request resolves late — with a listener row it must
    // never show under the p2 scope.
    resolveP1(statusFixture({ state: 'available', listeners: [listenerFixture(22)] }))
    await waitFor(() => expect(screen.getByText('Nothing is listening')).toBeTruthy())
    expect(screen.queryByText('0.0.0.0:22')).toBeNull()
  })

  it('reports visibility to the backend, retiring the previous profile on re-scope', async () => {
    const visible = vi.fn().mockResolvedValue({})
    const services = fakeServices({ visible })
    const [pid, setPid] = createSignal<string | null>('ssh:p1:1')
    const [vis, setVis] = createSignal(true)
    renderPanel(services, { profileId: pid, visible: vis })

    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', true))
    setVis(false)
    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', false))

    // Re-scope retires the previous profile's flag, then arms the new one
    // with the CURRENT visibility (false here).
    setPid('ssh:p2:2')
    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', false))
    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p2:2', false))
    setVis(true)
    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p2:2', true))
  })

  it('a hidden view stops the status poll; re-showing resumes it', async () => {
    vi.useFakeTimers()
    try {
      const status = vi.fn().mockResolvedValue(statusFixture({ state: 'available' }))
      const services = fakeServices({ status })
      const [vis, setVis] = createSignal(true)
      renderPanel(services, { visible: vis })
      await vi.advanceTimersByTimeAsync(0)

      setVis(false)
      await vi.advanceTimersByTimeAsync(0)
      const callsAfterHide = status.mock.calls.length

      // Two poll intervals elapse in the dark — no further status calls.
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 2)
      expect(status.mock.calls.length).toBe(callsAfterHide)

      setVis(true)
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 1)
      expect(status.mock.calls.length).toBeGreaterThan(callsAfterHide)
    } finally {
      vi.useRealTimers()
    }
  })
})

// ── The local machine (nocx-wzc4.8) ─────────────────────────────────────

describe('PortsPanel — the local machine (nocx-wzc4.8)', () => {
  it("a local tab scopes ports.* to the reserved 'local' target and shows this machine's listeners", async () => {
    const status = vi
      .fn()
      .mockResolvedValue(
        statusFixture(
          { state: 'available', listeners: [listenerFixture(22)] },
          { profileId: LOCAL_TARGET_ID, host: 'my-machine' },
        ),
      )
    const openForward = vi.fn()
    const services = fakeServices({ status, openForward })
    renderPanel(services, { profileId: () => LOCAL_TARGET_ID })

    await waitFor(() => expect(status).toHaveBeenCalledWith(LOCAL_TARGET_ID))
    await waitFor(() => expect(screen.getByText('0.0.0.0:22')).toBeTruthy())
    // The row offers copy-address; Forward is not offered on this machine.
    expect(screen.queryByTestId('ports-forward')).toBeNull()
    expect(screen.getByTestId('ports-copy')).toBeTruthy()
    // No forwarding vocabulary at all on a local scope — the sections would
    // be an empty offer of an impossible action.
    expect(screen.queryByText('Forwarded')).toBeNull()
    expect(screen.queryByText('Orphaned forwards')).toBeNull()
    expect(screen.queryByText('No active forwards')).toBeNull()
  })

  it('a local row copies the address and never dials tunnel.open', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const origClipboard = globalThis.navigator.clipboard
    Object.defineProperty(globalThis.navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    try {
      const openForward = vi.fn()
      const services = fakeServices({
        status: vi
          .fn()
          .mockResolvedValue(
            statusFixture(
              { state: 'available', listeners: [listenerFixture(22)] },
              { profileId: LOCAL_TARGET_ID, host: 'my-machine' },
            ),
          ),
        openForward,
      })
      renderPanel(services, { profileId: () => LOCAL_TARGET_ID })
      await waitFor(() => expect(screen.getByText('0.0.0.0:22')).toBeTruthy())

      fireEvent.click(screen.getByTestId('ports-copy'))
      await waitFor(() => expect(writeText).toHaveBeenCalledWith('my-machine:22'))
      expect(openForward).not.toHaveBeenCalled()
    } finally {
      Object.defineProperty(globalThis.navigator, 'clipboard', {
        value: origClipboard,
        configurable: true,
      })
    }
  })

  it('permission-denied evidence renders on a local row exactly as on a remote host', async () => {
    const services = fakeServices({
      status: vi
        .fn()
        .mockResolvedValue(
          statusFixture(
            { state: 'available', listeners: [listenerFixture(22, 'permission-denied')] },
            { profileId: LOCAL_TARGET_ID, host: 'my-machine' },
          ),
        ),
    })
    renderPanel(services, { profileId: () => LOCAL_TARGET_ID })

    // The same explanation as a remote host: a fact about privilege, not an
    // error on the user's own machine.
    await waitFor(() => expect(screen.getByTestId('ports-owners-note')).toBeTruthy())
    expect(screen.getByTestId('ports-owners-note').textContent).toMatch(/run as root/)
    expect(screen.getByText('0.0.0.0:22')).toBeTruthy()
  })

  it("a local tab pending before the first sample never says 'no connection'", async () => {
    const services = fakeServices({
      status: vi
        .fn()
        .mockResolvedValue(
          statusFixture({ state: 'pending' }, { profileId: LOCAL_TARGET_ID, host: '' }),
        ),
    })
    renderPanel(services, { profileId: () => LOCAL_TARGET_ID })

    // Connected-and-waiting is loading, not an empty state: the settle delay
    // plus a round trip is exactly the window a spinner is for (nocx-wzc4.11).
    await waitFor(() => expect(screen.getByTestId('ports-loading')).toBeTruthy())
    expect(screen.queryByText('No active connection')).toBeNull()
  })
})

// ── The unavailable-host empty state gets an action (W2) ─────────────────
// The panel says "Cannot see the ports on <host>" when the pane walked into
// a shell reached by hand; the action must make that sentence actionable —
// open the host as a nocx connection. AGENTS.md rule 1: the action is
// present and enabled from the state a user starts in, activating it reaches
// the seam with the host and user, and it offers itself nowhere else.

describe('PortsPanel — the unavailable-host empty state (W2)', () => {
  it('renders the empty state with an enabled action when unavailableIn names a host', async () => {
    const onOpenAsConnection = vi.fn()
    const { container } = renderPanel(fakeServices(), {
      profileId: () => null,
      unavailableIn: () => 'pi@192.168.0.93',
      onOpenAsConnection,
    })

    await waitFor(() => {
      expect(container.textContent ?? '').toContain('Cannot see the ports on pi@192.168.0.93')
    })
    const action = screen.getByTestId<HTMLButtonElement>('ports-open-as-connection')
    expect(action).toBeTruthy()
    expect(action.disabled).toBe(false)
  })

  it('activating it reaches the seam with the host and user split out of unavailableIn', async () => {
    const onOpenAsConnection = vi.fn()
    renderPanel(fakeServices(), {
      profileId: () => null,
      unavailableIn: () => 'pi@192.168.0.93',
      onOpenAsConnection,
    })
    await waitFor(() => expect(screen.getByTestId('ports-open-as-connection')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-open-as-connection'))
    await waitFor(() => expect(onOpenAsConnection).toHaveBeenCalledWith('192.168.0.93', 'pi'))
  })

  it('passes an undefined user when unavailableIn names a bare host', async () => {
    const onOpenAsConnection = vi.fn()
    renderPanel(fakeServices(), {
      profileId: () => null,
      unavailableIn: () => '192.168.0.93',
      onOpenAsConnection,
    })
    await waitFor(() => expect(screen.getByTestId('ports-open-as-connection')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-open-as-connection'))
    await waitFor(() => expect(onOpenAsConnection).toHaveBeenCalledWith('192.168.0.93', undefined))
  })

  it('offers no action when there is no unavailable host', async () => {
    const onOpenAsConnection = vi.fn()
    renderPanel(fakeServices(), {
      profileId: () => null,
      unavailableIn: () => '',
      onOpenAsConnection,
    })
    await waitFor(() => expect(screen.getByText('No active connection')).toBeTruthy())
    expect(screen.queryByTestId('ports-open-as-connection')).toBeNull()
    expect(onOpenAsConnection).not.toHaveBeenCalled()
  })

  it('offers no action when no seam is wired (a dead action would be worse than none)', async () => {
    renderPanel(fakeServices(), {
      profileId: () => null,
      unavailableIn: () => 'pi@192.168.0.93',
    })
    await waitFor(() => {
      expect(screen.getByText(/Cannot see the ports/)).toBeTruthy()
    })
    expect(screen.queryByTestId('ports-open-as-connection')).toBeNull()
  })
})

afterEach(() => {
  clearToasts()
})

// ── The filter (nocx-cdub) ──────────────────────────────────────────────
// A busy host lists dozens of listeners; finding one port means scanning the
// rail. The filter is the kit's SearchField — the one search vocabulary, the
// one connections.tsx and secrets.tsx already drive. What it matches: the
// rendered address (which carries the port), and the process name when the
// probe could name it — never the pid, which is restart-unstable and not a
// thing a user types, and matching it lets a query hit a row nobody meant
// (decision 1). A query matching nothing SAYS SO — a heading with nothing
// under it is what the owner reads as broken (decision 4 of the previous
// beads). And a row that carries a live or self-stopped forward is never
// filtered out: the filter exists to find rows, never to strand the Stop or
// Retry action (decision 3).
const threeListeners: PortsStatusResult['discovery']['listeners'] = [
  {
    family: 'ipv4',
    address: '192.168.0.93',
    port: 9993,
    process: { evidence: 'known', name: 'pihole', pid: 111 },
  },
  {
    family: 'ipv4',
    address: '127.0.0.1',
    port: 53,
    process: { evidence: 'known', name: 'node', pid: 222 },
  },
  {
    family: 'ipv4',
    address: '0.0.0.0',
    port: 22,
    process: { evidence: 'known', name: 'sshd', pid: 333 },
  },
]

describe('PortsPanel — the filter (nocx-cdub)', () => {
  it('a port number finds the row a user expects', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')
    fireEvent.input(field, { target: { value: '9993' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    expect(screen.getByText('192.168.0.93:9993')).toBeTruthy()
    expect(screen.queryByText('127.0.0.1:53')).toBeNull()
    expect(screen.queryByText('0.0.0.0:22')).toBeNull()
  })

  it('a partial address finds the row', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')
    fireEvent.input(field, { target: { value: '192.168' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    expect(screen.getByText('192.168.0.93:9993')).toBeTruthy()
  })

  it('a process name finds the row — and a pid does not', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')

    // The most natural query for "is pihole still listening" is the process
    // name — matching only the address would fail it.
    fireEvent.input(field, { target: { value: 'pihole' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    expect(screen.getByText('192.168.0.93:9993')).toBeTruthy()

    // The pid is deliberately not matched: '111' is pihole's pid, and a
    // query that hits it is a hit nobody meant.
    fireEvent.input(field, { target: { value: '111' } })
    await waitFor(() => expect(screen.queryAllByTestId('detected-row')).toHaveLength(0))
    expect(screen.getByText('No ports match that')).toBeTruthy()
  })

  it('a query matching nothing says so instead of an empty heading', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')
    fireEvent.input(field, { target: { value: 'zzz-nothing' } })
    await waitFor(() => expect(screen.queryAllByTestId('detected-row')).toHaveLength(0))
    expect(screen.getByText('No ports match that')).toBeTruthy()
  })

  it('clearing the filter restores the full list', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')
    fireEvent.input(field, { target: { value: '9993' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    fireEvent.input(field, { target: { value: '' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
  })

  it("a live forward's row is never filtered out — Stop stays reachable", async () => {
    const fwd = runningRecord({
      destination: '192.168.0.93:9993',
      actualBind: { host: '127.0.0.1', port: 9993 },
      requestedBind: { host: '127.0.0.1', port: 9993 },
    })
    const services = fakeServices({
      status: vi
        .fn()
        .mockResolvedValue(statusFixture({ listeners: threeListeners }, { forwards: [fwd] })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')
    fireEvent.input(field, { target: { value: 'zzz-nothing' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    // The surviving row is the forwarded one, and its Stop action is on
    // screen — a user who filters, forwards, then queries again can still
    // stop what they started (decision 3).
    expect(screen.getByTestId('ports-stop')).toBeTruthy()
    expect(screen.queryByText('No ports match that')).toBeNull()
  })

  it("a self-stopped forward's row is never filtered out — Retry stays reachable", async () => {
    const fwd = runningRecord({
      destination: '192.168.0.93:9993',
      actualBind: { host: '127.0.0.1', port: 9993 },
      requestedBind: { host: '127.0.0.1', port: 9993 },
      state: 'stopped',
      stopReason: 'error',
      error: 'connection reset',
    })
    const services = fakeServices({
      status: vi
        .fn()
        .mockResolvedValue(statusFixture({ listeners: threeListeners }, { forwards: [fwd] })),
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(3))
    const field = screen.getByLabelText('Filter ports')
    fireEvent.input(field, { target: { value: 'zzz-nothing' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    expect(screen.getByTestId('ports-retry-forward')).toBeTruthy()
    expect(screen.queryByText('No ports match that')).toBeNull()
  })
})
