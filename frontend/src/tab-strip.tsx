import { For, Show, createSignal } from 'solid-js'
import { Tab } from './tab'
import { Button } from './ui/button'
import { IconButton } from './ui/icon-button'
import { ContextMenu } from './ui/context-menu'
import { Caption } from './ui/caption'
import { groupStrip } from './layout/strip-groups'
import { WORKSPACE_DRAG_TYPE } from './layout/strip-drag'
import { isWorkspaceColour, type WorkspaceColour } from './layout/workspace-colours'
import { groupAttention } from './layout/workspace-colour'
import { SearchField } from './ui/search-field'
import { ResizeHandle } from './ui/resize-handle'
import { WorkspaceChip } from './workspace-chip'
import type { WorkspaceMenuRow } from './workspace-menu'
import {
  ChevronDownIcon,
  CloseIcon,
  KeyIcon,
  LayersIcon,
  PencilIcon,
  PinIcon,
  PlugIcon,
  PlusIcon,
  TextQuoteIcon,
} from './ui/icons'
import type { JSX, Setter } from 'solid-js'
import { createStore } from 'solid-js/store'
import { render } from 'solid-js/web'
import type { AgentStatus } from './agent-status'

/**
 * The vertical strip's width bounds, in CSS pixels.
 *
 * MIN 160. A row spends its width on the tab's index, its title and — for a
 * grouped row — its indent; below this the title is an ellipsis and the
 * column has stopped answering "which tab is this".
 *
 * MAX 480. The strip sits beside the panes and its job is to name them, not
 * to compete with them: 480 is half of the narrowest window the app is built
 * for once the activity bar is accounted for.
 *
 * DEFAULT 240 — the fixed width it had before it could be dragged, so a user
 * who never touches the edge sees no change.
 */
const TABSTRIP_WIDTH_MIN = 160
const TABSTRIP_WIDTH_MAX = 480
const TABSTRIP_WIDTH_STEP = 8
const TABSTRIP_WIDTH_DEFAULT = 240

// ═══════════════════════════════════════════════════════════════════════════
// TabStrip — presentation port for tab chrome
// ═══════════════════════════════════════════════════════════════════════════

/** The display state a TabStrip reads from each tab. */
export interface PaneView {
  readonly id: number
  readonly title: string
  /** Title shown before content publishes its first dynamic title. */
  readonly displayTitle?: string
  readonly hasActivity: boolean
  readonly agentStatus: AgentStatus | null
  readonly tooltip: string
  /** The tab's location for the strip's second line, or '' when the title already
   *  says it — see Tab.subtitle. */
  readonly subtitle: string
  /** What is happening in the pane, in one line — the same sentence the
   *  overview's card prints (`Pane.preview`). The vertical strip's second
   *  line prefers it; '' when the pane has nothing to say. */
  readonly preview: string
  /** When true, the tab offers a save action (alias adoption). */
  readonly adoptable?: boolean
  readonly onAdopt?: (() => void) | null
  /** The environment degraded or became uncertain (nocx-4t37.2): tab
   *  chrome carries at most this warning mark, never a permanent badge. */
  readonly warning?: boolean
  /** What the mark means (nocx-5uu5). */
  readonly warningLabel?: string
  /** The tab's colour as the BACKEND stores it, or null for an undecorated
   *  tab (nocx-isoph.4, §4.5). The strip renders it and never chooses it. */
  readonly colour?: string | null
  /** Whether the backend has this tab pinned. The strip draws the mark and
   *  places the tab (layout/strip-order.ts); the flag is stored. */
  readonly pinned?: boolean
  /** True only after the backend confirms native sandbox readiness. */
  readonly sandboxed?: boolean
  readonly paneId: string
  /** Which group this row is drawn under (nocx-isoph.5). The AXIS is the
   *  caller's — workspace here, `descriptor.surfaceType` in nocx-jv3q.1,
   *  project/host/worktree/branch in design §9 — and the strip only cuts the
   *  list where the key changes. Absent means "not grouped", which is one
   *  anonymous group and exactly what an ungrouped strip already looked
   *  like. */
  readonly groupKey?: string
  /** How far in the row is drawn: 0 for a top-level row, +1 per lineage
   *  generation (layout/strip-tree.ts). The horizontal strip is flat — the
   *  tree stays in the vertical one (§4.3). */
  readonly depth?: number
  onDisplayChange: (() => void) | null
}

/**
 * Reactive display-state record for a single tab, keyed by tab id.
 * Stored in a local Solid store so JSX expressions (each compiled into
 * their own reactive computation) are fine-grained reactive.
 * Uses displayTitle when the content has not published a dynamic title yet.
 */
interface PaneDisplayRecord {
  title: string
  tooltip: string
  subtitle: string
  preview: string
  adoptable: boolean
  warning: boolean
  warningLabel: string
  hasActivity: boolean
  agentStatus: AgentStatus | null
  colour: string | null
  pinned: boolean
  groupKey: string
  depth: number
  sandboxed: boolean
}

/** What stands above one group of rows, or null for a group that draws no
 *  heading — the default workspace's, which is top-level rows and nothing
 *  else (§4.2). The strip is TOLD these; deciding one is the axis's job
 *  (layout/strip-groups.ts). */
interface StripGroupHeading {
  readonly key: string
  readonly heading: string | null
  /** The workspace's stored colour, or null for one that has none — the
   *  default workspace, and any row the backend minted. It travels WITH the
   *  heading because it is the same fact about the same object, and because
   *  the strip must not derive it: it was derived once, by hashing the id,
   *  and that is exactly what nocx-2mipw replaced. */
  readonly colour: string | null
}

/** A heading in the strip's flat list of things to draw. An object rather
 *  than a bare string so it is told apart from a row by shape, and so it can
 *  keep a stable identity across redraws.
 *
 *  It carries its group's KEY as well as its text (nocx-isoph.7): a heading is
 *  the vertical strip's handle on the workspace it heads, and a menu opened
 *  from it has to name a subject. Deriving the subject from the text would be
 *  a second, lossier identity — two workspaces may be called the same thing. */
interface StripHeadingItem {
  readonly key: string
  readonly heading: string
}

