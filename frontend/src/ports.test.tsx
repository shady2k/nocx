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
import { batch, createSignal } from 'solid-js'
import { clearToasts } from './ui/toast'
import { RpcError, TransportUnavailableError } from './dispatcher'
import type { PaneConnection } from './generated/pane.connection'
import type { ActiveOrigin } from './pane-content'
import type { TunnelOpenResult } from './generated/tunnel.open'
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
  ...over,
})

const statusFixture = (
  over: Partial<PortsStatusResult['discovery']> = {},
  extra: Partial<PortsStatusResult> = {},
): PortsStatusResult => ({
  paneId: 'ssh:p1:1',
  host: 'host.example',
  discovery: discoveryFixture(over),
  ...extra,
})

const LOCAL_PANE = 'local-pane'

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
    listForwards: vi.fn().mockResolvedValue({ forwards: [] }),
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
  // Built as ONE object rather than spread into the JSX: a partial spread
  // makes every prop optional in the resulting type, and `activeOrigin` is
  // required — a panel handed no axis would gate on nothing and ask a raw
  // host for ports (ALX-1852).
  const props: PortsPanelProps = {
    paneId: () => 'ssh:p1:1',
    savedProfileId: () => (over.paneId?.() === LOCAL_PANE ? null : 'ssh:p1:1'),
    isLocal: () => over.paneId?.() === LOCAL_PANE,
    // No origin by default: this suite's axis cases hand one in, and the
    // rest model a surface with nothing to gate on.
    activeOrigin: () => null,
    services,
    visible: () => true,
    pause,
    filter,
    ...over,
  }
  return render(() => (
    <>
      <PortsFilter />
      <PortsPanel {...props} />
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
      .mockRejectedValueOnce(
        new RpcError('listen tcp 127.0.0.1:6768: bind: address already in use', -32603, {
          reason: 'address-in-use',
        }),
      )
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

  it('an "address already in use" MESSAGE alone is not a reason to retry', async () => {
    // The old rule was a regex over this exact prose. A plain Error carries
    // no typed cause (contracts/tunnel.error.schema.json), so the panel must
    // report the failure rather than silently opening a second port.
    const openForward = vi
      .fn()
      .mockRejectedValueOnce(new Error('listen tcp 127.0.0.1:6768: bind: address already in use'))
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
      openForward,
    })
    renderPanel(services)
    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-forward'))

    // The failure reaching the person is the observable the retry would have
    // pre-empted; assert on it, then on the single call.
    await waitFor(() =>
      expect(listingToasts.some((t) => /address already in use/.test(t.message))).toBe(true),
    )
    expect(openForward).toHaveBeenCalledTimes(1)
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

  it('retries visibility when the first status arrives after the helper is ready', async () => {
    const visible = vi
      .fn()
      .mockRejectedValueOnce(new Error('helper unavailable'))
      .mockResolvedValue({})
    const status = vi.fn().mockResolvedValue(statusFixture({ visible: false }))
    renderPanel(fakeServices({ status, visible }))

    await waitFor(() => expect(visible).toHaveBeenCalledTimes(2))
    expect(visible).toHaveBeenLastCalledWith('ssh:p1:1', true)
  })

  it('a backend-reported pause stops the status poll and keeps the forward list current', async () => {
    // The header action is Refresh now, so nothing in the renderer flips this;
    // the control only REFLECTS a pause the backend reports, and the poll must
    // still honour it.
    vi.useFakeTimers()
    try {
      const status = vi.fn().mockResolvedValue(statusFixture({ state: 'available', paused: true }))
      const listForwards = vi.fn().mockResolvedValue({ forwards: [] })
      renderPanel(fakeServices({ status, listForwards }))
      await vi.advanceTimersByTimeAsync(0)
      const listsAfterFirst = listForwards.mock.calls.length
      expect(listsAfterFirst).toBeGreaterThan(0)
      const callsAfterFirst = status.mock.calls.length
      expect(callsAfterFirst).toBeGreaterThan(0)

      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 2)
      expect(status.mock.calls.length).toBe(callsAfterFirst)
      expect(listForwards).toHaveBeenCalledTimes(listsAfterFirst + 2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('an unavailable probe offers no Retry or duplicate toolbar controls', async () => {
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

    expect(screen.queryByText('Retry')).toBeNull()
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
      // A socket that dropped is the dispatcher's OWN class — the panel reads
      // that, never the words "closed" or "not connected" (C26).
      const status = vi.fn().mockRejectedValue(new TransportUnavailableError())
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
      // The failure persists across polls — the toast must not restack.
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3)
      expect(listingToasts.length).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('an untyped failure is quoted into ONE open sentence — its prose decides nothing', async () => {
    // The sentence a failure earns is chosen by the wire's reason, the
    // dispatcher's typed loss, and nothing else (C26). A message that spells
    // a lost connection in English earns the OPEN sentence, with the
    // backend's own words appended so the diagnostic survives.
    vi.useFakeTimers()
    try {
      const status = vi.fn().mockRejectedValue(new Error('rpc: tunnel is closed'))
      renderPanel(fakeServices({ status }))
      await vi.advanceTimersByTimeAsync(0)
      await waitFor(() => expect(listingToasts.length).toBe(1))
      expect(listingToasts[0].message).toBe('Could not read the ports (rpc: tunnel is closed).')
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
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(5432)] })),
      listForwards: vi.fn().mockResolvedValue({
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
      }),
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
      listForwards: vi.fn().mockResolvedValue({
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
      }),
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
    const dest = '192.0.2.93:9993'
    const services = fakeServices({
      listForwards: vi.fn().mockResolvedValue({
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
      }),
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
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
      listForwards: vi.fn().mockResolvedValue({
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
      }),
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
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(5432)] })),
      listForwards: vi.fn().mockResolvedValue({
        forwards: [
          {
            ...runningRecord({ id: 'fwd-user', destination: 'host.example:5432' }),
            state: 'stopped' as const,
            stopReason: 'user' as const,
            error: null,
          },
        ],
      }),
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
      listForwards: vi.fn().mockResolvedValue({
        forwards: [
          runningRecord({
            id: 'fwd-orphan',
            destination: 'host.example:9999',
            actualBind: { host: '127.0.0.1', port: 9999 },
          }),
        ],
      }),
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
      listForwards: vi.fn().mockResolvedValue({
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
      }),
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

  it('draws the shared axis card, not a line of its own, when a read says helper-unavailable', async () => {
    // The race ALX-1852 closes: the answer arrives before the connection's
    // own observation has caught up. The panel draws the SAME card Files and
    // Git draw — its waiting state, because no axis sentence has arrived to
    // quote — and raises no toast.
    const record = runningRecord({ id: 'fwd-orphan' })
    const status = vi
      .fn()
      .mockRejectedValue(
        new RpcError('helper unavailable', -32602, { reason: 'helper-unavailable' }),
      )
    const listForwards = vi
      .fn()
      .mockResolvedValueOnce({ forwards: [record] })
      .mockResolvedValue({ forwards: [] })
    const stopForward = vi
      .fn()
      .mockResolvedValue({ ...record, state: 'stopped', stopReason: 'user' })
    renderPanel(fakeServices({ status, listForwards, stopForward }))

    const card = await screen.findByTestId('ports-helper-axis')
    expect(card.closest('section')?.textContent).toContain('Detected')
    expect(card.getAttribute('data-helper')).toBe('starting')
    expect(screen.getByText('Orphaned forwards')).toBeTruthy()
    expect(screen.getByTestId('forwarded-row').getAttribute('data-state')).toBe('forwarded')
    expect(screen.queryByTestId('ports-loading')).toBeNull()
    expect(listingToasts).toEqual([])

    fireEvent.click(screen.getByTestId('ports-stop'))
    await waitFor(() => expect(stopForward).toHaveBeenCalledWith(record.id))
    await waitFor(() => expect(screen.queryByTestId('forwarded-row')).toBeNull())
  })

  it('shows detected listeners when reading forwards fails', async () => {
    const status = vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] }))
    const listForwards = vi.fn().mockRejectedValue(new Error('forward list unavailable'))
    renderPanel(fakeServices({ status, listForwards }))

    expect(await screen.findByText('0.0.0.0:6768')).toBeTruthy()
    expect(listForwards).toHaveBeenCalled()
  })

  it('a clean bind renders no caveat chrome', async () => {
    const { container } = renderPanel(
      fakeServices({
        listForwards: vi.fn().mockResolvedValue({ forwards: [runningRecord({ id: 'fwd-clean' })] }),
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
    renderPanel(services, { paneId: pid })
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
    renderPanel(services, { paneId: pid })
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
    renderPanel(services, { paneId: pid })

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
    renderPanel(services, { paneId: pid, visible: vis })

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
          { paneId: LOCAL_PANE, host: 'my-machine' },
        ),
      )
    const openForward = vi.fn()
    const listForwards = vi.fn().mockResolvedValue({ forwards: [] })
    const services = fakeServices({ status, openForward, listForwards })
    renderPanel(services, { paneId: () => LOCAL_PANE })

    await waitFor(() => expect(status).toHaveBeenCalledWith(LOCAL_PANE))
    await waitFor(() => expect(screen.getByText('0.0.0.0:22')).toBeTruthy())
    // The row offers copy-address; Forward is not offered on this machine.
    expect(screen.queryByTestId('ports-forward')).toBeNull()
    expect(screen.getByTestId('ports-copy')).toBeTruthy()
    expect(listForwards).not.toHaveBeenCalled()
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
              { paneId: LOCAL_PANE, host: 'my-machine' },
            ),
          ),
        openForward,
      })
      renderPanel(services, { paneId: () => LOCAL_PANE })
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
            { paneId: LOCAL_PANE, host: 'my-machine' },
          ),
        ),
    })
    renderPanel(services, { paneId: () => LOCAL_PANE })

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
        .mockResolvedValue(statusFixture({ state: 'pending' }, { paneId: LOCAL_PANE, host: '' })),
    })
    renderPanel(services, { paneId: () => LOCAL_PANE })

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
      paneId: () => null,
      unavailableIn: () => 'pi@192.0.2.93',
      onOpenAsConnection,
    })

    await waitFor(() => {
      expect(container.textContent ?? '').toContain('Cannot see the ports on pi@192.0.2.93')
    })
    const action = screen.getByTestId<HTMLButtonElement>('ports-open-as-connection')
    expect(action).toBeTruthy()
    expect(action.disabled).toBe(false)
  })

  it('activating it reaches the seam with the host and user split out of unavailableIn', async () => {
    const onOpenAsConnection = vi.fn()
    renderPanel(fakeServices(), {
      paneId: () => null,
      unavailableIn: () => 'pi@192.0.2.93',
      onOpenAsConnection,
    })
    await waitFor(() => expect(screen.getByTestId('ports-open-as-connection')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-open-as-connection'))
    await waitFor(() => expect(onOpenAsConnection).toHaveBeenCalledWith('192.0.2.93', 'pi'))
  })

  it('passes an undefined user when unavailableIn names a bare host', async () => {
    const onOpenAsConnection = vi.fn()
    renderPanel(fakeServices(), {
      paneId: () => null,
      unavailableIn: () => '192.0.2.93',
      onOpenAsConnection,
    })
    await waitFor(() => expect(screen.getByTestId('ports-open-as-connection')).toBeTruthy())

    fireEvent.click(screen.getByTestId('ports-open-as-connection'))
    await waitFor(() => expect(onOpenAsConnection).toHaveBeenCalledWith('192.0.2.93', undefined))
  })

  it('offers no action when there is no unavailable host', async () => {
    const onOpenAsConnection = vi.fn()
    renderPanel(fakeServices(), {
      paneId: () => null,
      unavailableIn: () => '',
      onOpenAsConnection,
    })
    await waitFor(() => expect(screen.getByText('No active connection')).toBeTruthy())
    expect(screen.queryByTestId('ports-open-as-connection')).toBeNull()
    expect(onOpenAsConnection).not.toHaveBeenCalled()
  })

  it('offers no action when no seam is wired (a dead action would be worse than none)', async () => {
    renderPanel(fakeServices(), {
      paneId: () => null,
      unavailableIn: () => 'pi@192.0.2.93',
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
    address: '192.0.2.93',
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
    expect(screen.getByText('192.0.2.93:9993')).toBeTruthy()
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
    fireEvent.input(field, { target: { value: '192.0.2' } })
    await waitFor(() => expect(screen.getAllByTestId('detected-row')).toHaveLength(1))
    expect(screen.getByText('192.0.2.93:9993')).toBeTruthy()
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
    expect(screen.getByText('192.0.2.93:9993')).toBeTruthy()

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
      destination: '192.0.2.93:9993',
      actualBind: { host: '127.0.0.1', port: 9993 },
      requestedBind: { host: '127.0.0.1', port: 9993 },
    })
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
      listForwards: vi.fn().mockResolvedValue({ forwards: [fwd] }),
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
      destination: '192.0.2.93:9993',
      actualBind: { host: '127.0.0.1', port: 9993 },
      requestedBind: { host: '127.0.0.1', port: 9993 },
      state: 'stopped',
      stopReason: 'error',
      error: 'connection reset',
    })
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: threeListeners })),
      listForwards: vi.fn().mockResolvedValue({ forwards: [fwd] }),
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

