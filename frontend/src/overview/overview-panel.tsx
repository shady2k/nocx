// The workspace overview: every workspace and every pane at once, as TEXT
// (bead nocx-edhcu).
//
// WHY THERE IS NO THUMBNAIL HERE, AND WHY THAT IS THE FEATURE. Mission Control
// works because windows look different from one another. Twelve terminals do
// not: a scaled-down terminal is grey noise, and a wall of them answers no
// question at all. So a card is a sentence — what is running, where, in what
// state, for how long, and the last line it printed — which is what makes this
// surface answer the question a browser never had to: WHICH OF MY LONG-RUNNING
// THINGS NEEDS ME. A test asserts the absence of a canvas, because "we decided
// not to" is not a thing a reader can check.
//
// EVERYTHING IT KNOWS COMES THROUGH `OverviewPort`. The surface holds no
// reference to PaneManager, the layout chain or a session; it reads a snapshot
// and expresses one intent. That is interface-first + DI (AGENTS.md), and it
// is also what lets every failure path be stated in a test rather than staged.
//
// THE KIT OWNS EVERY CONTROL IN IT. Cards are `RecordRow`; navigation and
// creation are `Button`; state is `StatusDot`. This surface owns only their
// placement and the workspace-colour tint of each containing panel. It never
// repaints a kit control.
import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
  untrack,
} from 'solid-js'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { IconButton } from '../ui/icon-button'
import { CloseIcon, PlusIcon } from '../ui/icons'
import { RecordRow } from '../ui/record-row'
import { StatusDot } from '../ui/status-dot'
import { overviewGroups, paneCountLabel, stateLabel, stateTone } from './overview-model'
import type { OverviewCard, OverviewGroup } from './overview-model'
import type { OverviewPort } from './overview-port'

export interface OverviewPanelProps {
  port: OverviewPort
  /** Dismissal. The panel never decides its own lifetime: Escape belongs to
   *  the overlay stack, and activating a card has to close it too, so both
   *  routes end in the one place that owns it. */
  onClose: () => void
  /** The clock, injected so a test can state an age instead of racing one.
   *  A test that waited five real minutes for "for 5m" would be a test that
   *  depends on timing, which AGENTS.md forbids outright. */
  now?: () => number
}