function isHeading(item: StripHeadingItem | PaneView): item is StripHeadingItem {
  return 'heading' in item
}

/** How deep a row is drawn before the indent stops growing. A 240px column
 *  cannot indent forever, and a label squeezed to nothing is worse than a
 *  generation that shares its neighbour's indent. The DEPTH is unbounded; the
 *  drawing of it is not. */
const MAX_DRAWN_DEPTH = 6

/** Presentation port for tab chrome. */
export interface TabStrip {
  readonly orientation: Orientation
  mount(container: HTMLElement): void
  addPane(tab: PaneView): void
  removePane(paneId: number): void
  setActive(paneId: number): void
  reorder(tabs: readonly PaneView[]): void
  /** What to write above each group. The strip cuts its rows by the key each
   *  row carries and looks the heading up here, so a group nobody named draws
   *  none — and no row can go missing for want of a heading. */
  setGroupHeadings(headings: readonly StripGroupHeading[]): void
  /**
   * WHICH GROUP IS UNFOLDED in the horizontal strip, and null for none.
   *
   * It replaced `setWorkspaceChip`, and the replacement is the whole rework:
   * the row used to be told which workspace it was *showing*, and drew that
   * one alone behind a chip whose menu was the only way anywhere else. Now
   * every workspace is in the row and this says which one has its tabs out.
   * The strip is TOLD rather than deriving it from the active pane, because
   * the active pane can be one the chain does not hold at all — Settings, a
   * file viewer — and a group that folded itself the moment you opened
   * Settings would be chrome moving for a reason nobody can see.
   *
   * The vertical strip ignores it: that surface unfolds everything by design
   * (§4.3), so folding there would hide the finished worker it exists to
   * show.
   */
  setExpandedGroup(key: string | null): void
  onSwitchWorkspace: ((workspaceId: string) => void) | null
  onNewWorkspace: (() => void) | null
  /**
   * SHOW EVERY WORKSPACE AND EVERYTHING IN IT — the overview (nocx-edhcu).
   *
   * The button at the head of the row raises this and no longer creates. It
   * changed meaning deliberately: its glyph is a stack of plates, which has
   * always said "the workspaces" and never said "a new one", and the surface
   * it now opens shows CONTENTS rather than names — which is the complaint
   * the whole rework began with. Creating moved into the overview's own `+`
   * and into this strip's menu, where it has room for a name.
   */
  onOpenOverview: (() => void) | null
  /** The rows a workspace's own menu offers, asked for per heading
   *  (nocx-isoph.7). The strip DECIDES none of them: it is handed the rows by
   *  whoever owns the workspace set, exactly as it is handed a heading rather
   *  than working one out. Null when there is no chain to act on. */
  workspaceMenuRows: ((workspaceId: string) => WorkspaceMenuRow[]) | null
  /** Close a whole workspace, with the tabs in it. The strip raises the
   *  intent and closes nothing itself — what closing a workspace does to its
   *  panes is PaneManager's, and the menu row of the same name has always
   *  gone the same way. */
  onCloseWorkspace: ((workspaceId: string) => void) | null
  /** Put `movedId` next to `targetId`, before it or after it — the drag of a
   *  workspace heading. The strip raises the move and computes no order: what
   *  the wire takes is a whole permutation, and that belongs to whoever holds
   *  the set (PaneManager, through workspace-menu.ts's `moveWorkspace`). */
  onMoveWorkspace: ((movedId: string, targetId: string, before: boolean) => void) | null
  /** The CURRENT workspace was asked to close. The strip names no workspace
   *  in this intent — it shows one at a time, so "the current one" is the
   *  only thing it can mean, and the ask and the close belong to
   *  PaneManager.closeWorkspace (nocx-isoph.6). */
  onActivate: ((paneId: number) => void) | null
  onClose: ((paneId: number) => void) | null
  onNewPane: (() => void) | null
  onReorder: ((fromId: number, toId: number, before: boolean) => void) | null
  /** The tab's decoration, asked for from its context menu (nocx-isoph.4).
   *  Three intents rather than one "update": a patch where a missing field
   *  and a null field mean different things is how "what changed" stops
   *  being answerable, which is the same reason the wire has three methods.
   *  The strip raises them; the backend decides and the strip re-renders. */
  onRename: ((paneId: number) => void) | null
  /** null clears the colour, which is a real operation and not a no-op. */
  onRecolour: ((paneId: number, colour: string | null) => void) | null
  onPin: ((paneId: number, pinned: boolean) => void) | null
  onQuickConnect: (() => void) | null
  onInsertSecret: (() => void) | null
  /** The snippets action was pressed. Shaped exactly like onQuickConnect
   *  and onInsertSecret because it opens exactly what they open — the same
   *  palette, in its snippets variant (design §10.3). The strip knows
   *  nothing about a library. */
  onSnippets: (() => void) | null
}

// ═══════════════════════════════════════════════════════════════════════════
// Internal types
// ═══════════════════════════════════════════════════════════════════════════

export type Orientation = 'horizontal' | 'vertical'

// ═══════════════════════════════════════════════════════════════════════════
// TabStripBase — Solid renders every tab button via <For>, keyed by tab
// object identity. Display-state reactivity comes from a local store
// (createStore) that mirrors onDisplayChange-driven updates; the JSX reads
// store values inline (never hoisted into local variables), so Solid
// compiles each JSX expression into its own reactive computation.
// No createEffect or DOM patching is needed.
// ═══════════════════════════════════════════════════════════════════════════

abstract class TabStripBase implements TabStrip {
  protected dispose: (() => void) | null = null
  protected container: HTMLElement | null = null
  private mounted = false

  // Solid stores/signals — set during mount(), used by imperative API
  private _setPaneViews!: Setter<PaneView[]>
  private _getPaneViews!: () => PaneView[]
  private _setDisplay!: (...args: unknown[]) => void
  private _setGroupHeadings!: Setter<StripGroupHeading[]>
  private _setExpandedGroup!: Setter<string | null>
  /** What the strip was told before it was mounted. A caller that sets the
   *  expanded group or the headings first and mounts second must not lose
   *  them — the composition root replaces the whole strip when the placement
   *  setting changes, and the order of those two calls is not its business. */
  private pendingHeadings: StripGroupHeading[] = []
  private pendingExpanded: string | null = null

