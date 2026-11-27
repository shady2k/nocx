// ═══════════════════════════════════════════════════════════════════════════
// PortsPanel — the Detected / orphan-forwards surface (spec §9, nocx-wzc4.2),
// now a SIDEBAR VIEW (nocx-wzc4.7): the owner's reference
// (Orca's PORTS panel) sits beside the terminal so a port can be watched
// while the command that opens it is being typed. The panel follows the
// ACTIVE tab — paneId is a reactive accessor, never a capture — and
// pauses when the view is not visible (collapsed sidebar counts as hidden).
// Discovery's own state — unavailable, limited, last sample, Retry — lives
// in this same surface, because a degrade that is only in a log is the
// failure AGENTS.md names. Pause is a HEADER action (the view's
// SidebarViewDescriptor.actions slot), shared with the panel through the
// pause controller — the body offers no second vocabulary for it, and Retry
// exists only inside the failure states that need it (nocx-wzc4.9).
//
// THE FILTER IS THE SAME ARRANGEMENT one slot down (nocx-708q.3): the field
// lives in the shell's pinned row (SidebarViewDescriptor.filter) and the
// panel narrows its rows by the control they share. It was in the body,
// above the sections it governs, and so it scrolled away with the very list
// it narrows — which every panel with a filter had done, in its own way, for
// the same reason nobody had written down where a filter goes.
//
// The ledger labels, it never claims causation (spec D6): a row says what the
// remote listens on and why we know it, never "opened by <command>".
//
// TWO ANSWERS, FROM TWO PLACES (nocx-oalo1.13.4, ALX-1852). The HOST is asked
// only while the connection's helper axis serves: `helperGate` is the same
// predicate Files and Git gate on, and while it is closed the panel draws the
// shared `HelperAxisCard` and sends no `ports.*` at all. The LEDGER
// (`tunnel.list`) is the backend's own record of forwards — it goes over SSH
// channels and asks the host nothing — so it is read on every tick whatever
// the axis says, and a forward stays visible and stoppable on a host whose
// helper is not running. That split is one decision, taken once: the gate
// decides what may leave, never what is drawn.
//
// AND NOTHING IS DECIDED FROM AN ERROR'S TEXT (C26): the sentence a failed
// read earns is chosen by `ports.error.reason`, or by the dispatcher's own
// typed loss, and the backend's words are only ever quoted into the open
// fallback.
// ═══════════════════════════════════════════════════════════════════════════

import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import type { Component } from 'solid-js'
import { rpcRefusalCause, TransportUnavailableError, type Dispatcher } from './dispatcher'
import { HelperAxisCard } from './connection/helper-axis-card'
import { helperGate, type HelperGate } from './connection/helper-axis'
import type { ActiveOrigin } from './pane-content'
import { openForwardWithFallback } from './forward-retry'
import type { PortsError } from './generated/ports.error'
import type { TunnelListResult } from './generated/tunnel.list'
import type { PortsStatusResult } from './generated/ports.status'
import type { TunnelOpenResult } from './generated/tunnel.open'
import type { TunnelStopResult } from './generated/tunnel.stop'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { EmptyState } from './ui/empty-state'
import { IconButton } from './ui/icon-button'
import { ArrowRightIcon, CopyIcon, ExternalLinkIcon, SquareIcon } from './ui/icons'
import { MarkerList } from './ui/marker-list'
import { SearchField } from './ui/search-field'
import { Section } from './ui/section'
import { Spinner } from './ui/spinner'
import { Stack } from './ui/stack'
import { showToast } from './ui/toast'
import { PortsClient } from './ports-client'
import { t } from './i18n/locale-model'

/** Split a `user@host` target into its parts, the way the product spells
 *  the target: W1's `portsUnavailableReason` reuses locationLine's
 *  `user@host` (bare `host` when there is no user), so this is the ONE
 *  parse of that string and the only place the empty state's action learns
 *  who the host is. No port is ever read — a hand-typed ssh gives us none,
 *  and "not set" must stay not set (adoptAliasProfile). */
function splitUnavailableTarget(target: string): {
  host: string
  user: string | undefined
} {
  const at = target.lastIndexOf('@')
  if (at === -1) return { host: target, user: undefined }
  return { host: target.slice(at + 1), user: target.slice(0, at) || undefined }
}

// ── Services seam ─────────────────────────────────────────────────────────

/** The panel's entire backend surface, so a test can substitute a fake. */
export interface PortsPanelServices {
  listForwards(): Promise<TunnelListResult>
  status(paneId: string): Promise<PortsStatusResult>
  sample(paneId: string): Promise<PortsStatusResult>
  pause(paneId: string, paused: boolean): Promise<unknown>
  visible(paneId: string, visible: boolean): Promise<unknown>
  openForward(profileId: string, destination: string, port: number): Promise<TunnelOpenResult>
  stopForward(id: string): Promise<TunnelStopResult>
}

/** Real implementation over the dispatcher. The forward scope names the
 *  panel as owner, so closing the panel tab stops exactly its forwards. */
export function createPortsPanelServices(dispatcher: Dispatcher): PortsPanelServices {
  const call = <T,>(method: string, params: unknown): Promise<T> =>
    dispatcher.call<T>(method, params)
  const ports = new PortsClient(dispatcher)
  return {
    listForwards: ports.listForwards.bind(ports),
    status: ports.status.bind(ports),
    sample: ports.sample.bind(ports),
    pause: ports.pause.bind(ports),
    visible: ports.visible.bind(ports),
    openForward: (profileId, destination, port) =>
      call('tunnel.open', { profileId, port, destination, scope: `ports:${profileId}` }),
    stopForward: (id) => call('tunnel.stop', { id }),
  }
}

// ── Pause controller ─────────────────────────────────────────────────────

/** The Pause state, shared between the view's HEADER action and the panel
 *  body: one signal, one backend call site. The header toggles it, the panel
 *  feeds it the backend's truth on every status merge and forgets it on
 *  re-scope. The controller closes over the reactive profile accessor, so
 *  the header never carries a stale profile id. */
export interface PortsPauseControl {
  paused: () => boolean
  /** Backend truth from a status merge. */
  sync(paused: boolean): void
  /** A profile switch forgets the previous connection's pause. */
  reset(): void
}

