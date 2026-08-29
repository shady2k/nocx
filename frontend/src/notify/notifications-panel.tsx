/**
 * The notification centre's panel — what happened while you were not looking.
 *
 * It is a plain role="list" of kit RecordRows, the shape notes-panel.tsx
 * already uses, and deliberately NOT a CollectionView: that is the searchable
 * manager surface (it requires searchValue, onSearch, hasItems and an empty
 * slot), and narrowing here is by a named axis, never by free text.
 *
 * Resolution of an occurrence to a tab is the RENDERER's and arrives as a
 * prop: the backend cannot do it at all — Attribution.Tab is a WebSocket
 * connection id — and the surface asking is what keeps this panel off the
 * banner-click path. The panel never marks anything read by being looked at:
 * "output arrived" and "you saw what we told you" are different facts, and
 * conflating them is the defect this centre exists to undo.
 *
 * ── Grouping and narrowing (nocx-ctl6q, decisions D2–D4) ────────────────
 *
 * A row whose count is above one DISCLOSES the constituents the feed still
 * holds (`run`), each with its own instant and its own unread mark; a row of
 * one is a leaf that still reserves the disclosure's width, so every title in
 * the list forms one column. Where the tail overflowed, the expansion says how
 * much of the run it is showing — a truncation presented as the whole is the
 * one thing D2 forbids.
 *
 * The filter and the set of expanded rows live HERE, in this surface, and not
 * in feed-store.ts. The store is created at the composition root because TWO
 * consumers read it — this panel and the activity bar's badge (main.tsx) — so
 * a filter inside it would narrow what the bell counts, which is exactly what
 * D3 says must not happen: `feed.unreadCount` is the single source of truth
 * for the bell and the dock badge, and a bell that quietened itself because
 * you narrowed a list would be lying about what is waiting. The store owns the
 * snapshot; a view over it is not a second owner of it (AD-8). D4 says the
 * same for expansion: ephemeral view state, in renderer signals, persisted
 * nowhere.
 *
 * So the panel states the narrowed count itself — "3 of 12 shown" — which is
 * the epic's criterion answered with its second half deliberately.
 */
import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js'
import { EmptyState } from '../ui/empty-state'
import { Field } from '../ui/field'
import { RecordRow } from '../ui/record-row'
import { Select } from '../ui/select'
import type { BadgeTone } from '../ui/badge'
import type { FeedStore } from './feed-store'
import type { Kind, NotifyCatalogue } from '../generated/notify.catalogue'
import type { NotifyFeedRead } from '../generated/notify.feed.read'

type Occurrence = NotifyFeedRead['occurrences'][number]
type RunMember = Occurrence['run'][number]

/** Level maps onto the kit's tone vocabulary. info is neutral, not "info":
 *  an ordinary completion is not an advisory, and toning every row blue is
 *  how a feed stops distinguishing anything. */
function toneOf(level: Occurrence['level']): BadgeTone {
  switch (level) {
    case 'danger':
      return 'danger'
    case 'warning':
      return 'warning'
    case 'success':
      return 'success'
    default:
      return 'neutral'
  }
}

/** A row's instant in the person's locale — this surface's phrasing, which
 *  is why it is here and not in ui/. An unparseable stamp renders verbatim
 *  rather than as "Invalid Date": the wire's word is better than a lie. */
function formatWhen(at: string): string {
  const ms = Date.parse(at)
  if (Number.isNaN(ms)) return at
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

/** Human-readable fallback used until the backend catalogue is available. */
function fallbackKindLabel(kind: string): string {
  const words = kind
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/\./g, ' ')
    .trim()
    .toLowerCase()
  if (words === '') return 'Notification'
  return `${words[0].toUpperCase()}${words.slice(1)}`
}

/** Two parts joined only where there are two parts to join. A LOCAL session
 *  has no host (the backend sets one only on the remote branch), so a fixed
 *  two-part template opened the row's meta line with " · " for the centre's
 *  very first source (nocx-lmmi5). One owner for that rule, used by the meta
 *  line and by the session filter's labels alike. */