  public abstract readonly orientation: Orientation

  // Intent callbacks
  onActivate: ((paneId: number) => void) | null = null
  onClose: ((paneId: number) => void) | null = null
  onNewPane: (() => void) | null = null
  onReorder: ((fromId: number, toId: number, before: boolean) => void) | null = null
  onRename: ((paneId: number) => void) | null = null
  onRecolour: ((paneId: number, colour: string | null) => void) | null = null
  onPin: ((paneId: number, pinned: boolean) => void) | null = null
  onQuickConnect: (() => void) | null = null
  onInsertSecret: (() => void) | null = null
  onSnippets: (() => void) | null = null
  onSwitchWorkspace: ((workspaceId: string) => void) | null = null
  onNewWorkspace: (() => void) | null = null
  onOpenOverview: (() => void) | null = null
  workspaceMenuRows: ((workspaceId: string) => WorkspaceMenuRow[]) | null = null
  onCloseWorkspace: ((workspaceId: string) => void) | null = null
  onMoveWorkspace: ((movedId: string, targetId: string, before: boolean) => void) | null = null

  /** Subclasses set up container attributes (class, aria). */
  protected abstract setupContainer(container: HTMLElement): void

  mount(container: HTMLElement): void {
    if (this.mounted) return
    this.mounted = true
    this.container = container

    this.setupContainer(container)
    container.addEventListener('keydown', this.onTablistKeydown)
    this.dispose = render(() => {
      const [paneViews, setPaneViews] = createSignal<PaneView[]>([])
      const [display, setDisplay] = createStore<{
        records: Record<number, PaneDisplayRecord>
        activeId: number
      }>({ records: {}, activeId: -1 })
      const [searchQuery, setSearchQuery] = createSignal('')
      // The tab menu: which tab it belongs to and where it was opened. One
      // menu for the whole strip rather than one per row — a menu is a
      // singleton on screen, and a component per tab would be N listeners
      // for a thing at most one of which can be open.
      const [menu, setMenu] = createSignal<{ paneId: number; x: number; y: number } | null>(null)
      // The workspace menu is its OWN signal rather than a variant of the tab
      // menu above: the two are opened from different things, carry different
      // rows and can be reached in the same frame, and one signal holding
      // either would make "which menu is open" a question with two answers.
      const [workspaceMenu, setWorkspaceMenu] = createSignal<{
        rows: WorkspaceMenuRow[]
        x: number
        y: number
      } | null>(null)
      const [groupHeadings, setGroupHeadings] = createSignal<StripGroupHeading[]>(
        this.pendingHeadings,
      )
      const [expandedGroup, setExpandedGroup] = createSignal<string | null>(this.pendingExpanded)
      // The strip's own overflow menu — the caret at the end of the row. Its
      // own signal for the same reason the workspace menu has one: three
      // menus that can be reached in the same frame, and one signal holding
      // any of them would make "which menu is open" a question with three
      // answers.
      const [stripMenu, setStripMenu] = createSignal<{ x: number; y: number } | null>(null)
      // WHICH ROW IS BEING DRAGGED, so every other row can say whether it
      // would take it. The dragged id cannot be read from the DataTransfer
      // during a dragover — the browser withholds the data until the drop —
      // so the strip remembers what its own row told it at dragstart.
      const [dragging, setDragging] = createSignal<number | null>(null)
      // THE VERTICAL STRIP'S WIDTH. A panel beside the panes is a thing the
      // user drags, in both placements — the sidebar has answered to a drag
      // since nocx-qmcu and this one was a fixed 240px, so the same window
      // behaved two ways depending on where the tabs were.
      //
      // NOT PERSISTED YET, and deliberately not through the settings
      // registry: a width produced by dragging an edge is UI state rather
      // than a deliberate choice, and registering it would put a second
      // "width" row on the Settings page — the defect nocx-mqie.3 exists to
      // remove for the sidebar. It settles into the UI-state document that
      // epic builds, beside the sidebar's.
      const [stripWidth, setStripWidth] = createSignal(TABSTRIP_WIDTH_DEFAULT)

      this._getPaneViews = paneViews
      this._setPaneViews = setPaneViews
      this._setDisplay = setDisplay
      this._setGroupHeadings = setGroupHeadings
      this._setExpandedGroup = setExpandedGroup

      /**
       * What the strip draws, top to bottom: headings and rows in one list.
       *
       * THE CUT IS THE SHARED MECHANISM (layout/strip-groups.ts) and the axis
       * is an input: this strip groups by whatever key each row carries and
       * looks the heading up in what it was told. Which axis that is — the
       * workspace here, the surface type in nocx-jv3q.1, project or host or
       * worktree or branch in design §9 — is decided outside, so a second
       * axis is a different `groupKey`, never a second grouping.
       *
       * A HEADING GATHERS ITS ROWS; A GROUP WITH NO HEADING IS JUST ROWS, AND
       * THEY DO NOT MOVE. That is the default workspace, whose tabs are
       * top-level rows and nothing else (§4.2) — and it is also what keeps a
       * pane the chain does not hold (Settings, a viewer) exactly where it
       * already was. Sweeping those to the end broke "the last tab is the one
       * that just opened" in four e2e specs once already.
       *
       * One flat list rather than a list of groups, and that is not a style
       * choice: `<For>` reconciles by REFERENCE, so a list whose items are
       * freshly built group objects rebuilds every row's DOM on every change —
       * and with it focus, the drag in progress and the node identity
       * ADR-0012 §1 depends on. The rows here are the same PaneView objects
       * throughout, and a heading keeps its identity through `headingItems`.
       */
      const headingItems = new Map<string, StripHeadingItem>()
      const headingItem = (key: string, heading: string): StripHeadingItem => {
        const cached = headingItems.get(`${key} ${heading}`)
        if (cached) return cached
        const item: StripHeadingItem = { key, heading }
        headingItems.set(`${key} ${heading}`, item)
        return item
      }
      /** Whether a row survives the strip's filter. Rows are HIDDEN rather
       *  than removed — a filtered row keeps its DOM, its identity and its
       *  place — so this is also what a heading has to ask before it draws:
       *  a heading over a group the filter has emptied reads as a broken
       *  list. */
      const matchesFilter = (view: PaneView): boolean => {
        const q = searchQuery().toLowerCase().trim()
        if (!q) return true
        const record = display.records[view.id]
        return (
          (record?.title ?? '').toLowerCase().includes(q) ||
          (record?.tooltip ?? '').toLowerCase().includes(q)
        )
      }

      /**
       * Whether a row is FOLDED AWAY behind its workspace's pill.
       *
       * Only the horizontal strip folds, and only rows that belong to a named
       * group. A row with no group — the default workspace's tabs (§4.2),
       * Settings, a file viewer, a pane whose create has not answered — is
       * never folded, because there is no pill standing for it and folding it
       * would remove it from the product with nothing left to say where it
       * went. That is the failure this whole rework exists to end, and it
       * would be a poor joke to reintroduce it here.
       *
       * A folded row is HIDDEN, exactly like a filtered one: it keeps its DOM,
       * its identity and its place in the order, so unfolding is a repaint
       * rather than a rebuild — which is what ADR-0012 §1's node identity and
       * any drag in progress depend on.
       */
      const folded = (view: PaneView): boolean => {
        if (this.orientation === 'vertical') return false
        const key = display.records[view.id]?.groupKey ?? ''
        if (key === '') return false
        if (groupHeadings().find((g) => g.key === key)?.heading == null) return false
        return key !== expandedGroup()
      }

      /** The colour of the run a row sits in, or undefined for a row that
       *  sits in no run at all — an ungrouped tab is not a one-tab group and
       *  must not be drawn as one, and a workspace nobody coloured is drawn
       *  without one rather than with a colour this renderer invented. */
      const groupColourOf = (key: string): WorkspaceColour | undefined => {
        if (key === '') return undefined
        const group = groupHeadings().find((g) => g.key === key)
        if (group?.heading == null) return undefined
        return isWorkspaceColour(group.colour) ? group.colour : undefined
      }

      /** What one workspace's pill has to say about the panes inside it. Read
       *  off the rows the strip already holds — every pane is in the row now,
       *  folded or not, so nothing has to be plumbed in for this. */
      const groupMembers = (key: string): PaneView[] =>
        paneViews().filter((v) => (display.records[v.id]?.groupKey ?? '') === key)

      const items = (): Array<StripHeadingItem | PaneView> => {
        const rows = paneViews()
        const groups = groupStrip(rows, {
          key: (view) => display.records[view.id]?.groupKey ?? '',
          heading: (key) => groupHeadings().find((g) => g.key === key)?.heading ?? null,
        })
        const out: Array<StripHeadingItem | PaneView> = []
        const gathered = new Set<string>()
        for (const row of rows) {
          const group = groups.find((g) => g.rows.includes(row))
          if (!group || group.heading === null) {
            out.push(row)
            continue
          }
          if (gathered.has(group.key)) continue
          gathered.add(group.key)
          if (group.rows.some(matchesFilter)) out.push(headingItem(group.key, group.heading))
          out.push(...group.rows)
        }
        return out
      }

      /** The actions a tab offers, in the order they are reached for. The
       *  strip builds the rows; every one of them raises an intent and
       *  decides nothing — the answer comes back through the store. */
      /**
       * A MENU OF ACTIONS, AND THE COLOURS ARE NOT ACTIONS. This menu used to
       * carry the palette inline — one row per colour word, "Green", "Amber",
       * "Red", "Violet", plus "No colour" — so choosing a colour meant reading
       * a list of nouns that showed none of them, and the three things a
       * person actually does to a tab were spread around them. Name and colour
       * are one decision about one tab, and they are asked for together now,
       * in the form the workspace already used (name-colour-dialog.tsx).
       */
      const menuItems = (paneId: number) => {
        const pinned = display.records[paneId]?.pinned === true
        return [
          {
            id: 'rename',
            label: 'Rename…',
            icon: PencilIcon,
            onSelect: () => this.onRename?.(paneId),
          },
          {
            id: 'pin',
            label: pinned ? 'Unpin' : 'Pin',
            icon: PinIcon,
            onSelect: () => this.onPin?.(paneId, !pinned),
          },
          {
            id: 'close',
            label: 'Close',
            icon: CloseIcon,
            onSelect: () => this.onClose?.(paneId),
          },
        ]
      }

      /** One thing the strip draws: a group heading, or a tab row. Written
       *  as a function rather than a conditional inside the list so each
       *  branch is a plain expression — and so the Tab's props are read once,
       *  per item, exactly as they were before headings existed. */
      const drawItem = (item: StripHeadingItem | PaneView): JSX.Element => {
        if (isHeading(item)) {
          // THE HORIZONTAL STRIP DRAWS A WORKSPACE AS A PILL IN THE ROW, and
          // the vertical one as a heading over a column. Same object, same
          // menu, two orientations — which is why this is a branch inside one
          // drawItem rather than two components that would drift.
          if (this.orientation === 'horizontal') {
            const members = () => groupMembers(item.key)
            return (
              <WorkspaceChip
                name={item.heading}
                colour={groupColourOf(item.key) ?? null}
                count={members().length}
                attention={groupAttention(
                  members().map((v) => ({
                    hasActivity: display.records[v.id]?.hasActivity === true,
                    agentStatus: display.records[v.id]?.agentStatus ?? null,
                    warning: display.records[v.id]?.warning === true,
                  })),
                )}
                expanded={item.key === expandedGroup()}
                // ONE CLICK IS THE WHOLE OF SWITCHING NOW. The strip raises
                // the intent and picks nothing: which of the workspace's tabs
                // ends up in front is an MRU question, and the MRU belongs to
                // PaneManager. A strip that chose "the first row" would be a
                // second answer to a question that already has an owner.
                onActivate={() => this.onSwitchWorkspace?.(item.key)}
                onMenu={(x, y) => {
                  const rows = this.workspaceMenuRows?.(item.key) ?? []
                  if (rows.length === 0) return
                  setWorkspaceMenu({ rows, x, y })
                }}
              />
            )
          }
          // THE HEADING IS THE HANDLE (nocx-isoph.7). A vertical strip shows
          // every workspace at once, so the thing standing above a group is
          // where that workspace's own actions belong — the chip is the same
          // mechanism placed on the other orientation, and both take their
          // rows from workspace-menu.ts so they cannot come to disagree about
          // what a workspace may do.
          //
          // It stays a heading and does not become a button: the element, its
          // class and its Caption are unchanged, and the click is added to
          // them. A row that turned into a control when a second workspace
          // existed would be chrome appearing on a counter, which is the rule
          // §4.2 withdrew.
          // THE CONTROL IS THE KIT'S, and the heading places it — the same
          // shape the chip uses, for the same reason. A hand-rolled
          // role="button" was the first attempt and eslint's
          // nocx/no-role-impersonation refused it: a button that opens a menu
          // is a kit primitive, not a behavioural unit outside the kit's
          // vocabulary, so the rule's answer is to use the component rather
          // than to declare a composite contract. That is also what gets the
          // focus ring and the keyboard for free instead of re-deriving them.
          //
          // The div stays as the row's LAYOUT and paints nothing: a surface
          // may place a kit component and may never repaint one, and the
          // Caption inside keeps the typography a heading has always had.
          //
          // Every heading that is drawn has rows to open — the default draws
          // none at all (§4.2) — so there is no heading in the product that
          // is a control leading nowhere.
          return (
            // THE HEADING CARRIES THE WORKSPACE'S COLOUR, because in this
            // orientation nothing else can. The colour is IDENTITY — the one
            // thing about a workspace a person reads without looking at it —
            // and the horizontal strip puts it on the pill. The column had no
            // pill and no wash (tab.css suppresses it here on purpose: the
            // heading above the rows already says where a run begins), so the
            // colour was simply absent and every workspace looked alike.
            <div
              class="tabstrip-group-heading"
              data-colour={groupColourOf(item.key)}
              // A WORKSPACE IS DRAGGED BY ITS HEADING, which is the thing that
              // stands for it — the same object the menu's Move up / Move down
              // act on, and the same rows this drag replaces for anyone who
              // would rather point than count. The payload is its own MIME
              // type: a tab and a workspace are both being dragged around the
              // same rail, and a heading that accepted a tab's plain text
              // would take a drop it cannot honour.
              draggable={true}
              onDragStart={(e: DragEvent) => {
                e.dataTransfer?.setData(WORKSPACE_DRAG_TYPE, item.key)
                e.dataTransfer?.setData('text/plain', item.heading)
              }}
              onDragOver={(e: DragEvent) => {
                if (!e.dataTransfer?.types.includes(WORKSPACE_DRAG_TYPE)) return
                e.preventDefault()
                const row = e.currentTarget
                if (!(row instanceof HTMLElement)) return
                const rect = row.getBoundingClientRect()
                row.dataset.dropEdge = e.clientY < rect.top + rect.height / 2 ? 'before' : 'after'
              }}
              onDragLeave={(e: DragEvent) => {
                if (e.currentTarget instanceof HTMLElement) delete e.currentTarget.dataset.dropEdge
              }}
              onDrop={(e: DragEvent) => {
                const row = e.currentTarget
                const edge = row instanceof HTMLElement ? row.dataset.dropEdge : undefined
                if (row instanceof HTMLElement) delete row.dataset.dropEdge
                const moved = e.dataTransfer?.getData(WORKSPACE_DRAG_TYPE)
                if (!moved || moved === item.key) return
                e.preventDefault()
                this.onMoveWorkspace?.(moved, item.key, edge !== 'after')
              }}
            >
              <Button
                variant="ghost"
                size="sm"
                title={`Workspace: ${item.heading}`}
                onClick={(e: MouseEvent) => {
                  const anchor = e.currentTarget
                  if (!(anchor instanceof HTMLElement)) return
                  const rows = this.workspaceMenuRows?.(item.key) ?? []
                  if (rows.length === 0) return
                  const rect = anchor.getBoundingClientRect()
                  setWorkspaceMenu({ rows, x: rect.left, y: rect.bottom })
                }}
              >
                <Caption size="context">{item.heading}</Caption>
              </Button>
              {/* CLOSING THE WHOLE RUN, from the row that stands for it. The
                  action already existed as a menu row and stays there — this
                  is the same intent given the place a person reaches for it,
                  exactly as a tab's own close mark sits on the tab rather
                  than only in its menu. It is the kit's IconButton, and the
                  heading only places it. */}
              <IconButton
                size="sm"
                ariaLabel={`Close workspace ${item.heading}`}
                title="Close workspace"
                onClick={(e: MouseEvent) => {
                  e.stopPropagation()
                  this.onCloseWorkspace?.(item.key)
                }}
              >
                {'\u00d7'}
              </IconButton>
            </div>
          )
        }
        return (
          <Tab
            id={`tab-btn-${item.id}`}
            paneId={item.id}
            controlledPaneId={item.paneId}
            index={paneViews().indexOf(item)}
            depth={Math.min(display.records[item.id]?.depth ?? 0, MAX_DRAWN_DEPTH)}
            active={display.activeId === item.id}
            agentStatus={display.records[item.id]?.agentStatus ?? null}
            adoptable={display.records[item.id]?.adoptable === true}
            warning={display.records[item.id]?.warning === true}
            warningLabel={display.records[item.id]?.warningLabel || undefined}
            sandboxed={display.records[item.id]?.sandboxed === true}
            onAdopt={item.onAdopt ?? undefined}
            title={display.records[item.id]?.title ?? ''}
            tooltip={display.records[item.id]?.tooltip ?? ''}
            subtitle={display.records[item.id]?.subtitle ?? ''}
            preview={display.records[item.id]?.preview ?? ''}
            hasActivity={display.records[item.id]?.hasActivity === true}
            tabIndex={display.activeId === item.id ? 0 : -1}
            orientation={this.orientation}
            hidden={!matchesFilter(item) || folded(item)}
            colour={display.records[item.id]?.colour ?? undefined}
            // WHICH RUN OF TABS THIS ONE BELONGS TO, so the unfolded group
            // reads as a segment rather than as tabs that happen to sit next
            // to a pill. It is the GROUP's colour and never the tab's — the
            // tab's own `colour` above is a mark the user put on it — so the
            // two are separate attributes and tab.css draws them differently.
            groupColour={
              folded(item) ? undefined : groupColourOf(display.records[item.id]?.groupKey ?? '')
            }
            pinned={display.records[item.id]?.pinned === true}
            onActivate={() => this.onActivate?.(item.id)}
            onClose={(id) => this.onClose?.(id)}
            onReorder={(fromId, toId, before) => this.onReorder?.(fromId, toId, before)}
            onDragBegin={(id) => setDragging(id)}
            onDragFinish={() => setDragging(null)}
            // A reorder is one workspace's business (see Tab.dropAllowed), so
            // a row accepts a drop only from its own run.
            dropAllowed={
              dragging() === null ||
              (display.records[dragging()!]?.groupKey ?? '') ===
                (display.records[item.id]?.groupKey ?? '')
            }
            onMenu={(paneId, x, y) => setMenu({ paneId, x, y })}
          />
        )
      }

      return (
        <>
          <Show when={this.orientation === 'vertical'}>
            <div class="tabstrip-header">
              {/* THE HEAD OF THE RAIL, and it is the head of the row in the
                  other placement for the same reason: the overview is where a
                  workspace is looked at whole, so it stands before the list of
                  them rather than among the actions at the far end. */}
              <div class="tabstrip-lead">
                <IconButton
                  ariaLabel="Show all workspaces"
                  title="Show all workspaces"
                  onClick={() => this.onOpenOverview?.()}
                >
                  <LayersIcon />
                </IconButton>
              </div>
              <div class="tabstrip-search">
                <SearchField
                  value={searchQuery()}
                  onInput={(v) => setSearchQuery(v)}
                  placeholder="Filter tabs…"
                  ariaLabel="Filter tabs"
                  onKeyDown={(e) => {
                    if (e.key === 'Escape' && searchQuery() !== '') {
                      e.stopPropagation()
                    }
                  }}
                />
              </div>
              {/* ONE VOCABULARY, BOTH PLACEMENTS. This header carried the
                  five same-weight glyphs the horizontal strip shed — new tab,
                  new workspace, quick connect, insert a secret, snippets — and
                  the layers glyph among them meant NEW WORKSPACE while the
                  identical glyph at the head of the horizontal row meant SHOW
                  ALL WORKSPACES. One mark with two meanings is not a
                  difference in layout, it is a person learning a control twice
                  and being wrong once; which of the two they get depended on a
                  placement setting.

                  So the row is the horizontal one's: the layers mark opens the
                  overview, `+` opens a tab, and the caret opens everything
                  else. Nothing was removed — quick connect, the secret picker
                  and snippets are rows in that menu, where they have their
                  names, and creating a workspace is a row there too (it cannot
                  live on a heading: the default draws none, so a create
                  offered only there would be unreachable for exactly the
                  person who has never made a workspace — nocx-isoph.7). */}
              <div class="tabstrip-actions">
                <IconButton ariaLabel="New tab" square onClick={() => this.onNewPane?.()}>
                  <PlusIcon />
                </IconButton>
                <IconButton
                  ariaLabel="More"
                  title="More"
                  onClick={(e: MouseEvent) => {
                    const anchor = e.currentTarget
                    if (!(anchor instanceof HTMLElement)) return
                    const rect = anchor.getBoundingClientRect()
                    setStripMenu({ x: rect.left, y: rect.bottom })
                  }}
                >
                  <ChevronDownIcon />
                </IconButton>
              </div>
            </div>
          </Show>
          {/* The chip that used to stand here is gone, and with it the
              dropdown that was the only way between workspaces. Every
              workspace is IN the row now — see drawItem's horizontal branch.
              What stands here instead is the way to see them all at once:
              the overview (nocx-edhcu), which answers with CONTENTS what the
              old chip's menu answered with names.

              IT IS OUTSIDE `.tabs-container` ON PURPOSE. Inside, it would
              scroll away with the tabs the moment the row overflowed — and it
              is exactly when a person has more tabs than fit that they reach
              for the picture of all of them. Here it is pinned, and it reads
              as what it is: the head of the row of workspaces. */}
          <Show when={this.orientation === 'horizontal'}>
            <div class="tabstrip-lead">
              <IconButton
                ariaLabel="Show all workspaces"
                title="Show all workspaces"
                onClick={() => this.onOpenOverview?.()}
              >
                <LayersIcon />
              </IconButton>
            </div>
          </Show>
          <div
            class="tabs-container"
            // THREE SOURCES, BECAUSE OVERFLOW CHANGES THREE WAYS. The
            // scroll event covers moving within a row that is already too
            // long; the resize observer covers the row or the window changing
            // size under a scroll position that did not move; the mutation
            // observer covers the CONTENTS growing inside a box whose own
            // size did not change — which is the whole of a restore, where
            // the rows arrive after this ref has run and every title is
            // rewritten again when its pane publishes one. Any of the three
            // alone leaves a stale fade: a window dragged narrower fires no
            // scroll event, and a row that is already at its maximum width
            // resizes not at all when the eighth tab lands in it.
            // None is unregistered, and none needs to be: all three are
            // rooted in this element, the composition root drops the whole
            // strip and its host together when the placement setting changes
            // (PaneManager.replaceStrip), and an observer whose only target
            // has gone is collected with it.
            ref={(el: HTMLElement) => {
              // Coalesced to one measurement per frame — a restore mutates
              // the row dozens of times in a tick, and `scrollWidth` is a
              // forced layout every time it is read.
              let queued = false
              const measure = (): void => {
                if (queued) return
                queued = true
                requestAnimationFrame(() => {
                  queued = false
                  this.updateOverflow()
                })
              }
              el.addEventListener('scroll', () => this.updateOverflow(), { passive: true })
              new ResizeObserver(() => this.updateOverflow()).observe(el)
              new MutationObserver(measure).observe(el, {
                childList: true,
                subtree: true,
                characterData: true,
              })
              // After the first paint: `scrollWidth` is meaningless until the
              // rows are laid out, and this ref runs before they are.
              measure()
              // And once more when the UI font has actually arrived: a web
              // font swapping in re-lays every tab without mutating one, so
              // a row that only just fits in the fallback metrics overflows
              // silently. `document.fonts` is absent in jsdom.
              void document.fonts?.ready.then(measure)
            }}
          >
            {/* A group with no heading draws NOTHING above its rows — no
                element, no empty caption, no wrapper. That is what makes the
                default workspace's rows top-level rows (§4.2) rather than
                rows under a blank header, and it is why the default's chrome
                is identical whether or not another workspace exists. */}
            <For each={items()}>{(item) => drawItem(item)}</For>
          </div>
          <Show when={menu()} keyed>
            {(open) => (
              <ContextMenu
                open
                x={open.x}
                y={open.y}
                items={menuItems(open.paneId)}
                onClose={() => setMenu(null)}
                data-testid="tab-menu"
              />
            )}
          </Show>
          <Show when={stripMenu()} keyed>
            {(open) => (
              <ContextMenu
                open
                x={open.x}
                y={open.y}
                items={[
                  {
                    id: 'quick-connect',
                    label: 'Quick connect…',
                    icon: PlugIcon,
                    onSelect: () => this.onQuickConnect?.(),
                  },
                  {
                    id: 'insert-secret',
                    label: 'Insert a secret…',
                    icon: KeyIcon,
                    onSelect: () => this.onInsertSecret?.(),
                  },
                  {
                    id: 'snippets',
                    label: 'Snippets…',
                    icon: TextQuoteIcon,
                    onSelect: () => this.onSnippets?.(),
                  },
                  {
                    id: 'new-workspace',
                    label: 'New workspace…',
                    icon: LayersIcon,
                    onSelect: () => this.onNewWorkspace?.(),
                  },
                ]}
                onClose={() => setStripMenu(null)}
                data-testid="strip-menu"
              />
            )}
          </Show>
          <Show when={workspaceMenu()} keyed>
            {(open) => (
              <ContextMenu
                open
                x={open.x}
                y={open.y}
                items={open.rows}
                onClose={() => setWorkspaceMenu(null)}
                data-testid="workspace-menu"
              />
            )}
          </Show>
          <Show when={this.orientation === 'horizontal'}>
            {/* The strip's actions, as one group. They were two loose siblings of
                the tab list, which the vertical strip then spread down the whole
                column — the list is `flex: 1 1 auto`, so it pushed them apart and
                left the caret alone in the bottom corner. As a group they can be
                placed once, per orientation, by the strip's own CSS. */}
            {/* FIVE ICONS BECAME TWO, and the two that stayed are the ones a
                person reaches for without looking. `+` opens a tab, which is
                the strip's whole purpose; the caret opens everything else.

                What was there before was a rank of five same-sized, same-
                weight glyphs — new tab, new workspace, quick connect, insert
                a secret, snippets — with nothing to say which was ordinary
                and which was occasional. A row like that is not read, it is
                hunted through, and it competes with the tabs beside it for
                the same corner of the eye. Edge's tab strip ends in exactly
                these two controls for the same reason.

                Nothing was removed: the three that left are rows in the menu,
                where they have room for their names — and a name is a better
                affordance than a key glyph for an action performed twice an
                hour. */}
            <div class="tabstrip-actions">
              <IconButton ariaLabel="New tab" onClick={() => this.onNewPane?.()}>
                <PlusIcon />
              </IconButton>
              <IconButton
                ariaLabel="More"
                title="More"
                onClick={(e: MouseEvent) => {
                  const anchor = e.currentTarget
                  if (!(anchor instanceof HTMLElement)) return
                  const rect = anchor.getBoundingClientRect()
                  setStripMenu({ x: rect.right, y: rect.bottom })
                }}
              >
                <ChevronDownIcon />
              </IconButton>
            </div>
            <div class="tabbar-spacer" />
          </Show>
          {/* The strip's trailing edge. Placed absolutely against the strip
              (tab-strip.css) rather than as a flex item, because the strip is
              a COLUMN — the sidebar's handle is the trailing slot of a row and
              this one has no row to be a slot in. Placement only: the control,
              its keyboard and its bounds are the kit's. */}
          <Show when={this.orientation === 'vertical'}>
            <ResizeHandle
              ariaLabel="Resize the tab strip"
              value={stripWidth()}
              min={TABSTRIP_WIDTH_MIN}
              max={TABSTRIP_WIDTH_MAX}
              step={TABSTRIP_WIDTH_STEP}
              onChange={(w) => {
                setStripWidth(w)
                container.style.setProperty('--tabstrip-width', `${w}px`)
              }}
              onCommit={(w) => {
                setStripWidth(w)
                container.style.setProperty('--tabstrip-width', `${w}px`)
              }}
            />
          </Show>
        </>
      )
    }, container)
  }