// nocx-wzc4.11 replaced the Pause header action with Refresh, so nothing in
// the renderer flips this any more: the control now only REFLECTS a pause the
// backend reports. `ports.pause` consequently has no caller — nocx-wzc4.12
// decides whether it gets one or goes.
export function createPortsPauseControl(): PortsPauseControl {
  const [paused, setPaused] = createSignal(false)
  return {
    paused,
    sync: (p) => setPaused(p),
    reset: () => setPaused(false),
  }
}

/** The panel's FILTER, shared with the shell's pinned slot exactly the way
 *  the pause control is shared with the header action.
 *
 *  It exists because the field cannot live in the body any more: a filter
 *  rendered among the rows scrolls away with the rows it narrows, which is
 *  the one control that must stay reachable while you scroll (owner,
 *  2026-08-22). `SidebarViewDescriptor.filter` is where a panel says which
 *  of its children is the filter, and the descriptor is built outside the
 *  panel — so the query has to be, too, or the field and the list would
 *  read two signals and could disagree about what is typed.
 *
 *  `available` travels the same seam in the opposite direction, and that is
 *  deliberate rather than convenient: only the panel can know whether there
 *  is a list to narrow, because it owns the discovery state, and a search
 *  box drawn above an explanation of why there are no ports is noise
 *  (nocx-cdub). `sync` on the pause control is the same shape for the same
 *  reason. */
export interface PortsFilterControl {
  /** What is typed. */
  query: () => string
  setQuery(q: string): void
  /** True while the panel holds a list a query could narrow — the field is
   *  absent when it is false. */
  available: () => boolean
  /** The panel reports its discovery state; nobody else may. */
  setAvailable(a: boolean): void
}

export function createPortsFilterControl(): PortsFilterControl {
  const [query, setQuery] = createSignal('')
  const [available, setAvailable] = createSignal(false)
  return {
    query,
    setQuery: (q) => setQuery(q),
    available,
    setAvailable: (a) => setAvailable(a),
  }
}

/** The Ports view's filter component, for `SidebarViewDescriptor.filter`.
 *  A factory rather than a component with a prop: the descriptor's slot
 *  takes a bare `Component`, so the control is closed over here — which is
 *  also what guarantees the field and the rows read one signal. */
export function createPortsFilter(control: PortsFilterControl): Component {
  return () => (
    <Show when={control.available()}>
      <SearchField
        value={control.query()}
        onInput={(v) => control.setQuery(v)}
        placeholder={t('ports.filter.ph', 'Filter ports')}
        ariaLabel={t('ports.filter.aria', 'Filter ports')}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && control.query() !== '') {
            e.stopPropagation()
            control.setQuery('')
          }
        }}
      />
    </Show>
  )
}

// ── Panel ─────────────────────────────────────────────────────────────────

export const POLL_INTERVAL_MS = 5_000

/** WHETHER THIS VIEW MAY ASK ITS HOST AT ALL — the one answer the panel body
 *  and the header action both read (design §5.10, C25/C26; ALX-1852).
 *
 *  It is `helperGate`'s predicate and nothing of its own: Files, Git, the
 *  terminal drop and the link opener gate on the same answer, and a second
 *  spelling here is how two surfaces come to disagree about one connection.
 *  A tab with no ports scope has nothing to gate (the panel says there is no
 *  connection), and a local tab is always allowed — `helperGate` answers both.
 *
 *  The gate decides what may LEAVE, never what is drawn: the ledger
 *  (`tunnel.list`) is the backend's own record of forwards and is read
 *  whether the axis serves or not (ALX-1852). */
export function portsHelperGate(paneId: string | null, origin: ActiveOrigin | null): HelperGate {
  if (paneId === null || origin === null) return null
  return helperGate({ kind: origin.kind, connection: origin.connection })
}

export interface PortsPanelProps {
  /** The backend pane id of the active terminal. */
  paneId: () => string | null
  /** Local panes offer copy-address. Only saved SSH profiles can open forwards. */
  isLocal?: () => boolean
  savedProfileId?: () => string | null
  /** The ACTIVE tab's origin — the connection whose helper axis this panel
   *  draws, and the same reactive accessor the Files panel and the Git store
   *  gate on. A signal, never a capture: switching tabs re-scopes the panel. */
  activeOrigin: () => ActiveOrigin | null
  /** The axis card's actions, owned once in the composition root and shared
   *  with Files and Git (connection/helper-axis-card.tsx). */
  onRefreshHelper?: (paneId: string) => void
  onHostSettings?: (profileId: string) => void
  services: PortsPanelServices
  /** Reactive visibility; false stops the status poll and tells the
   *  backend to pause sampling. A collapsed sidebar is not visible. */
  visible: () => boolean
  /** The shared Pause control — the header action toggles it, the panel
   *  reflects and syncs it (nocx-wzc4.9). */
  pause: PortsPauseControl
  /** The shared Filter control — the shell's pinned row holds the field,
   *  the panel narrows its rows by it and reports whether there is a list
   *  to narrow at all (nocx-708q.3). */
  filter: PortsFilterControl
  /** When paneId is null because the pane walked into an environment we
   *  cannot enumerate, this names it — a hand-typed `ssh` has no managed
   *  connection, so there is no second exec channel to ask on. '' otherwise
   *  (nocx-695k.3). */
  unavailableIn?: () => string
  /** The unavailable-host empty state's action: open that host as a nocx
   *  connection. The panel stays dumb — it hands over only the host/user it
   *  parsed out of `unavailableIn`; how a connection is made is the
   *  composition root's business (W2). Absent, the state offers no action
   *  rather than a dead one. */
  onOpenAsConnection?: (host: string, user: string | undefined) => void
}

type ForwardRecord = TunnelOpenResult | TunnelStopResult

/** One failed ports.* read: the wire's typed cause, when it carried one, and
 *  the text a person's diagnostic line quotes. The two travel together
 *  because the sentence is chosen by the REASON and only ever displays the
 *  text (C26 — nothing here is matched against). */
interface PortsFailure {
  reason: PortsError['reason'] | null
  /** True when the socket dropped under the call — the dispatcher's own
   *  class, never a reading of its words. */
  transportLost: boolean
  message: string
}

