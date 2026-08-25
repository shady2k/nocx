// ═══════════════════════════════════════════════════════════════════════════
// PortsPanel — the Detected / orphan-forwards surface (spec §9, nocx-wzc4.2),
// now a SIDEBAR VIEW (nocx-wzc4.7): the owner's reference
// (Orca's PORTS panel) sits beside the terminal so a port can be watched
// while the command that opens it is being typed. The panel follows the
// ACTIVE tab — profileId is a reactive accessor, never a capture — and
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
// ═══════════════════════════════════════════════════════════════════════════

import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import type { Component } from 'solid-js'
import type { Dispatcher } from './dispatcher'
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
import { LOCAL_TARGET_ID } from './ports-client'

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
  status(profileId: string): Promise<PortsStatusResult>
  sample(profileId: string): Promise<PortsStatusResult>
  pause(profileId: string, paused: boolean): Promise<unknown>
  visible(profileId: string, visible: boolean): Promise<unknown>
  openForward(profileId: string, destination: string, port: number): Promise<TunnelOpenResult>
  stopForward(id: string): Promise<TunnelStopResult>
}

/** Real implementation over the dispatcher. The forward scope names the
 *  panel as owner, so closing the panel tab stops exactly its forwards. */
export function createPortsPanelServices(dispatcher: Dispatcher): PortsPanelServices {
  const call = <T,>(method: string, params: unknown): Promise<T> =>
    dispatcher.call<T>(method, params)
  return {
    status: (profileId) => call('ports.status', { profileId }),
    sample: (profileId) => call('ports.sample', { profileId }),
    pause: (profileId, paused) => call('ports.pause', { profileId, paused }),
    visible: (profileId, visible) => call('ports.visible', { profileId, visible }),
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
        placeholder="Filter ports"
        ariaLabel="Filter ports"
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

export interface PortsPanelProps {
  /** Reactive scope — the ACTIVE tab's ports target id, never a capture: a
   *  saved-profile id, or the reserved "local" for a local shell
   *  (nocx-wzc4.8). Null when the active tab has no ports scope (alias tab,
   *  Settings): the panel then shows the no-connection state instead of a
   *  stale host's ports. */
  profileId: () => string | null
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
  /** When profileId is null because the pane walked into an environment we
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

export function PortsPanel(props: PortsPanelProps) {
  const [status, setStatus] = createSignal<PortsStatusResult | null>(null)
  const [error, setError] = createSignal<string | null>(null)
  const [forwards, setForwards] = createSignal<Map<string, ForwardRecord>>(new Map())

  /** The query, read from the shared control rather than held here: the
   *  field that writes it is in the shell's pinned row, outside this
   *  component (nocx-708q.3). */
  const query = () => props.filter.query()

  /** The panel's view of the shared pause state. */
  const paused = () => props.pause.paused()

  /** The panel's current scope — an alias for the reactive prop, read at
   *  call sites so every fetch and mutation targets the ACTIVE tab. */
  const profileId = () => props.profileId()

  /** The host the pane walked into without a managed connection — the
   *  no-connection state's subject and the source of its action. '' when
   *  there is none (W1 feeds this from locationLine). */
  const unavailableTarget = () => props.unavailableIn?.() ?? ''

  /** True while the panel is scoped to the machine nocx itself runs on —
   *  the reserved "local" target (nocx-wzc4.8). Nothing can be forwarded
   *  from the machine you are on, so local rows offer copy-address instead
   *  of a Forward action. */
  const isLocal = () => profileId() === LOCAL_TARGET_ID

  /** Merge a fresh status: discovery state, cadence flags, and the backend's
   *  tracked forwards (which include connection-loss stops) on top of the
   *  panel's own records (which include user stops). */
  const applyStatus = (st: PortsStatusResult): void => {
    setStatus(st)
    setError(null)
    props.pause.sync(st.discovery.paused)
    setForwards((prev) => {
      const next = new Map(prev)
      for (const f of st.forwards) next.set(f.id, f)
      return next
    })
  }

  // Non-reactive by intent: reads signals, writes state, but must never
  // re-run when a signal it reads changes — it is a plain fetch. The pid is
  // captured per call and a response applies only while the panel is still
  // scoped to it: a late answer for a previous tab must never paint over the
  // current one (nocx-wzc4.7).
  const refresh = async (): Promise<void> => {
    const pid = profileId()
    if (pid === null) return
    try {
      const st = await props.services.status(pid)
      if (profileId() !== pid) return
      applyStatus(st)
    } catch (e) {
      if (profileId() !== pid) return
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  // Re-scope: the panel follows the ACTIVE tab. A profile switch discards
  // the previous connection's entire state — a local tab must not keep
  // showing a stale host's ports.
  createEffect(
    on(profileId, () => {
      setStatus(null)
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
   *  Toast. The reason that reaches `setError` is the wire's, and a message
   *  about an action does not live in the document flow (ui/README "Toast")
   *  — but dropping the wire's words is a soft degrade: "Could not read the
   *  ports" cannot tell a dropped connection from a permission refusal,
   *  which need different responses (nocx-8sudy). So the mapped sentence
   *  still carries what happened; only the phrasing is ours.
   *
   *  The backend's own words (ws_ports.go): -32603 "Port discovery not
   *  available (no discovery scheduler wired)" for an unwired scheduler,
   *  and -32602 "Invalid params: profileId required" for a malformed call
   *  (which this panel never makes). The rest is an open set from arbitrary
   *  transport and host failures — the dispatcher rejects with the RPC
   *  message for a control error, or a plain Error from `rejectAllPending`
   *  ("closed", "ws closed", "not connected") when the socket dropped. */
  const listingFailureMessage = (reason: string): string => {
    const r = reason.toLowerCase()
    if (
      /not connected|ws closed|closed|connection (lost|closed|reset|refused)/.test(r) ||
      r.includes('disconnected')
    ) {
      return 'Could not read the ports — the connection was lost.'
    }
    if (r.includes('discovery scheduler') || r.includes('not available')) {
      return 'Could not read the ports — port discovery is not available.'
    }
    if (r.includes('invalid params')) {
      return 'Could not read the ports — the request was not valid.'
    }
    // The open set: never pretend to a completeness we cannot have. The
    // sentence stays a person's; the reason is appended so the diagnostic
    // survives (AGENTS.md: a soft degrade must be visible in the product).
    return `Could not read the ports (${reason}).`
  }

  /** Raise the outcome of a failed status/sample call once, at the moment
   *  it happens — never once per poll tick (danger is sticky; a repeated
   *  failure is the same news twice). Edge-triggered on the `error`
   *  signal's VALUE: `on()` fires only when it changes, so a string that
   *  persists across failing polls raises once, and a recovery (null)
   *  followed by another failure raises again. */
  createEffect(
    on(error, (msg) => {
      if (msg !== null) {
        showToast({ level: 'danger', message: listingFailureMessage(msg) })
      }
    }),
  )

  // The backend's per-profile visible flag — the scheduler pauses discovery
  // sampling while nothing is watching. Re-scope retires the previous
  // profile's flag before arming the current one.
  createEffect(
    on([profileId, () => props.visible()], ([pid, vis], prev) => {
      const prevPid = prev?.[0] ?? null
      if (prevPid !== null && prevPid !== pid) {
        void props.services.visible(prevPid, false).catch(() => {})
      }
      if (pid !== null) {
        void props.services.visible(pid, vis).catch(() => {})
      }
    }),
  )

  // Initial load (a tracked scope, so solid/reactivity accepts the call)
  // plus a visibility-gated poll: hidden views stop fetching.
  let poll: ReturnType<typeof setInterval> | undefined
  createEffect(() => {
    const pid = profileId()
    if (pid === null) return
    void refresh()
    if (!props.visible()) return
    // The interval survives pause; the refresh is what skips. Resuming reuses
    // the same interval, and the optimistic toggle flips the flag the moment
    // the header action is pressed (nocx-wzc4.9).
    poll = setInterval(() => {
      if (!props.pause.paused()) void refresh()
    }, POLL_INTERVAL_MS)
    onCleanup(() => clearInterval(poll))
  })

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
   *  locally, default to an allocated loopback port. */
  const forward = async (destination: string, port: number): Promise<void> => {
    const pid = profileId()
    // Nothing to forward from the machine you are on: local rows offer
    // copy-address, never Forward (nocx-wzc4.8). The guard makes the
    // invariant structural — the row button is already swapped, but a
    // stray call must never dial tunnel.open with the "local" target.
    if (pid === null || pid === LOCAL_TARGET_ID) return
    try {
      const rec = await props.services.openForward(pid, destination, port)
      if (profileId() !== pid) return
      recordForward(rec)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      if (!/address already in use|EADDRINUSE/i.test(msg)) {
        if (profileId() === pid) showToast({ level: 'danger', message: msg })
        return
      }
      try {
        const rec = await props.services.openForward(pid, destination, 0)
        if (profileId() !== pid) return
        recordForward(rec)
      } catch (e2) {
        const msg2 = e2 instanceof Error ? e2.message : String(e2)
        if (profileId() === pid) showToast({ level: 'danger', message: msg2 })
      }
    }
    if (profileId() === pid) await refresh()
  }

  const stop = async (id: string): Promise<void> => {
    const pid = profileId()
    if (pid === null) return
    try {
      const rec = await props.services.stopForward(id)
      if (profileId() !== pid) return
      setForwards((prev) => new Map(prev).set(rec.id, rec))
      await refresh()
    } catch (e) {
      if (profileId() === pid) {
        showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
      }
    }
  }

  const retry = (rec: ForwardRecord): void => {
    void forward(rec.destination, rec.requestedBind.port)
  }

  /** The Retry action inside failure states: force a fresh sample. */
  const sampleNow = async (): Promise<void> => {
    const pid = profileId()
    if (pid === null) return
    try {
      const st = await props.services.sample(pid)
      if (profileId() !== pid) return
      applyStatus(st)
    } catch (e) {
      if (profileId() !== pid) return
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const copyAddress = (addr: string): void => {
    void navigator.clipboard
      .writeText(addr)
      .then(() => showToast({ level: 'success', message: `Copied ${addr}` }))
      .catch(() => showToast({ level: 'warning', message: 'Could not copy' }))
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
    st()?.state === 'available' || st()?.state === 'available-limited'
  /** Tell the shell's pinned row whether there is a list to narrow. Only
   *  this panel knows — the descriptor that owns the row cannot see the
   *  discovery state — and a search box above an explanation of why there
   *  are no ports is noise (nocx-cdub). A connection that dropped has no
   *  list either, which is why connLost counts here and not only below. */
  createEffect(() => props.filter.setAvailable(listAvailable() && !st()?.connLost))
  /** Reading, as opposed to having nothing to read. No status yet is always
   *  loading; a connected target whose first sample has not landed is too —
   *  that window is the settle delay plus a round trip, and showing nothing
   *  through it reads as broken (nocx-wzc4.11). A profile with no session is
   *  NOT loading: there is nothing to wait for. */
  /** True when any listener's owner could not be named. The reason is the
   *  probe's privilege, not the row's. */
  const hiddenOwners = (): boolean => listeners().some((l) => l.process.evidence !== 'known')

  const loading = (): boolean => {
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
    'permission-or-policy-refused',
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
    if (!s || s.connLost) return ''
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
      when={profileId() !== null}
      fallback={
        <Show
          when={unavailableTarget()}
          fallback={
            <EmptyState
              title="No active connection"
              description="Switch to an SSH tab — the ports it listens on will appear here."
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
              <Spinner label="Reading ports" />
              <span>Reading ports…</span>
            </div>
          }
        >
          {/* A profile with no session yet is not loading — there is nothing
              to wait for until the user opens one. */}
          <Show when={!host() && st()?.state === 'pending' && !isLocal()}>
            <EmptyState
              title="No active connection"
              description="Open an SSH session to this profile first — the ports it listens on will appear here."
            />
          </Show>
          <Show when={host() || (st()?.state ?? '') !== 'pending'}>
            <Show when={st()?.connLost}>
              <EmptyState
                title="Connection lost"
                description="Discovery stopped with the connection. It resumes automatically after you reconnect."
                action={<Button onClick={() => void sampleNow()}>Retry</Button>}
              />
            </Show>
            <Show when={!st()?.connLost}>
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
              <Section title="Detected" divided dense>
                {/* Whose listeners these are, said out loud and always. The
                    panel had no such statement, so a tab titled
                    `pi@raspberrypi` showed this machine's listeners and
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
                    title="Discovery is in a state this panel does not know"
                    description={`The backend reported "${unhandledState()}". This is a bug in nocx, not on the host.`}
                    action={<Button onClick={() => void sampleNow()}>Retry</Button>}
                  />
                </Show>
                <Show when={st()?.state === 'unavailable'}>
                  <EmptyState
                    title="Could not determine what is listening"
                    description={st()?.classification || 'No probe tool is usable on this host.'}
                  />
                </Show>
                <Show when={st()?.state === 'failed-transiently'}>
                  <EmptyState
                    title="Discovery failed transiently"
                    description={`${st()?.classification ?? 'The probe failed.'} Retrying automatically with backoff.`}
                  />
                </Show>
                <Show when={st()?.state === 'permission-or-policy-refused'}>
                  <EmptyState
                    title="Discovery refused on this host"
                    description={
                      st()?.classification ?? 'The server refused the additional session.'
                    }
                    action={<Button onClick={() => void sampleNow()}>Retry</Button>}
                  />
                </Show>
                <Show when={st()?.state === 'pending' && host()}>
                  <EmptyState
                    title="Waiting for the first sample"
                    description="The settle sample runs shortly after the connection comes up."
                  />
                </Show>
                <Show when={listAvailable()}>
                  {/* Stated once, above the rows it applies to. */}
                  <Show when={hiddenOwners()}>
                    <p class="ports-note" data-testid="ports-owners-note">
                      Some owners are hidden — run as root to see them.
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
                                          Forwarded
                                        </Badge>
                                      </>
                                    )
                                  }}
                                </Show>
                              </div>
                              <Show
                                when={isLocal()}
                                fallback={
                                  running ? (
                                    <div class="ports-row__actions">
                                      <IconButton
                                        data-testid="ports-copy"
                                        size="xs"
                                        ariaLabel={`Copy ${running.actualBind.host}:${running.actualBind.port}`}
                                        title={`Copy ${running.actualBind.host}:${running.actualBind.port}`}
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
                                        ariaLabel={`Open ${running.actualBind.host}:${running.actualBind.port}`}
                                        title={`Open ${running.actualBind.host}:${running.actualBind.port}`}
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
                                        ariaLabel={`Stop forward ${running.destination}`}
                                        title={`Stop forward ${running.destination}`}
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
                                      Retry
                                    </Button>
                                  ) : (
                                    <div class="ports-row__actions">
                                      <IconButton
                                        data-testid="ports-forward"
                                        size="xs"
                                        ariaLabel={`Forward ${destinationFor(l)}`}
                                        title={`Forward ${destinationFor(l)}`}
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
                                    ariaLabel={`Copy ${destinationFor(l)}`}
                                    title={`Copy ${destinationFor(l)}`}
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
                          title="No ports match that"
                          description="Clear the filter to see the full list."
                        />
                      }
                    >
                      <EmptyState
                        title="Nothing is listening"
                        description={`No listeners observed on ${host()}.`}
                      />
                    </Show>
                  </Show>
                </Show>
              </Section>

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
                <Section title="Orphaned forwards" divided dense>
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
                              <Button data-testid="ports-retry-forward" onClick={() => retry(f)}>
                                Retry
                              </Button>
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
                                ariaLabel={`Copy ${f.actualBind.host}:${f.actualBind.port}`}
                                title={`Copy ${f.actualBind.host}:${f.actualBind.port}`}
                                onClick={() =>
                                  copyAddress(`${f.actualBind.host}:${f.actualBind.port}`)
                                }
                              >
                                <CopyIcon />
                              </IconButton>
                              <IconButton
                                data-testid="ports-open"
                                size="xs"
                                ariaLabel={`Open ${f.actualBind.host}:${f.actualBind.port}`}
                                title={`Open ${f.actualBind.host}:${f.actualBind.port}`}
                                onClick={() =>
                                  openAddress(`${f.actualBind.host}:${f.actualBind.port}`)
                                }
                              >
                                <ExternalLinkIcon />
                              </IconButton>
                              <IconButton
                                data-testid="ports-stop"
                                size="xs"
                                ariaLabel={`Stop forward ${f.destination}`}
                                title={`Stop forward ${f.destination}`}
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
            </Show>
          </Show>

          {/* Only "paused" survives here (nocx-wzc4.10). The sample's age told
              the user nothing they could act on: the list refreshes itself, and
              when a sample fails we show the failure instead of stale rows —
              so there is never a moment where the rows on screen are older
              than they look. "Paused" is different: it is the one state where
              the rows genuinely stop tracking the host, and it names why. */}
          <Show when={paused()}>
            <p class="ports-meta" data-testid="ports-meta">
              paused
            </p>
          </Show>
        </Show>
      </Stack>
    </Show>
  )
}