  addPane(tab: PaneView): void {
    if (!this.mounted) return

    // Wire display-change notification to write changed fields into the store.
    tab.onDisplayChange = () => {
      this._setDisplay('records', tab.id, {
        title: tab.displayTitle ?? tab.title,
        tooltip: tab.tooltip,
        subtitle: tab.subtitle,
        preview: tab.preview,
        adoptable: tab.adoptable,
        warning: tab.warning,
        warningLabel: tab.warningLabel ?? '',
        hasActivity: tab.hasActivity,
        agentStatus: tab.agentStatus,
        colour: tab.colour ?? null,
        pinned: tab.pinned === true,
        groupKey: tab.groupKey ?? '',
        depth: tab.depth ?? 0,
        sandboxed: tab.sandboxed === true,
      })
    }

    this._setPaneViews((prev) => [...prev, tab])

    // Initialize store entry with current display state.
    this._setDisplay('records', tab.id, {
      title: tab.displayTitle ?? tab.title,
      tooltip: tab.tooltip,
      subtitle: tab.subtitle,
      preview: tab.preview,
      adoptable: tab.adoptable,
      warning: tab.warning,
      warningLabel: tab.warningLabel ?? '',
      hasActivity: tab.hasActivity,
      agentStatus: tab.agentStatus,
      colour: tab.colour ?? null,
      pinned: tab.pinned === true,
      groupKey: tab.groupKey ?? '',
      depth: tab.depth ?? 0,
      sandboxed: tab.sandboxed === true,
    })

    // Link pane to button (aria-labelledby)
    const pane = document.getElementById(tab.paneId)
    if (pane) pane.setAttribute('aria-labelledby', `tab-btn-${tab.id}`)
  }