// ── The helper axis (nocx-oalo1.13.4, ALX-1852) ───────────────────────────
// The panel's host answer is gated on the connection's helper axis — the same
// `helperGate` Files and Git gate on — while the ledger (`tunnel.list`) is
// read whatever the axis says, because forwards live in the backend's own
// record and not on the host. What a person gets: no spinner and no toast on
// a host whose helper is not running, the ONE card every other panel draws
// with its reason and its action, a forward still visible and stoppable under
// it, and the ports back on screen by themselves the moment the helper
// serves again.
const SSH_PANE = 'ssh:p1:1'

function sshOrigin(helper: PaneConnection['helper']): ActiveOrigin {
  return {
    paneId: 1,
    sessionId: 'session-1',
    kind: 'ssh',
    cwd: '/home/dev',
    cwdVerified: true,
    cwdFollow: true,
    host: 'host.example',
    machine: 'dev@host.example',
    connection: { paneId: SSH_PANE, connectionId: 'conn-1', helper },
  }
}

/** Every reason the connection contract's closed enum can carry, with the
 *  field a person reads in the fold's words (connection/helper-axis.ts). One
 *  table, so a reason added to the wire fails HERE rather than reaching a
 *  window as a blank card. */
const BLOCKED_REASONS: Array<{
  helper: PaneConnection['helper']
  sentence: string
}> = [
  {
    helper: { state: 'paused', reason: 'connection', action: 'none' },
    sentence: 'The connection is paused, so the nocx helper is not reachable.',
  },
  {
    helper: { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' },
    sentence: 'This host uses Raw integration, so the nocx helper is not used.',
  },
  {
    helper: { state: 'unavailable', reason: 'mode-script', action: 'host-settings' },
    sentence: 'This host uses Script integration, so the nocx helper is not used.',
  },
  {
    helper: { state: 'unavailable', reason: 'helper-lost', action: 'refresh' },
    sentence: 'The nocx helper stopped on this connection.',
  },
  {
    helper: { state: 'unavailable', reason: 'consent-pending', action: 'none' },
    sentence: 'The nocx helper is waiting for permission on this host.',
  },
  {
    helper: { state: 'unavailable', reason: 'consent-denied', action: 'none' },
    sentence: 'The nocx helper is not allowed on this host.',
  },
  {
    helper: { state: 'unavailable', reason: 'unsupported-platform', action: 'none' },
    sentence: 'This host cannot run the nocx helper.',
  },
  {
    helper: { state: 'unavailable', reason: 'install-failed', action: 'none' },
    sentence: 'The nocx helper could not be installed on this host.',
  },
  {
    helper: { state: 'unavailable', reason: 'exec-forbidden', action: 'none' },
    sentence: 'The host refused to run the nocx helper.',
  },
  {
    helper: { state: 'unavailable', reason: 'version-mismatch', action: 'none' },
    sentence: 'The nocx helper on this host is a different version.',
  },
]

describe('PortsPanel — the helper axis (nocx-oalo1.13.4)', () => {
  it('every closed-enum reason draws the shared card, in the fold’s own words', () => {
    for (const { helper, sentence } of BLOCKED_REASONS) {
      const { unmount } = renderPanel(fakeServices(), {
        activeOrigin: () => sshOrigin(helper),
      })
      const card = screen.getByTestId('ports-helper-axis')
      expect(card.closest('section')?.textContent).toContain('Detected')
      expect(card.textContent).toContain(sentence)
      // The wire's vocabulary never reaches a window.
      expect(card.textContent).not.toMatch(/mode-raw|mode-script|helper-lost|consent-/)
      unmount()
      cleanup()
    }
  })

  it('draws no ports.* answer at all while the gate is closed — but keeps reading the ledger', async () => {
    const record = runningRecord({ id: 'fwd-orphan', destination: 'host.example:9999' })
    const listForwards = vi.fn().mockResolvedValue({ forwards: [record] })
    const status = vi.fn().mockResolvedValue(statusFixture())
    const sample = vi.fn().mockResolvedValue(statusFixture())
    const visible = vi.fn().mockResolvedValue({})
    const pause = vi.fn().mockResolvedValue({})
    const services = fakeServices({ listForwards, status, sample, visible, pause })
    renderPanel(services, {
      activeOrigin: () =>
        sshOrigin({ state: 'unavailable', reason: 'mode-raw', action: 'host-settings' }),
    })

    const card = await screen.findByTestId('ports-helper-axis')
    expect(card.textContent).toContain('This host uses Raw integration')
    // The ledger answers whatever the axis says (ALX-1852): the forward is
    // on screen under the card and stays stoppable.
    await waitFor(() => expect(screen.getByText('Orphaned forwards')).toBeTruthy())
    expect(screen.getByText(/host.example:9999/)).toBeTruthy()
    expect(listForwards).toHaveBeenCalled()

    expect(status).not.toHaveBeenCalled()
    expect(sample).not.toHaveBeenCalled()
    expect(visible).not.toHaveBeenCalled()
    expect(pause).not.toHaveBeenCalled()
    expect(screen.queryByTestId('ports-loading')).toBeNull()
    expect(listingToasts).toEqual([])
  })

  it('returns by itself: the axis serves again and the ports are read with nobody pressing anything', async () => {
    const [origin, setOrigin] = createSignal(sshOrigin({ state: 'starting', action: 'none' }))
    // A forward for a destination no listener owns: it stays in the ledger
    // across the gate, which is the map this test proves is not discarded.
    const record = runningRecord({ id: 'fwd-live', destination: 'host.example:9999' })
    const status = vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] }))
    const visible = vi.fn().mockResolvedValue({})
    const listForwards = vi.fn().mockResolvedValue({ forwards: [record] })
    const services = fakeServices({ status, visible, listForwards })
    renderPanel(services, { activeOrigin: origin })

    // `starting` is the waiting card, and still nothing leaves.
    await waitFor(() =>
      expect(screen.getByTestId('ports-helper-axis').getAttribute('data-helper')).toBe('starting'),
    )
    expect(status).not.toHaveBeenCalled()

    setOrigin(sshOrigin({ state: 'serving', action: 'none' }))

    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())
    expect(status).toHaveBeenCalledWith(SSH_PANE)
    // The visibility flag the gate held back is sent on the way in, with the
    // visibility the tab has NOW — and no button was pressed to cause it.
    expect(visible).toHaveBeenCalledWith(SSH_PANE, true)
    // The forward map is not discarded on the way through the gate.
    expect(screen.queryByTestId('ports-helper-axis')).toBeNull()
    expect(listForwards).toHaveBeenCalled()
  })

  it('an ad-hoc tab (no saved profile) offers no Forward button', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
    })
    renderPanel(services, {
      savedProfileId: () => null,
      activeOrigin: () => sshOrigin({ state: 'serving', action: 'none' }),
    })
    await waitFor(() => expect(screen.getByText('0.0.0.0:6768')).toBeTruthy())
    // Forward is `tunnel.open`, which is addressed by a saved profile: a
    // hand-typed tab has no host record to forward from, so the control is
    // absent from the DOM rather than dead (D14).
    expect(screen.queryByTestId('ports-forward')).toBeNull()
    expect(screen.getByTestId('ports-copy')).toBeTruthy()
  })

  it('a running forward on an ad-hoc tab keeps its Stop — only Forward and Retry need a profile', async () => {
    const record = runningRecord({ id: 'fwd-adhoc', destination: 'host.example:6768' })
    const stopForward = vi
      .fn()
      .mockResolvedValue({ ...record, state: 'stopped', stopReason: 'user', error: null })
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
      listForwards: vi.fn().mockResolvedValue({ forwards: [record] }),
      stopForward,
    })
    renderPanel(services, {
      savedProfileId: () => null,
      activeOrigin: () => sshOrigin({ state: 'serving', action: 'none' }),
    })

    await waitFor(() => expect(screen.getByText(/127.0.0.1:6768/)).toBeTruthy())
    // Stop is addressed by the forward's own id, so it is a working action on
    // a tab with no host record — and the only way out of a forward whose
    // target the person can no longer re-open (ALX-1852 r2).
    fireEvent.click(screen.getByTestId('ports-stop'))
    await waitFor(() => expect(stopForward).toHaveBeenCalledWith('fwd-adhoc'))
    expect(screen.queryByTestId('ports-forward')).toBeNull()
  })

  it('a forward the last serving sample owned keeps its Stop when the gate closes', async () => {
    const [origin, setOrigin] = createSignal(sshOrigin({ state: 'serving', action: 'none' }))
    const record = runningRecord({ id: 'fwd-live', destination: 'host.example:6768' })
    const stopForward = vi
      .fn()
      .mockResolvedValue({ ...record, state: 'stopped', stopReason: 'user', error: null })
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] })),
      listForwards: vi.fn().mockResolvedValue({ forwards: [record] }),
      stopForward,
    })
    renderPanel(services, { activeOrigin: origin })

    // While the host serves, the forward lives on its detected row and the
    // orphan section has no subject.
    await waitFor(() => expect(screen.getByTestId('ports-stop')).toBeTruthy())
    expect(screen.queryByText('Orphaned forwards')).toBeNull()

    setOrigin(sshOrigin({ state: 'unavailable', reason: 'helper-lost', action: 'refresh' }))

    // The Detected section is gone, and the row that owned the forward with
    // it. The cached listener must not keep claiming the forward: an orphan
    // is the only row left, and a running one is stoppable (ALX-1852 r2).
    await waitFor(() => expect(screen.getByText('Orphaned forwards')).toBeTruthy())
    expect(screen.getByTestId('ports-helper-axis')).toBeTruthy()
    const orphans = screen.getAllByTestId('forwarded-row')
    expect(orphans).toHaveLength(1)
    expect(orphans[0].textContent).toContain('host.example:6768')

    fireEvent.click(screen.getByTestId('ports-stop'))
    await waitFor(() => expect(stopForward).toHaveBeenCalledWith('fwd-live'))
  })

  it('a typed refusal does not silence the reason and the action that arrive after it', async () => {
    const [origin, setOrigin] = createSignal(sshOrigin({ state: 'serving', action: 'none' }))
    const onRefreshHelper = vi.fn()
    // The refusal reaches the panel before the connection's own observation
    // does, which is the order ALX-1852 is about.
    const status = vi
      .fn()
      .mockRejectedValue(
        new RpcError('helper unavailable', -32602, { reason: 'helper-unavailable' }),
      )
    renderPanel(fakeServices({ status }), { activeOrigin: origin, onRefreshHelper })

    // Nothing has named a reason yet, so the card WAITS — never a toast and
    // never a spinner that would never end.
    await waitFor(() =>
      expect(screen.getByTestId('ports-helper-axis').getAttribute('data-helper')).toBe('starting'),
    )
    expect(listingToasts).toEqual([])

    setOrigin(sshOrigin({ state: 'unavailable', reason: 'helper-lost', action: 'refresh' }))

    // The observation is now the authority, and it carries the backend's
    // action: a card that kept drawing the waiting state here left the person
    // with no way back, on a poll that had already stopped.
    await waitFor(() =>
      expect(screen.getByTestId('ports-helper-axis').getAttribute('data-helper')).toBe(
        'unavailable',
      ),
    )
    expect(screen.getByTestId('ports-helper-axis').textContent).toContain(
      'The nocx helper stopped on this connection.',
    )
    fireEvent.click(screen.getByTestId('ports-helper-refresh'))
    expect(onRefreshHelper).toHaveBeenCalledWith(SSH_PANE)
  })

  it('leaving a serving tab for a blocked one still retires the tab it left', async () => {
    const [pane, setPane] = createSignal<string | null>('ssh:p1:1')
    const [origin, setOrigin] = createSignal(sshOrigin({ state: 'serving', action: 'none' }))
    const visible = vi.fn().mockResolvedValue({})
    renderPanel(fakeServices({ visible }), {
      paneId: pane,
      activeOrigin: origin,
      savedProfileId: () => 'ssh:p1:1',
    })

    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', true))
    visible.mockClear()

    // One tab switch, one batch: the new target's axis is already blocked
    // when the panel re-scopes, which is the shape that used to skip the
    // retirement of the tab it left.
    batch(() => {
      setPane('ssh:p2:2')
      setOrigin(sshOrigin({ state: 'unavailable', reason: 'mode-raw', action: 'host-settings' }))
    })
    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', false))
    // The target it cannot ask is asked nothing.
    expect(visible).not.toHaveBeenCalledWith('ssh:p2:2', true)
  })

  it('moving between two gated tabs owes the backend nothing at all', async () => {
    const [pane, setPane] = createSignal<string | null>('ssh:p1:1')
    const listForwards = vi.fn().mockResolvedValue({ forwards: [] })
    const visible = vi.fn().mockResolvedValue({})
    renderPanel(fakeServices({ listForwards, visible }), {
      paneId: pane,
      activeOrigin: () =>
        sshOrigin({ state: 'unavailable', reason: 'mode-raw', action: 'host-settings' }),
      savedProfileId: () => 'ssh:p1:1',
    })

    // A gated panel draws the card and still reads the ledger; the ledger read
    // is the re-scope's own observable, and it lands after the visibility
    // decision the same batch made.
    await waitFor(() => expect(screen.getByTestId('ports-helper-axis')).toBeTruthy())
    await waitFor(() => expect(listForwards.mock.calls.length).toBeGreaterThan(0))
    visible.mockClear()

    setPane('ssh:p2:2')
    await waitFor(() => expect(listForwards.mock.calls.length).toBeGreaterThan(1))

    // NEITHER TAB WAS EVER ARMED, so neither owes a retirement: a `false` for
    // the tab the panel left would be a `ports.*` behind closed gates, and
    // ALX-1852 holds the panel to zero of those (ALX-1865 r3).
    expect(visible).not.toHaveBeenCalled()
  })

  it('a panel that goes away gives back the visibility it armed', async () => {
    const visible = vi.fn().mockResolvedValue({})
    const panel = renderPanel(fakeServices({ visible }), {
      activeOrigin: () => sshOrigin({ state: 'serving', action: 'none' }),
    })
    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', true))
    visible.mockClear()

    // The sidebar is switched away from Ports, or closed, while this tab is the
    // visible one. The flag the panel armed is the panel's to give back, and a
    // scheduler left armed keeps sampling a host nobody watches.
    panel.unmount()

    await waitFor(() => expect(visible).toHaveBeenCalledWith('ssh:p1:1', false))
    expect(visible).toHaveBeenCalledTimes(1)
  })

  it('a status that lands after the gate closed does not arm the scheduler it answers', async () => {
    vi.useFakeTimers()
    try {
      const [origin, setOrigin] = createSignal(sshOrigin({ state: 'serving', action: 'none' }))
      let resolveStatus!: (st: PortsStatusResult) => void
      const status = vi.fn(
        () =>
          new Promise<PortsStatusResult>((res) => {
            resolveStatus = res
          }),
      )
      const visible = vi.fn().mockResolvedValue({})
      renderPanel(fakeServices({ status, visible }), { activeOrigin: origin })
      await vi.advanceTimersByTimeAsync(0)
      expect(status).toHaveBeenCalled()
      visible.mockClear()

      // The answer left while the panel was serving and lands after the
      // observation closed the gate. The tab is still the same tab, so an
      // identity check alone passes — the gate is the second half.
      setOrigin(sshOrigin({ state: 'unavailable', reason: 'helper-lost', action: 'refresh' }))
      resolveStatus(statusFixture({ visible: false }))
      await vi.advanceTimersByTimeAsync(0)

      // The gate closing RETIRES the flag the panel armed — a `false` owed to
      // the target it armed, not an ask — so the answer is not what may arm a
      // scheduler on a host this panel may no longer ask (ALX-1865 r3).
      expect(visible).not.toHaveBeenCalledWith('ssh:p1:1', true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('chooses the sentence by the wire’s reason, never by the message’s prose', async () => {
    vi.useFakeTimers()
    try {
      // A refusal the wire typed: `unknown-pane` has a sentence of its own.
      const unknownPane = vi
        .fn()
        .mockRejectedValue(new RpcError('Invalid params', -32602, { reason: 'unknown-pane' }))
      const first = renderPanel(fakeServices({ status: unknownPane }))
      await vi.advanceTimersByTimeAsync(0)
      await waitFor(() => expect(listingToasts.length).toBe(1))
      expect(listingToasts[0].message).toBe(
        'Could not read the ports — this tab is no longer a connection nocx can ask.',
      )
      first.unmount()
      cleanup()
      listingToasts.length = 0

      // The same words WITHOUT the typed cause decide nothing: the prose
      // that used to be matched ("invalid params", "not available", "closed")
      // is quoted into the one open sentence.
      const prose = vi
        .fn()
        .mockRejectedValue(new Error('Invalid params: port discovery is not available, ws closed'))
      renderPanel(fakeServices({ status: prose }))
      await vi.advanceTimersByTimeAsync(0)
      await waitFor(() => expect(listingToasts.length).toBe(1))
      expect(listingToasts[0].message).toBe(
        'Could not read the ports (Invalid params: port discovery is not available, ws closed).',
      )
    } finally {
      vi.useRealTimers()
    }
  })

  it('a refusal the observation has not caught up with is retried, and clears itself', async () => {
    // The race ALX-1852 closes has TWO endings, and the second is the one a
    // card that silenced the poll would lose: the backend said the helper was
    // gone, the observation still says it serves, and the next tick must be
    // allowed to ask again — otherwise a helper that came back is invisible
    // until the person switches tabs.
    vi.useFakeTimers()
    try {
      const refusal = (): RpcError =>
        new RpcError('helper unavailable', -32602, { reason: 'helper-unavailable' })
      // Two refusals, then an answer: the observation lags long enough for the
      // card to be a state a person sees, and then catches up.
      const status = vi
        .fn()
        .mockRejectedValueOnce(refusal())
        .mockRejectedValueOnce(refusal())
        .mockResolvedValue(statusFixture({ listeners: [listenerFixture(6768)] }))
      renderPanel(fakeServices({ status }), {
        activeOrigin: () => sshOrigin({ state: 'serving', action: 'none' }),
      })

      // Under fake timers nothing waits: each advance flushes the microtasks
      // an answered call resolves in, which is the whole observable here.
      await vi.advanceTimersByTimeAsync(0)
      expect(screen.getByTestId('ports-helper-axis')).toBeTruthy()
      // ONE retry per refusal, and no more: the card state is also a gate the
      // effect watches, so a refusal that keeps arriving cannot spin.
      expect(status.mock.calls.length).toBeLessThanOrEqual(2)

      // The tick asks again — the gate that decides what may LEAVE is the
      // observation's, and it says serving — so the answer clears the card
      // with nobody pressing anything.
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS)
      expect(screen.getByText('0.0.0.0:6768')).toBeTruthy()
      expect(screen.queryByTestId('ports-helper-axis')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('a probe-less host names the state in a fixed sentence, not the backend’s classification', async () => {
    const services = fakeServices({
      status: vi.fn().mockResolvedValue(
        statusFixture({
          state: 'unavailable',
          classification: 'no probe tool usable on this host',
        }),
      ),
    })
    renderPanel(services, { activeOrigin: () => sshOrigin({ state: 'serving', action: 'none' }) })
    await waitFor(() => expect(screen.getByText('No port tool on this host.')).toBeTruthy())
    expect(screen.queryByText('Nothing is listening')).toBeNull()
  })
})