function dotted(left: string, right: string): string {
  return left === '' ? right : `${left} · ${right}`
}

/** The row's meta line: where it came from, then when. The wording of the
 *  row's TITLE is the backend's and is not restated here. */
function metaLine(host: string, when: string): string {
  return dotted(host, when)
}

/** No filter. The Select's placeholder carries it, and it is the empty string
 *  because that is what a native <select> reports for an option with no value
 *  of its own. */
const ALL = ''

/** An axis value behind a one-character mark, so the empty string keeps its
 *  one meaning. A local session's host is GENUINELY the empty string, and an
 *  option whose value was '' would be indistinguishable from the placeholder —
 *  narrowing to "This machine" would read as narrowing to nothing. */
function pick(raw: string): string {
  return `v:${raw}`
}

/** One way to narrow the feed. `identity` is what two occurrences must share
 *  to be the same host/session/kind; `label` is how that reads to a person.
 *  Adding an axis is adding a row to this table — the panel's rendering,
 *  its options, its filtering and its "is anything narrowed" all read it. */
interface Axis {
  id: 'host' | 'session' | 'kind'
  label: string
  placeholder: string
  identity: (o: Occurrence) => string
  optionLabel: (o: Occurrence) => string
}

const UNAVAILABLE_SESSION = '\u0000unavailable'

function catalogueKind(
  kind: string,
  catalogue: NotifyCatalogue | null | undefined,
): Kind | undefined {
  return catalogue?.kinds.find((entry) => entry.kind === kind)
}

function kindLabel(kind: string, catalogue: NotifyCatalogue | null | undefined): string {
  return catalogueKind(kind, catalogue)?.label ?? fallbackKindLabel(kind)
}

function kindDescription(
  kind: string,
  catalogue: NotifyCatalogue | null | undefined,
): string | undefined {
  return catalogueKind(kind, catalogue)?.description
}

type Picked = Record<Axis['id'], string>
const NOTHING_PICKED: Picked = { host: ALL, session: ALL, kind: ALL }

export interface NotificationsPanelProps {
  store: FeedStore
  /** Focus the tab this occurrence came from. */
  onActivate: (backendId: string, sessionId: string) => void
  /** Whether that tab still exists — a row whose session is gone is not
   *  activatable, and says so rather than doing nothing when clicked. */
  canActivate: (backendId: string, sessionId: string) => boolean
  /** The backend-owned kind vocabulary, or null before its first success. */
  catalogue?: () => NotifyCatalogue | null
  /** Renderer-owned session-to-tab display name lookup. */
  sessionNameOf?: (backendId: string, sessionId: string) => string | null
  /** Say when the answer `canActivate` gives may have changed — a tab opened,
   *  or one closed (PaneManager.onPanesChanged). Returns the unsubscribe.
   *
   *  Required because `canActivate` reads state this panel cannot see, so
   *  nothing about it is reactive on its own, and this panel OUTLIVES the
   *  tabs it is about: the sidebar toggles a class rather than unmounting the
   *  view, so a `session.ended` row is built at the moment its own tab is
   *  closing. Without this the row kept the answer it got then and went on
   *  offering a tab that had closed (nocx-bu8fl).
   *
   *  Optional so a test that is not about the tab strip need not wire one;
   *  omitting it is the old behaviour, an answer read once per render. */
  subscribe?: (listener: () => void) => () => void
}