  removePane(paneId: number): void {
    if (!this.mounted) return
    this._setPaneViews((prev) => {
      const removed = prev.find((t) => t.id === paneId)
      if (removed) removed.onDisplayChange = null
      return prev.filter((t) => t.id !== paneId)
    })
    // Delete store entry — functional update avoids referencing current state.
    this._setDisplay('records', (prev: Record<number, PaneDisplayRecord>) => {
      const next = { ...prev }
      delete next[paneId]
      return next
    })
  }

  setActive(paneId: number): void {
    if (!this.mounted) return
    this._setDisplay('activeId', paneId)
    this.revealPane(paneId)
  }

  /**
   * BRING THE TAB YOU ARE ON BACK ONTO THE SCREEN.
   *
   * The row scrolls now (tab-strip.css), and a scrolling row without this is
   * worse than one that clips: open an eighth tab and it becomes the active
   * one somewhere past the right edge, so the thing you just created is the
   * one thing you cannot see. It is the same for `Cmd+1..9`, for the MRU
   * landing after a close, and for a workspace switch.
   *
   * `inline: 'nearest'` scrolls the least that will do — a tab already in
   * view does not move, so activating tabs does not slew the row about under
   * the pointer.
   *
   * The DOM is settled by now: Solid's signal setters run their dependent
   * effects synchronously outside a batch, which is the same fact `reorder`
   * relies on to restore focus.
   */
  private revealPane(paneId: number): void {
    const row = this.container?.querySelector(`[data-pane-id="${paneId}"]`)
    if (row instanceof HTMLElement) row.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    this.updateOverflow()
  }