export function OverviewPanel(props: OverviewPanelProps) {
  let root: HTMLDivElement | undefined
  let hoverTimer: number | undefined
  // A counter, not a copy of the snapshot: the port owns the state and this
  // only says "ask again". Keeping a copy here would give one fact two owners
  // and they would diverge the first time a pane changed while the overview
  // was open.
  const [generation, setGeneration] = createSignal(0)
  const [closingWorkspaceId, setClosingWorkspaceId] = createSignal<string | null>(null)

  const groups = createMemo<OverviewGroup[]>(() => {
    generation()
    const clock = props.now ?? Date.now
    return overviewGroups(props.port.snapshot(), clock())
  })

  const [focusedWorkspaceId, setFocusedWorkspaceId] = createSignal<string | null>(null)
  const [hoveredWorkspaceId, setHoveredWorkspaceId] = createSignal<string | null>(null)
  const spotlightWorkspaceId = createMemo(() => {
    const currentGroups = groups()
    const focused = focusedWorkspaceId()
    if (focused !== null && currentGroups.some((group) => group.id === focused)) {
      return focused
    }
    return (
      currentGroups.find((group) => group.cards.some((card) => card.isActive))?.id ??
      currentGroups[0]?.id ??
      null
    )
  })
  const spotlightIndex = createMemo(() =>
    groups().findIndex((group) => group.id === spotlightWorkspaceId()),
  )

  // Centre a middle spotlight, but clamp at the track ends. Reserving half a
  // viewport after the first/last column made the carousel technically centred
  // and visually broken: one side became empty while the other showed slivers.
  const centreWorkspace = (id: string): void => {
    queueMicrotask(() => {
      // UNTRACKED ON PURPOSE. A microtask is not a tracked scope, and this
      // read is not a subscription: it asks whether the spotlight is STILL
      // the column this centring was queued for, one tick after the queueing.
      // Tracking it would subscribe whatever happened to be reactive around
      // the caller to a value read after the fact.
      if (untrack(spotlightWorkspaceId) !== id || !root) return
      const column = Array.from(root.querySelectorAll<HTMLElement>('.overview__column')).find(
        (candidate) => candidate.dataset.workspaceId === id,
      )
      const columns = column?.parentElement
      if (!column || !columns) return
      centreNow(column, columns)
      // AND AGAIN WHEN THE COLUMNS ARE THE WIDTH THEY ARE GOING TO BE. A
      // column's width follows its distance from the spotlight (240 → 360px,
      // overview.css) and that width is ANIMATED, so the measurement above
      // runs against the layout the board is leaving: the column being
      // centred is still narrow and the one it replaces is still wide. It
      // landed near the centre and not on it, and clicking the same column
      // twice was the workaround — the second click measured a board that
      // had finished moving.
      //
      // Waiting on the transition rather than on a duration is the point: it
      // is the same measurement, taken once the geometry is final, and it
      // stays correct if the timing in the stylesheet ever changes. Smooth
      // scrolling makes the correction part of the same motion rather than a
      // second jump.
      const settle = (event: TransitionEvent): void => {
        if (event.propertyName !== 'flex-basis') return
        columns.removeEventListener('transitionend', settle)
        if (untrack(spotlightWorkspaceId) !== id) return
        centreNow(column, columns)
      }
      columns.addEventListener('transitionend', settle)
    })
  }

  /** Put the column's centre on the track's centre, from the geometry as it
   *  stands now, clamped at both ends of the track. */
  const centreNow = (column: HTMLElement, columns: HTMLElement): void => {
    const columnBounds = column.getBoundingClientRect()
    const columnsBounds = columns.getBoundingClientRect()
    const centred =
      columns.scrollLeft +
      columnBounds.left -
      columnsBounds.left +
      columnBounds.width / 2 -
      columnsBounds.width / 2
    const left = Math.max(0, Math.min(centred, columns.scrollWidth - columns.clientWidth))
    columns.scrollTo({ left })
  }

  createEffect(() => {
    const id = spotlightWorkspaceId()
    if (id !== null) centreWorkspace(id)
  })

  onMount(() => {
    const unsubscribe = props.port.subscribe?.(() => setGeneration((n) => n + 1))
    onCleanup(() => {
      unsubscribe?.()
      if (hoverTimer !== undefined) window.clearTimeout(hoverTimer)
    })

    // Open ON the pane the person is in, so the first arrow key moves from
    // where they already are rather than from a corner they were not looking
    // at. With nothing active, the first card; with no cards at all, the
    // panel itself, so the focus trap has something to hold.
    if (!root) return
    const cards = Array.from(root.querySelectorAll<HTMLElement>(CARD_CONTROL))
    const active = cards.find(
      (c) => c.closest<HTMLElement>('.overview__card')?.dataset.active === 'true',
    )
    // Focus without moving the board; `nearest` only reveals the card inside
    // its column when that workspace has more panes than fit vertically.
    const target = active ?? cards[0] ?? root
    target.focus({ preventScroll: true })
    target.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  })

  const pick = (paneId: string): void => {
    props.port.activate(paneId)
    props.onClose()
  }

  const move = (from: HTMLElement | null, delta: number): void => {
    if (!root) return
    const cards = Array.from(root.querySelectorAll<HTMLElement>(CARD_CONTROL))
    if (cards.length === 0) return
    const current = from === null ? -1 : cards.indexOf(from)
    // Clamped, not wrapped. A wrap sends the eye across the whole window for
    // a key that means "one over", and at the ends the person has already
    // been told there is nothing further by the card not moving.
    const next = Math.min(cards.length - 1, Math.max(0, (current === -1 ? 0 : current) + delta))
    cards[next].focus()
  }

  const trapTab = (e: KeyboardEvent): void => {
    if (!root) return
    // The card's control IS a button now, so one clause covers both it and
    // the panel's own buttons — and in document order, which is what a comma
    // selector always gave anyway.
    const controls = Array.from(root.querySelectorAll<HTMLElement>('button:not(:disabled)'))
    e.preventDefault()
    if (controls.length === 0) {
      root.focus()
      return
    }
    const current = controls.indexOf(document.activeElement as HTMLElement)
    const step = e.shiftKey ? -1 : 1
    const next = (current + step + controls.length) % controls.length
    controls[next].focus()
  }

  const onKeyDown = (e: KeyboardEvent): void => {
    const target = document.activeElement as HTMLElement | null
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        e.preventDefault()
        move(target, 1)
        return
      case 'ArrowLeft':
      case 'ArrowUp':
        e.preventDefault()
        move(target, -1)
        return
      case 'Home':
        e.preventDefault()
        move(null, 0)
        return
      case 'End':
        e.preventDefault()
        move(null, Number.MAX_SAFE_INTEGER)
        return
      case 'Tab':
        trapTab(e)
        return
      default:
    }
  }

  return (
    <div
      class="overview"
      role="dialog"
      aria-modal="true"
      aria-label="Workspace overview"
      tabIndex={-1}
      ref={root}
      onKeyDown={onKeyDown}
      onMouseDown={(e) => {
        // Only the ground itself. A mousedown that started on a card is that
        // card's, and a drag that ends out here must not read as a dismissal.
        if (e.target === e.currentTarget) props.onClose()
      }}
    >
      <div class="overview__close">
        <IconButton
          ariaLabel="Close workspace overview"
          title="Close"
          type="button"
          onClick={props.onClose}
        >
          <CloseIcon />
        </IconButton>
      </div>

      <div class="overview__board">
        <div class="overview__columns">
          {/* Ungrouped is a presentation label for the default workspace's
              loose panes, not a stored workspace name. It comes first because
              that is the ordinary state before a person creates a workspace. */}
          <For each={groups()}>
            {(group, index) => {
              const displayName = group.isDefault ? 'Ungrouped' : (group.name ?? 'Workspace')
              const distance = () => Math.min(3, Math.abs(index() - spotlightIndex()))
              return (
                <section
                  class="overview__column"
                  data-workspace-id={group.id}
                  data-distance={String(distance())}
                  data-spotlight={distance() === 0 ? 'true' : undefined}
                  data-hovered={hoveredWorkspaceId() === group.id ? 'true' : undefined}
                  data-ungrouped={group.isDefault ? 'true' : undefined}
                  data-colour={group.colour ?? undefined}
                  aria-label={displayName}
                  tabIndex={-1}
                  onClick={(event) => {
                    const target = event.target
                    if (target instanceof Element && target.closest('button, .overview__card')) {
                      return
                    }
                    const alreadySpotlight = spotlightWorkspaceId() === group.id
                    event.currentTarget.focus({ preventScroll: true })
                    setFocusedWorkspaceId(group.id)
                    if (alreadySpotlight) centreWorkspace(group.id)
                  }}
                  onPointerEnter={() => {
                    if (hoverTimer !== undefined) window.clearTimeout(hoverTimer)
                    hoverTimer = window.setTimeout(() => {
                      hoverTimer = undefined
                      setHoveredWorkspaceId(group.id)
                    }, 350)
                  }}
                  onPointerLeave={() => {
                    if (hoverTimer !== undefined) window.clearTimeout(hoverTimer)
                    hoverTimer = undefined
                    setHoveredWorkspaceId(null)
                  }}
                  onFocusIn={() => {
                    if (hoverTimer !== undefined) window.clearTimeout(hoverTimer)
                    hoverTimer = undefined
                    setFocusedWorkspaceId(group.id)
                  }}
                  onFocusOut={(event) => {
                    // THE SPOTLIGHT MOVES WHEN SOMETHING TAKES FOCUS, never
                    // because focus went away. A `focusout` with no
                    // relatedTarget is focus leaving the DOCUMENT — which is
                    // what switching to another application does — and
                    // clearing on it made the board move while nobody was
                    // looking: the spotlight fell back to the workspace
                    // holding the active pane, the track scrolled there, and
                    // coming back restored focus to the column the person had
                    // actually chosen, so they watched it scroll from one to
                    // the other. Leaving the window is not a change of mind.
                    if (
                      event.relatedTarget instanceof Node &&
                      !event.currentTarget.contains(event.relatedTarget)
                    ) {
                      setFocusedWorkspaceId(null)
                    }
                  }}
                >
                  <header class="overview__column-head" data-attention={group.attention}>
                    <Button
                      variant="ghost"
                      size="sm"
                      title={displayName}
                      onClick={() => {
                        props.port.switchWorkspace(group.id)
                        props.onClose()
                      }}
                    >
                      <Show when={group.attention !== 'idle'} fallback={displayName}>
                        <StatusDot
                          tone={stateTone(group.attention)}
                          accessibleName={stateLabel(group.attention)}
                        >
                          {displayName}
                        </StatusDot>
                      </Show>
                    </Button>
                    <Badge>{paneCountLabel(group.cards.length)}</Badge>
                    <Show when={!group.isDefault && distance() === 0}>
                      <IconButton
                        ariaLabel={`Close ${displayName} workspace`}
                        title={`Close ${displayName}`}
                        type="button"
                        size="sm"
                        disabled={closingWorkspaceId() !== null}
                        onClick={() => {
                          if (closingWorkspaceId() !== null) return
                          setClosingWorkspaceId(group.id)
                          // The continuations run after the click, outside any
                          // tracked scope: they SET state and call the panel's
                          // own close, neither of which is a subscription.
                          void props.port.closeWorkspace(group.id).then(
                            (closed) => {
                              setClosingWorkspaceId(null)
                              if (closed) untrack(() => props.onClose())
                            },
                            () => setClosingWorkspaceId(null),
                          )
                        }}
                      >
                        <CloseIcon />
                      </IconButton>
                    </Show>
                  </header>

                  <OverviewCards group={group} onPick={pick} />

                  <Show when={distance() === 0}>
                    <footer class="overview__column-actions">
                      <Button
                        variant="dashed"
                        onClick={() => {
                          props.port.createTab(group.id)
                          props.onClose()
                        }}
                      >
                        <PlusIcon />
                        New tab
                      </Button>
                    </footer>
                  </Show>
                </section>
              )
            }}
          </For>
        </div>

        {/* Workspace creation belongs to the whole board, not to a fake empty
            workspace column. */}
        <div class="overview__new-workspace">
          <Button
            variant="dashed"
            onClick={() => {
              props.port.createWorkspace()
              props.onClose()
            }}
          >
            <PlusIcon />
            New workspace
          </Button>
        </div>
      </div>
    </div>
  )
}

/** What the arrow keys move between, and what opening the panel focuses: the
 *  card's RECORD NAME, which is the control that opens it (nocx-5xwub). It
 *  used to be `.overview__card [tabindex]` — the kit row itself, which was
 *  the tab stop then. The row is a `listitem` and never announced that it
 *  did anything; the name is a button and says so, so the roving lands on
 *  the thing a person is actually told about. */
const CARD_CONTROL = '.overview__card .ui-record-row__open'

function OverviewCards(props: { group: OverviewGroup; onPick: (paneId: string) => void }) {
  return (
    <div class="overview__cards" role="list">
      <For each={props.group.cards}>
        {(card: OverviewCard) => (
          <div
            class="overview__card"
            data-state={card.state}
            data-active={card.isActive ? 'true' : undefined}
          >
            <RecordRow
              title={card.title}
              meta={card.location ?? undefined}
              status={{ tone: stateTone(card.state), text: card.stateText }}
              detail={[
                ...(card.process === null ? [] : [card.process]),
                ...(card.quote === null ? [] : [card.quote]),
                ...card.excerpt,
              ]}
              actions={null}
              selected={card.isActive}
              onActivate={() => props.onPick(card.paneId)}
            />
          </div>
        )}
      </For>
    </div>
  )
}