export function NotificationsPanel(props: NotificationsPanelProps) {
  const dropped = () => props.store.dropped()
  const all = () => props.store.visibleOccurrences()
  const catalogue = () => props.catalogue?.() ?? null
  const sessionName = (o: Occurrence) => props.sessionNameOf?.(o.backendId, o.sessionId) ?? null
  const axes: readonly Axis[] = [
    {
      id: 'host',
      label: 'Host',
      placeholder: 'All hosts',
      identity: (o) => o.host,
      optionLabel: (o) => (o.host === '' ? 'This machine' : o.host),
    },
    {
      id: 'session',
      label: 'Session',
      placeholder: 'All sessions',
      identity: (o) =>
        sessionName(o) === null ? UNAVAILABLE_SESSION : `${o.backendId}/${o.sessionId}`,
      optionLabel: (o) => sessionName(o) ?? 'Unavailable sessions',
    },
    {
      id: 'kind',
      label: 'Kind',
      placeholder: 'All kinds',
      identity: (o) => o.kind,
      optionLabel: (o) => kindLabel(o.kind, catalogue()),
    },
  ]

  /** Bumped whenever the tab strip changed. Rows read it before asking
   *  `canActivate`, which is what makes that question reactive at all — see
   *  the prop. A counter rather than the answer itself because the question
   *  is asked per row and the answer differs per row. */
  const [tabsGeneration, setTabsGeneration] = createSignal(0)
  onMount(() => {
    const unsubscribe = props.subscribe?.(() => setTabsGeneration((n) => n + 1))
    onCleanup(() => unsubscribe?.())
  })

  /** Which rows are open, by occurrence id. Ephemeral (D4): a feed that does
   *  not survive a restart cannot have an expansion that does. Keyed by id
   *  rather than by position, which is what keeps a row open across a refetch
   *  — and across a filter being applied and cleared. */
  const [expandedIds, setExpandedIds] = createSignal<ReadonlySet<string>>(new Set())
  const isExpanded = (id: string) => expandedIds().has(id)
  const toggle = (id: string) =>
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (!next.delete(id)) next.add(id)
      return next
    })

  const [picked, setPicked] = createSignal<Picked>(NOTHING_PICKED)

  /** The options an axis offers, derived from EVERY occurrence and never from
   *  the narrowed list: options computed from the filtered rows would remove
   *  every choice but the one you just made, and leave no way back. First-seen
   *  order, which is newest-first, because that is the order the feed gave. */
  const optionsFor = (axis: Axis) => {
    const seen = new Map<string, string>()
    for (const o of all()) {
      const id = axis.identity(o)
      if (!seen.has(id)) seen.set(id, axis.optionLabel(o))
    }
    return [...seen].map(([id, label]) => ({ value: pick(id), label }))
  }

  /** What an axis is actually narrowing to. A pick whose value the feed no
   *  longer holds — its rows evicted, its host gone — narrows to nothing and
   *  would leave an empty list with no visible cause, so it reads as no
   *  filter at all. */
  const activeOn = (axis: Axis): string => {
    const value = picked()[axis.id]
    if (value === ALL) return ALL
    return optionsFor(axis).some((o) => o.value === value) ? value : ALL
  }
  createEffect(() => {
    const current = picked()
    const next = { ...current }
    let changed = false
    for (const axis of axes) {
      if (current[axis.id] !== ALL && activeOn(axis) === ALL) {
        next[axis.id] = ALL
        changed = true
      }
    }
    if (changed) setPicked(next)
  })

  const narrowed = () => axes.some((axis) => activeOn(axis) !== ALL)

  const visible = () =>
    all().filter((o) =>
      axes.every((axis) => {
        const value = activeOn(axis)
        return value === ALL || value === pick(axis.identity(o))
      }),
    )

  return (
    <div class="notifications-panel">
      {/* An axis with one value can narrow nothing: an offer that cannot be
          honoured is a lie (design §8), and three dead controls at the top of
          a 240px panel is most of the list. */}
      <Show when={axes.some((axis) => optionsFor(axis).length > 1)}>
        <div class="notifications-panel__filters">
          <For each={axes}>
            {(axis) => (
              <Show when={optionsFor(axis).length > 1}>
                <Field for={`notifications-filter-${axis.id}`} label={axis.label}>
                  <Select
                    value={activeOn(axis)}
                    onChange={(value) => setPicked((prev) => ({ ...prev, [axis.id]: value }))}
                    options={optionsFor(axis)}
                    placeholder={axis.placeholder}
                    placeholderValue={ALL}
                  />
                </Field>
              </Show>
            )}
          </For>
        </div>
      </Show>

      {/* Stated only while something is narrowed. The BELL goes on counting
          everything (D3), so this is the only place the narrowed number is
          told, and "12 of 12 shown" over an unnarrowed feed would be noise
          the rest of the time. */}
      <Show when={narrowed()}>
        <div class="notifications-panel__shown" data-testid="notifications-shown-count">
          {visible().length} of {all().length} shown
        </div>
      </Show>

      <Show
        when={props.store.readKnown()}
        fallback={
          <EmptyState
            title="Could not read notifications"
            description="Reconnect or try again to check for notifications."
          />
        }
      >
        <Show
          when={all().length > 0}
          fallback={
            <EmptyState
              title="Nothing to catch up on"
              description="Notifications raised while you are elsewhere collect here."
            />
          }
        >
          <Show
            when={visible().length > 0}
            fallback={
              <EmptyState
                title="Nothing matches"
                description="No notification is from every one of the things you narrowed to."
              />
            }
          >
            <div class="notifications-panel__list" role="list" aria-label="Notifications">
              <For each={visible()}>
                {(o) => {
                  const live = () => {
                    // Read FIRST, so this stays a dependency however
                    // canActivate answers.
                    tabsGeneration()
                    return props.canActivate(o.backendId, o.sessionId)
                  }
                  return (
                    <RecordRow
                      density="dense"
                      title={o.count > 1 ? `${o.title} ×${o.count}` : o.title}
                      kind={{
                        label: kindLabel(o.kind, catalogue()),
                        description: kindDescription(o.kind, catalogue()),
                        tone: toneOf(o.level),
                      }}
                      meta={metaLine(o.host, live() ? formatWhen(o.at) : 'session closed')}
                      detail={o.body === '' ? undefined : o.body}
                      selected={!o.read}
                      onActivate={
                        live() ? () => props.onActivate(o.backendId, o.sessionId) : undefined
                      }
                      // A row of one is a LEAF, not a row that never heard of the
                      // disclosure: it holds the chevron's width so every title in
                      // this list stands in one column (record-row.tsx's three
                      // states, the middle one).
                      expandable={o.count > 1}
                      expanded={isExpanded(o.id)}
                      onToggle={() => toggle(o.id)}
                      actions={<></>}
                    >
                      <Run occurrence={o} />
                    </RecordRow>
                  )
                }}
              </For>
            </div>
          </Show>
        </Show>
      </Show>

      <Show when={dropped().count > 0}>
        {/* Outside the list and never activatable: a soft degrade must be
            visible in the product, not only in a log. */}
        <div class="notifications-panel__dropped" data-testid="notifications-dropped">
          {dropped().count} notifications dropped from the feed between{' '}
          {formatWhen(dropped().oldest)} and {formatWhen(dropped().newest)}
        </div>
      </Show>
    </div>
  )
}