  /**
   * Say whether the row runs past either edge, so the strip can fade there.
   *
   * MEASURED, NOT GUESSED. There is no CSS that asks "is this box scrolled",
   * and a fade painted unconditionally would lie for the ordinary case of
   * four tabs in a wide window — an edge that says "there is more" when there
   * is not is worse than no edge at all, because it is unfalsifiable by
   * looking.
   *
   * The one-pixel slack absorbs fractional scroll positions, which a trackpad
   * and a fractional device-pixel ratio both produce; without it the end fade
   * flickers on at rest.
   */
  private updateOverflow(): void {
    if (this.orientation !== 'horizontal') return
    const box = this.container?.querySelector('.tabs-container')
    if (!(box instanceof HTMLElement)) return
    const max = box.scrollWidth - box.clientWidth
    box.toggleAttribute('data-overflow-start', box.scrollLeft > 1)
    box.toggleAttribute('data-overflow-end', box.scrollLeft < max - 1)
  }

  setGroupHeadings(headings: readonly StripGroupHeading[]): void {
    this.pendingHeadings = [...headings]
    if (this.mounted) this._setGroupHeadings(this.pendingHeadings)
  }

  setExpandedGroup(key: string | null): void {
    this.pendingExpanded = key
    if (this.mounted) this._setExpandedGroup(key)
  }