export function PortsPanel(props: PortsPanelProps) {
  const [helperUnavailable, setHelperUnavailable] = createSignal(false)
  const [status, setStatus] = createSignal<PortsStatusResult | null>(null)
  const [error, setError] = createSignal<PortsFailure | null>(null)
  const [forwards, setForwards] = createSignal<Map<string, ForwardRecord>>(new Map())

  /** The query, read from the shared control rather than held here: the
   *  field that writes it is in the shell's pinned row, outside this
   *  component (nocx-708q.3). */
  const query = () => props.filter.query()

  /** The panel's view of the shared pause state. */
  const paused = () => props.pause.paused()

  /** The panel's current scope — an alias for the reactive prop, read at
   *  call sites so every fetch and mutation targets the ACTIVE tab. */
  const paneId = () => props.paneId()

  /** The host the pane walked into without a managed connection — the
   *  no-connection state's subject and the source of its action. '' when
   *  there is none (W1 feeds this from locationLine). */
  const unavailableTarget = () => props.unavailableIn?.() ?? ''

  const isLocal = () => props.isLocal?.() === true
  const savedProfileId = () => props.savedProfileId?.() ?? null

  /** The gate, folded once: the panel asks its host nothing while it is
   *  closed (ALX-1852). */
  const gate = (): HelperGate => portsHelperGate(paneId(), props.activeOrigin())
  /** Closed, including the moment a ports.* answer told us the helper is gone
   *  before the connection's own observation caught up (the race ALX-1852
   *  closes). Both draw the SAME card and neither raises a toast. */
  const gateClosed = (): boolean => gate() !== null || helperUnavailable()

  const applyStatus = (st: PortsStatusResult): void => {
    setStatus(st)
    setHelperUnavailable(false)
    setError(null)
    props.pause.sync(st.discovery.paused)
  }

  const applyDiscoveryFailure = (failure: unknown): void => {
    const reason = rpcRefusalCause<PortsError['reason']>(failure)
    if (reason === 'helper-unavailable') {
      // A STATE, not a failure: the connection's helper is not serving this
      // host, which the axis card says in the backend's own words. No toast,
      // no spinner, and no second sentence of our invention (ALX-1852).
      setStatus(null)
      setHelperUnavailable(true)
      setError(null)
      return
    }
    const next: PortsFailure = {
      reason,
      transportLost: failure instanceof TransportUnavailableError,
      message: failure instanceof Error ? failure.message : String(failure),
    }
    // THE SAME NEWS IS NOT NEWS TWICE. A failing poll re-states one failure
    // every five seconds; a new object for it would re-fire the Toast that
    // explains it (nocx-8sudy). The state changes when the ACCOUNT changes,
    // which is also what keeps the toast edge-triggered on the signal's value
    // rather than on an object's identity.
    const prev = error()
    if (prev !== null && prev.reason === next.reason && prev.message === next.message) return
    setError(next)
  }

  const refreshForwards = async (pid: string): Promise<void> => {
    try {
      const { forwards: records } = await props.services.listForwards()
      if (paneId() !== pid || isLocal()) return
      setForwards((prev) => {
        const next = new Map(prev)
        for (const record of records) next.set(record.id, record)
        return next
      })
    } catch {
      // Keep the last ledger so its actions stay available during a read failure.
    }
  }

  // Non-reactive by intent: reads signals, writes state, but must never
  // re-run when a signal it reads changes — it is a plain fetch. The pid is
  // captured per call and a response applies only while the panel is still
  // scoped to it: a late answer for a previous tab must never paint over the
  // current one (nocx-wzc4.7).
  const refreshDiscovery = async (pid: string): Promise<void> => {
    try {
      const st = await props.services.status(pid)
      if (paneId() !== pid) return
      applyStatus(st)
      // The first visibility call may have reached the backend before
      // the pane or its far helper was ready.
      if (props.visible() && !st.discovery.visible) {
        void props.services.visible(pid, true).catch(() => {})
      }
    } catch (e) {
      if (paneId() !== pid) return
      applyDiscoveryFailure(e)
    }
  }

  const refresh = async (readDiscovery = true): Promise<void> => {
    const pid = paneId()
    if (pid === null) return
    await Promise.all([
      // The host is asked only while the axis serves (ALX-1852). The ledger
      // is the backend's own record and is read either way, so a forward
      // stays visible and stoppable on a host whose helper is not running.
      readDiscovery && !gateClosed() ? refreshDiscovery(pid) : undefined,
      !isLocal() && props.visible() ? refreshForwards(pid) : undefined,
    ])
  }

  // Re-scope: the panel follows the ACTIVE tab. A profile switch discards
  // the previous connection's entire state — a local tab must not keep
  // showing a stale host's ports.
  createEffect(
    on(paneId, () => {
      setStatus(null)
      setHelperUnavailable(false)
      setError(null)
      props.pause.reset()
      setForwards(new Map())
      // The filter is part of the re-scoped state: a query typed for the
      // previous host must not meet the next host's list half-filtered
      // (nocx-cdub decision 4). It is cleared through the shared control,
      // which is what makes the pinned field go blank with the rows.
      props.filter.setQuery('')
    }),
  )

  /** Turn one status/sample failure into the sentence a person reads in a
   *  Toast. THE REASON DECIDES THE SENTENCE (C26, nocx-oalo1.13.4); the
   *  message is only ever quoted, never inspected — a mapper that matches
   *  prose is the defect the connection-owner rule exists to refuse, and
   *  `listingFailureMessage` carried five such matches until this leaf.
   *
   *  Three arms, and the third is the honest one:
   *   - the wire's closed reason (`contracts/ports.error.schema.json`),
   *   - a socket that dropped, which the dispatcher types rather than
   *     describes (TransportUnavailableError),
   *   - everything else, in one open sentence with the backend's own words
   *     appended as a diagnostic (AGENTS.md: a soft degrade must be visible
   *     in the product, never swallowed).
   */
  const listingFailureMessage = (failure: PortsFailure): string => {
    switch (failure.reason) {
      case 'helper-unavailable':
        return t(
          'ports.failed.helperUnavailable',
          'Could not read the ports — the nocx helper is not serving this connection.',
        )
      case 'unknown-pane':
        return t(
          'ports.failed.unknownPane',
          'Could not read the ports — this tab is no longer a connection nocx can ask.',
        )
      case null:
        break
    }
    if (failure.transportLost) {
      return t('ports.failed.disconnected', 'Could not read the ports — the connection was lost.')
    }
    // The open set: never pretend to a completeness we cannot have. The
    // sentence stays a person's; the reason is appended so the diagnostic
    // survives (AGENTS.md: a soft degrade must be visible in the product).
    return t('ports.failed.reason', 'Could not read the ports ({reason}).', {
      reason: failure.message,
    })
  }

  /** Raise the outcome of a failed status/sample call once, at the moment
   *  it happens — never once per poll tick (danger is sticky; a repeated
   *  failure is the same news twice). Edge-triggered on the failure state's
   *  VALUE: `applyDiscoveryFailure` refuses to re-state an identical account,
   *  so a field of failing polls raises one Toast and a recovery followed by
   *  another failure raises again. */
  createEffect(
    on(error, (failure) => {
      if (failure !== null) {
        showToast({ level: 'danger', message: listingFailureMessage(failure) })
      }
    }),
  )

  // The backend's per-target visible flag — the scheduler pauses discovery
  // sampling while nothing is watching. Re-scope retires the previous
  // target's flag before arming the current one.
  //
  // A CLOSED GATE SENDS NOTHING, not even a `false`: while the axis is not
  // serving the scheduler samples nothing anyway, and "no ports.* leaves" is
  // the invariant this leaf is accepted on (ALX-1852). The one call it owes
  // is made by the effect below the moment the gate opens again.
  createEffect(
    on([paneId, () => props.visible(), gateClosed], ([pid, vis, closed], prev) => {
      if (closed) return
      const prevPid = prev?.[0] ?? null
      if (prevPid !== null && prevPid !== pid) {
        void props.services.visible(prevPid, false).catch(() => {})
      }
      if (pid !== null) {
        void props.services.visible(pid, vis).catch(() => {})
      }
    }),
  )

  // A read AS SOON AS ONE IS POSSIBLE, with nobody pressing anything: a
  // re-scope, a tab coming back into view and an axis that returns to
  // `serving` all end in a fresh read (ALX-1852). `refresh()` itself decides
  // what may be asked — while the gate is closed that is the ledger only.
  createEffect(
    on([paneId, gateClosed], ([pid]) => {
      if (pid !== null) void refresh()
    }),
  )

  // The tick: a hidden view stops fetching, and a paused panel stops the
  // discovery read while the ledger keeps being read — `tunnel.list` is the
  // backend's record, not the host's answer (ALX-1852).
  let poll: ReturnType<typeof setInterval> | undefined
  createEffect(
    on([paneId, () => props.visible()], ([pid, vis]) => {
      if (pid === null || !vis) return
      poll = setInterval(() => {
        void refresh(!props.pause.paused())
      }, POLL_INTERVAL_MS)
      onCleanup(() => clearInterval(poll))
    }),
  )

  const destinationFor = (l: PortsStatusResult['discovery']['listeners'][number]): string => {
    const host = status()?.host ?? ''
    const wildcard = l.address === '0.0.0.0' || l.address === '::' || l.address === ''
    return wildcard && host ? `${host}:${l.port}` : `${l.address}:${l.port}`
  }

  const recordForward = (rec: ForwardRecord): void => {
    setForwards((prev) => new Map(prev).set(rec.id, rec))
    showToast({
      level: 'success',
      message: `Forwarding ${rec.destination} on ${rec.actualBind.host}:${rec.actualBind.port}`,
    })
  }

  /** One action from the row (spec §9). When the same numeric port is busy
   *  locally — the wire's typed `address-in-use` cause, never the message —
   *  the forward is opened on an allocated loopback port instead. */
  const forward = async (destination: string, port: number): Promise<void> => {
    const pid = paneId()
    const profile = savedProfileId()
    if (pid === null || profile === null) return
    // Bind the service once, here: the retry callback below closes over this
    // and the request only, so it reads no reactive prop when it runs.
    const openOn = props.services.openForward.bind(props.services)
    try {
      const rec = await openForwardWithFallback(
        (bindPort) => openOn(profile, destination, bindPort),
        port,
      )
      if (paneId() !== pid) return
      recordForward(rec)
    } catch (e) {
      if (paneId() === pid) {
        showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
      }
      return
    }
    if (paneId() === pid) await refresh(false)
  }

  const stop = async (id: string): Promise<void> => {
    const pid = paneId()
    if (pid === null) return
    try {
      const rec = await props.services.stopForward(id)
      if (paneId() !== pid) return
      setForwards((prev) => new Map(prev).set(rec.id, rec))
      await refresh(false)
    } catch (e) {
      if (paneId() === pid) {
        showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
      }
    }
  }

  const retry = (rec: ForwardRecord): void => {
    void forward(rec.destination, rec.requestedBind.port)
  }

  /** The Retry action inside failure states: force a fresh sample. */
  const sampleNow = async (): Promise<void> => {
    const pid = paneId()
    if (pid === null) return
    try {
      const st = await props.services.sample(pid)
      if (paneId() !== pid) return
      applyStatus(st)
    } catch (e) {
      if (paneId() !== pid) return
      applyDiscoveryFailure(e)
    }
  }

  const copyAddress = (addr: string): void => {
    void navigator.clipboard
      .writeText(addr)
      .then(() =>
        showToast({ level: 'success', message: `${t('ports.copied', 'Copied')} ${addr}` }),
      )
      .catch(() =>
        showToast({ level: 'warning', message: t('ports.copyFailed', 'Could not copy') }),
      )
  }

  const openAddress = (addr: string): void => {
    window.open(`http://${addr}`, '_blank')
  }

  const st = () => status()?.discovery
  const host = () => status()?.host ?? ''
  const listeners = () => st()?.listeners ?? []
  /** The discovery states that can hold rows — the only states the filter
   *  and the rows appear in. A failure state has no list to filter, and
   *  showing a search box above an explanation reads as noise (nocx-cdub). */
  const listAvailable = (): boolean =>
    !gateClosed() && (st()?.state === 'available' || st()?.state === 'available-limited')
  /** Tell the shell's pinned row whether there is a list to narrow. Only
   *  this panel knows — the descriptor that owns the row cannot see the
   *  discovery state — and a search box above an explanation of why there
   *  are no ports is noise (nocx-cdub). A connection that dropped has no
   *  list either. */
  createEffect(() => props.filter.setAvailable(listAvailable()))
  /** Reading, as opposed to having nothing to read. No status yet is always
   *  loading; a connected target whose first sample has not landed is too —
   *  that window is the settle delay plus a round trip, and showing nothing
   *  through it reads as broken (nocx-wzc4.11). A profile with no session is
   *  NOT loading: there is nothing to wait for. */
  /** True when any listener's owner could not be named. The reason is the
   *  probe's privilege, not the row's. */
  const hiddenOwners = (): boolean => listeners().some((l) => l.process.evidence !== 'known')

  const loading = (): boolean => {
    // A closed gate has nothing to wait for: the card says what to read
    // instead of a spinner that would never end (ALX-1852).
    if (gateClosed()) return false
    if (st() === undefined) return true
    if (st()?.state !== 'pending') return false
    return isLocal() || !!host()
  }

  /** Every discovery state the Detected section has an arm for. The section
   *  renders exactly one thing, and this is what makes "exactly one" a fact
   *  rather than a hope: a heading with a hairline and nothing under it is
   *  the shape a user reads as broken, and it is what the owner saw on
   *  2026-08-04. A state we do not know must NAME ITSELF rather than render
   *  as absence — an unhandled case is information, and losing it is the
   *  soft degrade AGENTS.md forbids. */
  const DETECTED_ARMS = new Set([
    'unavailable',
    'failed-transiently',
    'pending',
    'available',
    'available-limited',
  ])

  /** Whose listeners the Detected section is about. Always rendered, because
   *  the alternative is a list of bare addresses whose machine the user has
   *  to infer — and the thing they will infer it from is the tab title,
   *  which is set by whatever program last wrote OSC 2 and is not a fact
   *  about where discovery ran. */
  const whoseListeners = (): string => {
    if (isLocal()) return 'This machine'
    return host() || 'Connected host'
  }

  /** The state string when no arm claims it — '' when one does. */
  const unhandledState = (): string => {
    const s = st()
    if (!s) return ''
    return DETECTED_ARMS.has(s.state) ? '' : s.state || '(empty)'
  }

  /** The one forward that owns a DESTINATION's state (W7 revision,
   *  nocx-4wbx): a running record beats a self-stopped one for the same
   *  destination. A forward that is running is the destination's current
   *  truth; an earlier failure for the SAME destination is no longer news —
   *  its Retry would offer to redo what has already been done — so the row
   *  a destination gets is the live one. Bought by a webkit CI failure: a
   *  first connection's stored forward stopped with "connection lost" while
   *  a reconnect's replay ran, and the panel showed both as two truths
   *  about one thing — the exact shape this epic removed from
   *  Detected/Forwarded, reappearing among orphans. Records for DIFFERENT
   *  destinations are untouched: two live forwards to two destinations are
   *  two rows. */
  const forwardForDestination = (dest: string): ForwardRecord | undefined => {
    const matches = [...forwards().values()].filter((f) => f.destination === dest)
    return matches.find((f) => f.state === 'running') ?? matches[0]
  }

  /** The one forward for a detected row, keyed by the ONE destination
   *  derivation: a forward carries the same string destinationFor() builds,
   *  so the row that owns a port is the row that shows its state — the old
   *  separate Forwarded list made the same port two rows with two owners
   *  (W2). */
  const forwardFor = (
    l: PortsStatusResult['discovery']['listeners'][number],
  ): ForwardRecord | undefined => forwardForDestination(destinationFor(l))

  /** A forward the connection lost on its own — the two reasons that earn a
   *  Retry. A user stop is not information and renders nothing (W2). */
  const isSelfStopped = (f: ForwardRecord): boolean =>
    f.state === 'stopped' && (f.stopReason === 'error' || f.stopReason === 'connection lost')

  /** Forwards with no detected row to own them — the host may have stopped
   *  listening, discovery may be degraded, or the forward may have been
   *  replayed rather than started from this list. Running ones must stay
   *  stoppable and self-stopped ones must stay visible; folding them into
   *  the Detected list would make them vanish, a worse bug than the one
   *  this replaces (W2). A self-stopped record whose destination a live
   *  forward owns is superseded before this filter sees it (W7): the live
   *  row carries the destination's state, and a second row beside it would
   *  only offer a Retry that redoes what is already done. */
  const orphanForwards = (): ForwardRecord[] =>
    [...forwards().values()].filter((f) => {
      if (f.state !== 'running' && !isSelfStopped(f)) return false
      if (f.state !== 'running' && forwardForDestination(f.destination)?.state === 'running') {
        return false
      }
      return !listeners().some((l) => destinationFor(l) === f.destination)
    })

  /** The Detected section's rows: each listener plus the forward that owns
   *  its state, if any. Derived as ONE list so the section re-renders when
   *  either discovery or the forwards map changes — a plain `For` over
   *  listeners() would not re-run its item bodies when a forward appears
   *  (mapArray untracks the mapping, so only the list signal itself is
   *  tracked), and the row would never change (W2). */
  const detectedRows = () => listeners().map((l) => ({ listener: l, fwd: forwardFor(l) }))

  type DetectedRow = {
    listener: PortsStatusResult['discovery']['listeners'][number]
    fwd: ForwardRecord | undefined
  }

  /** A row that carries a forward in a state the user must still be able to
   *  act on — running (Stop) or self-stopped (Retry) — is never hidden by
   *  the filter: the filter exists to find rows, never to strand an action
   *  (nocx-cdub decision 3). */
  const carriesActionableForward = (r: DetectedRow): boolean =>
    r.fwd !== undefined && (r.fwd.state === 'running' || isSelfStopped(r.fwd))

  /** What a row matches against: the rendered address (which carries the
   *  port), the port on its own, the process name when the probe could name
   *  it, and the forward's destination when one owns the row. The pid is
   *  deliberately not matched — it is restart-unstable and not something a
   *  user types, and matching it lets a query hit a row nobody meant
   *  (nocx-cdub decision 1). */
  const rowHaystack = (r: DetectedRow): string => {
    const l = r.listener
    const parts = [`${l.address}:${l.port}`, String(l.port)]
    if (l.process.evidence === 'known') parts.push(l.process.name)
    if (r.fwd !== undefined) parts.push(r.fwd.destination)
    return parts.join(' ')
  }

  /** The Detected section's rows after the filter. A row carrying a live or
   *  self-stopped forward keeps its Stop/Retry on screen whatever the query
   *  says; everything else must match the query. The orphaned forwards are
   *  deliberately outside the filter: every one of them is by construction
   *  running or self-stopped, so filtering them could only hide a stoppable
   *  or retryable forward (nocx-cdub decision 3). */
  const visibleRows = (): DetectedRow[] => {
    const q = query().trim().toLowerCase()
    if (q === '') return detectedRows()
    return detectedRows().filter(
      (r) => carriesActionableForward(r) || rowHaystack(r).toLowerCase().includes(q),
    )
  }

  const processLabel = (p: { evidence: string; name: string; pid: number }): string => {
    switch (p.evidence) {
      case 'known':
        return `${p.name} (pid ${p.pid})`
      case 'permission-denied':
        return 'owners hidden — run as root to see owners'
      default:
        return 'process not provided by this probe'
    }
  }

  return (
    <Show
      when={paneId() !== null}
      fallback={
        <Show
          when={unavailableTarget()}
          fallback={
            <EmptyState
              title={t('ports.noConnection.title', 'No active connection')}
              description={t(
                'ports.noConnection.desc',
                'Switch to an SSH tab — the ports it listens on will appear here.',
              )}
            />
          }
        >
          {/* The pane walked somewhere we cannot see. Say which host and why,
              and name what would change it — showing this machine's listeners
              instead is what made a tab sitting on a Pi look like it was
              listing the Pi's ports (owner, 2026-08-04). The action is the
              second half: the sentence tells the user to open the host as a
              connection, and the button does it (W2). */}
          <EmptyState
            title={`Cannot see the ports on ${unavailableTarget()}`}
            description="You reached this shell by hand, so nocx has no connection of its own to it and cannot ask what is listening. Open the host as a connection to see its ports."
            action={
              <Show when={props.onOpenAsConnection}>
                <Button
                  data-testid="ports-open-as-connection"
                  onClick={() => {
                    const { host, user } = splitUnavailableTarget(unavailableTarget())
                    props.onOpenAsConnection?.(host, user)
                  }}
                >
                  Open as connection
                </Button>
              </Show>
            }
          />
        </Show>
      }
    >
      <Stack gap="loose">
        {/* ── Discovery state ─────────────────────────────────────── */}
        <Show
          when={!loading()}
          fallback={
            <div class="ports-loading" data-testid="ports-loading">
              <Spinner label={t('ports.reading.label', 'Reading ports')} />
              <span>{t('ports.reading.text', 'Reading ports…')}</span>
            </div>
          }
        >
          {/* THE GATE, DRAWN ONCE (ALX-1852, design §5.10). While the
              connection's helper is not serving — `starting`, or `blocked`
              for any reason the observation names — the Detected section
              holds the SAME card Files and Git draw, and the panel asks its
              host nothing at all. The sentence, the tone and the one action
              are the fold's answer (connection/helper-axis.ts); this surface
              supplies only its identity and its headline. */}
          <Show when={gateClosed()}>
            <Section title={t('ports.detected.title', 'Detected')} divided dense>
              <HelperAxisCard
                surface="ports"
                title={t(
                  'ports.helperBlocked.title',
                  'Ports need the nocx helper on this connection',
                )}
                startingTitle={t('ports.helperStarting.title', 'Waiting for the nocx helper')}
                startingDescription={t(
                  'ports.helperStarting.desc',
                  'The ports this host listens on appear when the nocx helper starts serving this connection.',
                )}
                /* A typed `helper-unavailable` can arrive before the
                   connection's own observation has caught up (ALX-1852). The
                   backend has then said this surface cannot read the host
                   while no axis sentence exists yet, so the card draws its
                   WAITING state — it never mints a reason the wire did not
                   send (ADR-ba6xet1b). */
                helper={
                  helperUnavailable() ? null : (props.activeOrigin()?.connection?.helper ?? null)
                }
                paneId={props.activeOrigin()?.connection?.paneId ?? null}
                livePaneId={() => props.activeOrigin()?.connection?.paneId ?? null}
                profileId={savedProfileId()}
                onRefresh={props.onRefreshHelper}
                onHostSettings={props.onHostSettings}
              />
            </Section>
          </Show>
          {/* A profile with no session yet is not loading — there is nothing
              to wait for until the user opens one. */}
          <Show when={!gateClosed() && !host() && st()?.state === 'pending' && !isLocal()}>
            <EmptyState
              title={t('ports.noConnection.title', 'No active connection')}
              description="Open an SSH session to this profile first — the ports it listens on will appear here."
            />
          </Show>
          <Show when={!gateClosed() && (host() || (st()?.state ?? '') !== 'pending')}>
            {/* THE FILTER IS NOT HERE, and that is the change. It used to
                    sit above the sections it governs, which is the right
                    place inside a body and the wrong place full stop: the
                    body scrolls, so the query field went up and away with
                    the very list it narrows (owner, 2026-08-22). It is now
                    `createPortsFilter` in the shell's pinned row, reading
                    the same control this panel filters by — one signal, two
                    readers. What the query means is unchanged: it is about
                    the whole panel, and the orphaned forwards stay outside
                    it, because every one of them is running or self-stopped
                    by construction and filtering them could only hide a
                    forward the user must still be able to stop or retry
                    (nocx-cdub decision 3). */}
            <Section title={t('ports.detected.title', 'Detected')} divided dense>
              {/* Whose listeners these are, said out loud and always. The
                    panel had no such statement, so a tab titled
                    `pi@host-2` showed this machine's listeners and
                    nothing contradicted the title (owner, 2026-08-04). A list
                    of addresses is meaningless without the machine it belongs
                    to, and the tab title is NOT that machine — it is whatever
                    the last program set.

                    A chip, not a sentence: this panel is a list you scan, and
                    the subject of a list is a label. The prose version read
                    like an apology for the rows underneath it. */}
              <div class="ports-subject" data-testid="ports-target-note">
                <Badge tone="neutral" truncate>
                  {whoseListeners()}
                </Badge>
              </div>
              <Show when={unhandledState()}>
                <EmptyState
                  title={t(
                    'ports.unknownState.title',
                    'Discovery is in a state this panel does not know',
                  )}
                  description={`${t('ports.unknownState.descHead', 'The backend reported "')}${unhandledState()}${t('ports.unknownState.descTail', '". This is a bug in nocx, not on the host.')}`}
                  action={
                    <Button onClick={() => void sampleNow()}>{t('ports.retry', 'Retry')}</Button>
                  }
                />
              </Show>
              <Show when={st()?.state === 'unavailable'}>
                {/* A FIXED SENTENCE, not the backend's classification: the
                    person reads what the state means for them, and the wire's
                    diagnostic prose stays a diagnostic. */}
                <EmptyState
                  title={t('ports.undetermined.title', 'Could not determine what is listening')}
                  description={t('ports.noTool.desc', 'No port tool on this host.')}
                />
              </Show>
              <Show when={st()?.state === 'failed-transiently'}>
                <EmptyState
                  title={t('ports.transient.title', 'Discovery failed transiently')}
                  description={`${st()?.classification ?? 'The probe failed.'} ${t('ports.transient.desc', 'Retrying automatically with backoff.')}`}
                />
              </Show>
              <Show when={st()?.state === 'pending' && host()}>
                <EmptyState
                  title={t('ports.settle.title', 'Waiting for the first sample')}
                  description={t(
                    'ports.settle.desc',
                    'The settle sample runs shortly after the connection comes up.',
                  )}
                />
              </Show>
              <Show when={listAvailable()}>
                {/* Stated once, above the rows it applies to. */}
                <Show when={hiddenOwners()}>
                  <p class="ports-note" data-testid="ports-owners-note">
                    {t('ports.hiddenOwners', 'Some owners are hidden — run as root to see them.')}
                  </p>
                </Show>
                <Show when={visibleRows().length > 0}>
                  <For each={visibleRows()}>
                    {(row) => {
                      const fwd = row.fwd
                      const running = fwd?.state === 'running' ? fwd : undefined
                      const failed = fwd !== undefined && isSelfStopped(fwd) ? fwd : undefined
                      const caveat = running?.caveat ?? ''
                      const l = row.listener
                      return (
                        <div
                          class="ports-row"
                          data-testid="detected-row"
                          data-state={running ? 'forwarded' : failed ? 'failed' : undefined}
                          title={failed?.error ?? undefined}
                        >
                          <div class="ports-row__main">
                            <div class="ports-row__text">
                              {/* The row is the port's single owner (W2):
                                    when its forward is running the address
                                    becomes the local bind the port is now
                                    reachable on; when the forward self-stopped
                                    the reason takes the quiet line. */}
                              <Show
                                when={running}
                                fallback={
                                  <Show
                                    when={failed}
                                    fallback={
                                      <>
                                        <span class="ports-row__addr">
                                          {l.address}:{l.port}
                                        </span>
                                        {/* Only a known owner earns a line.
                                              "Owners hidden" is one fact about the
                                              probe, not a banner repeated down every
                                              row where it does not fit
                                              (nocx-wzc4.11). */}
                                        <Show when={l.process.evidence === 'known'}>
                                          <span class="ports-row__proc">
                                            {processLabel(l.process)}
                                          </span>
                                        </Show>
                                      </>
                                    }
                                  >
                                    {(f) => {
                                      const rec = f()
                                      return (
                                        <>
                                          <span class="ports-row__addr">
                                            {l.address}:{l.port}
                                          </span>
                                          <Badge tone="danger" truncate>
                                            {rec.stopReason ?? 'stopped'}
                                          </Badge>
                                        </>
                                      )
                                    }}
                                  </Show>
                                }
                              >
                                {(r) => {
                                  const rec = r()
                                  return (
                                    <>
                                      <span class="ports-row__addr">
                                        {rec.actualBind.host}:{rec.actualBind.port}
                                      </span>
                                      <span class="ports-row__dest">
                                        <span class="ports-row__arrow" aria-hidden="true">
                                          →{' '}
                                        </span>
                                        {rec.destination}
                                      </span>
                                      {/* The tone is the legibility that does
                                            not need reading: a column of forwarded
                                            rows reads as one thing before the
                                            words do (W2). */}
                                      <Badge tone="info" truncate>
                                        {t('ports.forwarded', 'Forwarded')}
                                      </Badge>
                                    </>
                                  )
                                }}
                              </Show>
                            </div>
                            <Show
                              when={isLocal() || savedProfileId() === null}
                              fallback={
                                running ? (
                                  <div class="ports-row__actions">
                                    <IconButton
                                      data-testid="ports-copy"
                                      size="xs"
                                      ariaLabel={
                                        t('ports.copy.head', 'Copy') +
                                        ' ' +
                                        running.actualBind.host +
                                        ':' +
                                        running.actualBind.port
                                      }
                                      title={
                                        t('ports.copy.head', 'Copy') +
                                        ' ' +
                                        running.actualBind.host +
                                        ':' +
                                        running.actualBind.port
                                      }
                                      onClick={() =>
                                        copyAddress(
                                          `${running.actualBind.host}:${running.actualBind.port}`,
                                        )
                                      }
                                    >
                                      <CopyIcon />
                                    </IconButton>
                                    <IconButton
                                      data-testid="ports-open"
                                      size="xs"
                                      ariaLabel={
                                        t('ports.open.head', 'Open') +
                                        ' ' +
                                        running.actualBind.host +
                                        ':' +
                                        running.actualBind.port
                                      }
                                      title={
                                        t('ports.open.head', 'Open') +
                                        ' ' +
                                        running.actualBind.host +
                                        ':' +
                                        running.actualBind.port
                                      }
                                      onClick={() =>
                                        openAddress(
                                          `${running.actualBind.host}:${running.actualBind.port}`,
                                        )
                                      }
                                    >
                                      <ExternalLinkIcon />
                                    </IconButton>
                                    <IconButton
                                      data-testid="ports-stop"
                                      size="xs"
                                      ariaLabel={
                                        t('ports.stopForward', 'Stop forward') +
                                        ' ' +
                                        running.destination
                                      }
                                      title={
                                        t('ports.stopForward', 'Stop forward') +
                                        ' ' +
                                        running.destination
                                      }
                                      onClick={() => void stop(running.id)}
                                    >
                                      <SquareIcon />
                                    </IconButton>
                                  </div>
                                ) : failed ? (
                                  <Button
                                    data-testid="ports-retry-forward"
                                    onClick={() => retry(failed)}
                                  >
                                    {t('ports.retry', 'Retry')}
                                  </Button>
                                ) : (
                                  <div class="ports-row__actions">
                                    <IconButton
                                      data-testid="ports-forward"
                                      size="xs"
                                      ariaLabel={
                                        t('ports.forward', 'Forward') + ' ' + destinationFor(l)
                                      }
                                      title={
                                        t('ports.forward', 'Forward') + ' ' + destinationFor(l)
                                      }
                                      onClick={() => void forward(destinationFor(l), l.port)}
                                    >
                                      <ArrowRightIcon />
                                    </IconButton>
                                  </div>
                                )
                              }
                            >
                              <div class="ports-row__actions">
                                <IconButton
                                  data-testid="ports-copy"
                                  size="xs"
                                  ariaLabel={t('ports.copy.head', 'Copy') + ' ' + destinationFor(l)}
                                  title={t('ports.copy.head', 'Copy') + ' ' + destinationFor(l)}
                                  onClick={() => copyAddress(destinationFor(l))}
                                >
                                  <CopyIcon />
                                </IconButton>
                              </div>
                            </Show>
                          </div>
                          <Show when={caveat}>
                            <MarkerList items={[{ text: caveat, tone: 'note' }]} />
                          </Show>
                        </div>
                      )
                    }}
                  </For>
                </Show>
                {/* A query matching nothing says so: a heading with a
                      hairline and nothing under it is the shape a user
                      reads as broken, and "Nothing is listening" would be
                      a lie the user can disprove by clearing the box. */}
                <Show when={visibleRows().length === 0}>
                  <Show
                    when={query().trim() === ''}
                    fallback={
                      <EmptyState
                        title={t('ports.nomatch.title', 'No ports match that')}
                        description={t(
                          'ports.nomatch.desc',
                          'Clear the filter to see the full list.',
                        )}
                      />
                    }
                  >
                    <EmptyState
                      title={t('ports.nothing.title', 'Nothing is listening')}
                      description={`${t('ports.nothing.descHead', 'No listeners observed on')} ${host()}${t('ports.nothing.descTail', '.')}`}
                    />
                  </Show>
                </Show>
              </Show>
            </Section>
          </Show>

          {/* Only "paused" survives here (nocx-wzc4.10). The sample's age told
              the user nothing they could act on: the list refreshes itself, and
              when a sample fails we show the failure instead of stale rows —
              so there is never a moment where the rows on screen are older
              than they look. "Paused" is different: it is the one state where
              the rows genuinely stop tracking the host, and it names why. */}
          <Show when={paused() && !gateClosed()}>
            <p class="ports-meta" data-testid="ports-meta">
              {t('ports.paused', 'paused')}
            </p>
          </Show>
        </Show>
        {/* The forwarding vocabulary exists only off this machine:
              local rows offer copy-address, and an orphan section would
              be an empty offer of an impossible action (nothing to
              forward from the machine you are on, nocx-wzc4.8). The
              section itself renders only when it has a row to hold —
              an empty section has no subject, and its old "No active
              forwards" fallback contradicted a forward shown live on
              its own row (W2 revision). */}
        <Show when={!isLocal() && orphanForwards().length > 0}>
          {/* ── Orphaned forwards ─────────────────────────────── */}
          {/* Forwards with no detected row to own them. A running one
                is a forward the host no longer reports — still alive,
                still stoppable; a self-stopped one is a failure the user
                must be able to retry. The Stopped section is deleted: a
                stop the user performed is not information, and a detected
                port's state lives on its own row (W2). */}
          <Section title={t('ports.orphaned.title', 'Orphaned forwards')} divided dense>
            <For each={orphanForwards()}>
              {(f) => (
                <Show
                  when={f.state === 'running'}
                  fallback={
                    <div
                      class="ports-row"
                      data-testid="forwarded-row"
                      data-state="failed"
                      title={f.error ?? undefined}
                    >
                      <div class="ports-row__main">
                        <div class="ports-row__text">
                          <span class="ports-row__addr">{f.destination}</span>
                          <Badge tone="danger" truncate>
                            {f.stopReason ?? 'stopped'}
                          </Badge>
                        </div>
                        {/* Retry re-opens a forward through `tunnel.open`,
                            which is addressed by a SAVED profile: a
                            hand-typed tab has no host record to forward
                            from, so it offers no button rather than a dead
                            one (D14, nocx-oalo1.13.4). Stop stays — it is
                            addressed by the forward's own id. */}
                        <Show when={savedProfileId() !== null}>
                          <Button data-testid="ports-retry-forward" onClick={() => retry(f)}>
                            {t('ports.retry', 'Retry')}
                          </Button>
                        </Show>
                      </div>
                    </div>
                  }
                >
                  <div class="ports-row" data-testid="forwarded-row" data-state="forwarded">
                    <div class="ports-row__main">
                      <div class="ports-row__text">
                        <span class="ports-row__addr">
                          {f.actualBind.host}:{f.actualBind.port}
                        </span>
                        <span class="ports-row__dest">
                          <span class="ports-row__arrow" aria-hidden="true">
                            →{' '}
                          </span>
                          {f.destination}
                        </span>
                      </div>
                      <div class="ports-row__actions">
                        <IconButton
                          data-testid="ports-copy"
                          size="xs"
                          ariaLabel={
                            t('ports.copy.head', 'Copy') +
                            ' ' +
                            f.actualBind.host +
                            ':' +
                            f.actualBind.port
                          }
                          title={
                            t('ports.copy.head', 'Copy') +
                            ' ' +
                            f.actualBind.host +
                            ':' +
                            f.actualBind.port
                          }
                          onClick={() => copyAddress(`${f.actualBind.host}:${f.actualBind.port}`)}
                        >
                          <CopyIcon />
                        </IconButton>
                        <IconButton
                          data-testid="ports-open"
                          size="xs"
                          ariaLabel={
                            t('ports.open.head', 'Open') +
                            ' ' +
                            f.actualBind.host +
                            ':' +
                            f.actualBind.port
                          }
                          title={
                            t('ports.open.head', 'Open') +
                            ' ' +
                            f.actualBind.host +
                            ':' +
                            f.actualBind.port
                          }
                          onClick={() => openAddress(`${f.actualBind.host}:${f.actualBind.port}`)}
                        >
                          <ExternalLinkIcon />
                        </IconButton>
                        <IconButton
                          data-testid="ports-stop"
                          size="xs"
                          ariaLabel={t('ports.stopForward', 'Stop forward') + ' ' + f.destination}
                          title={t('ports.stopForward', 'Stop forward') + ' ' + f.destination}
                          onClick={() => void stop(f.id)}
                        >
                          <SquareIcon />
                        </IconButton>
                      </div>
                    </div>
                    {/* A -R forward whose bind sshd silently replaced
                           carries Caveat() — render it as the kit's note
                           (a caveat about the item above it), never as an
                           error: the forward is running. Empty caveat
                           renders nothing. */}
                    <Show when={f.caveat}>
                      <MarkerList items={[{ text: f.caveat, tone: 'note' }]} />
                    </Show>
                  </div>
                </Show>
              )}
            </For>
          </Section>
        </Show>
      </Stack>
    </Show>
  )
}