/** What a collapsed row discloses: the constituents the feed still holds,
 *  newest first as the wire gave them, each a kit record of its own so the
 *  expansion speaks the same grammar as the list above it.
 *
 *  A member carries its OWN instant and its OWN unread mark. A row marked read
 *  and then joined by a new occurrence is unread while its old members stay
 *  read — that asymmetry is the reason the expansion exists (D2), so nothing
 *  here reads the row's `read` flag. */
function Run(props: { occurrence: Occurrence }) {
  const o = () => props.occurrence
  const held = () => o().run.length
  return (
    <div
      class="notifications-panel__run"
      role="list"
      aria-label={`Occurrences of ${props.occurrence.title}`}
    >
      <For each={props.occurrence.run}>
        {(member: RunMember) => (
          <RecordRow
            density="dense"
            title={member.title}
            meta={formatWhen(member.at)}
            selected={!member.read}
            actions={<></>}
          />
        )}
      </For>
      {/* The tail is bounded, so say how much of the run this is. Presenting
          twenty of four thousand as the whole is the failure D2 names. */}
      <Show when={o().runDropped > 0}>
        <div class="notifications-panel__run-dropped" data-testid="notifications-run-dropped">
          {held()} of {o().count} shown
        </div>
      </Show>
    </div>
  )
}