  reorder(tabs: readonly PaneView[]): void {
    if (!this.mounted) return
    // Solid's <For> reconciliation clears focus when it moves a node with
    // insertBefore, even though the node itself survives — keyed identity is
    // necessary here and not sufficient (nocx-82l9.8). Signal setters run their
    // dependent effects synchronously outside a batch, so the DOM is settled by
    // the time _setPaneViews returns and restoring focus here is enough.
    const active = document.activeElement
    this._setPaneViews([...tabs])
    if (active instanceof HTMLElement && this.container?.contains(active)) {
      active.focus({ preventScroll: true })
    }
    // The set of rows just changed, so what runs past the edges did too —
    // closing the last tab can end an overflow that no scroll event will
    // report.
    this.updateOverflow()
  }

  // ── Keyboard (roving tabindex) ───────────────────────────────────────

  private readonly onTablistKeydown = (e: KeyboardEvent): void => {
    const keys =
      this.orientation === 'vertical'
        ? ['ArrowUp', 'ArrowDown', 'Home', 'End']
        : ['ArrowLeft', 'ArrowRight', 'Home', 'End']
    if (!keys.includes(e.key)) return

    const button = (e.target as HTMLElement).closest('[role="tab"]')
    if (!button) return

    e.preventDefault()
    e.stopPropagation()

    const paneId = Number(button.getAttribute('data-pane-id'))
    if (Number.isNaN(paneId)) return

    const tabs = this._getPaneViews()
    const idx = tabs.findIndex((t) => t.id === paneId)
    if (idx === -1) return

    const len = tabs.length
    let nextIdx: number
    switch (e.key) {
      case 'ArrowUp':
      case 'ArrowLeft':
        nextIdx = idx > 0 ? idx - 1 : len - 1
        break
      case 'ArrowDown':
      case 'ArrowRight':
        nextIdx = idx < len - 1 ? idx + 1 : 0
        break
      case 'Home':
        nextIdx = 0
        break
      case 'End':
        nextIdx = len - 1
        break
      default:
        return
    }

    const nextPane = tabs[nextIdx]
    if (nextPane) {
      const nextBtn = document.getElementById(`tab-btn-${nextPane.id}`)
      nextBtn?.focus()
    }
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// HorizontalTabStrip
// ═══════════════════════════════════════════════════════════════════════════

export class HorizontalTabStrip extends TabStripBase {
  public readonly orientation: Orientation = 'horizontal'

  protected setupContainer(container: HTMLElement): void {
    container.classList.add('tabbar')
    container.setAttribute('role', 'tablist')
    container.setAttribute('aria-label', 'Terminal tabs')
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// VerticalTabStrip
// ═══════════════════════════════════════════════════════════════════════════

export class VerticalTabStrip extends TabStripBase {
  public readonly orientation: Orientation = 'vertical'

  protected setupContainer(container: HTMLElement): void {
    container.classList.add('tabstrip-vertical')
    container.setAttribute('role', 'tablist')
    container.setAttribute('aria-label', 'Terminal tabs')
  }
}
