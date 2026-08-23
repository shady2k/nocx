// The operations view — the sidebar's answer to "what is nocx doing for me,
// and can I stop it?".
//
// ## Where it is, and why the icon and the list are in different places
//
// The complaint that bought the feature was "I cannot see that something is
// running": an upload survives a WebSocket drop and runs on its own SSH
// lease, and while the Files panel owned the only list of transfers a 2 GB
// upload became invisible and uncancellable the moment somebody switched
// sidebar view or pressed Cmd+B.
//
// That complaint has two halves and they belong in two places (nocx-hbdw4.1):
//
// - SEEING THAT SOMETHING RUNS is the ICON's, and the icon is in the activity
//   bar, which stays on screen whatever the panel is doing. It carries the
//   badge (active count) and the aggregate progress bar through
//   `SidebarViewDescriptor.status`, so both survive collapsing the panel and
//   switching to another view.
// - SEEING WHAT RUNS is a place somebody goes to look on purpose, and in this
//   shell such places are VIEWS that open the panel — Files, Git, Ports. So
//   the list is an ordinary view beside them.
//
// The first shape was a popover hung off a bottom-zone indicator, on the
// argument that a top-zone view is mutually exclusive and vanishes with the
// panel. That conflated the two halves: it is true of the list and false of
// the icon, and it is the icon that answers the complaint. The popover, the
// anchored-overlay machinery it needed and the second kind of bottom-zone
// entry all went with it, and the panel's width is also what stopped the row
// ellipsising its own status badge down to "D…".
//
// ## What it deliberately does not do
//
// It does not announce a failure. That is already `showToast`'s, from
// files/upload-surface.ts, and a second attention mechanism for one event is
// two owners of it. Nor does it persist anything: finished entries live in
// session memory, bounded by whoever remembers them, and history across
// restarts is a separate conversation the notification design defers on
// purpose.

import { For, Show, createSignal, onCleanup } from 'solid-js'
import type { SidebarViewDescriptor, SidebarViewStatus } from '../sidebar'
import { EmptyState } from '../ui/empty-state'
import { ArrowDownUpIcon } from '../ui/icons'
import { OperationRow } from '../ui/operation-row'
import { isTerminalPhase, isWaitingPhase } from '../ui/operation'
import type { Operation, OperationsModel } from './operations'
import { createThrottledOperations, type RenderThrottleDeps } from './render-throttle'

/** The view's registry id, and the sidebar's persisted `activeViewId` for it. */
export const OPERATIONS_VIEW_ID = 'operations'

/** Last in the bar: Files (-1), Ports (0), Git and Notes (1). A view a person
 *  visits when something is happening does not belong above the ones they
 *  work in. */
const OPERATIONS_VIEW_ORDER = 2

/** The panel header, and the accessible name of the list inside it. One
 *  string, because they are one thing to a reader. */
const TITLE = 'Operations'

/**
 * How often "2 min ago" is recomputed.
 *
 * A relative label ages whether or not any data moves, and when nothing is
 * running nothing else repaints — so without this the last finished row
 * would read "just now" for the rest of the session, which is the soft
 * degrade AGENTS.md forbids wearing a friendly word. Thirty seconds is half
 * the smallest step the label can take (`just now` → `1 min ago`), so the
 * worst staleness a reader can see is under one unit.
 *
 * It is NOT the render throttle and must not be merged with it: that one
 * holds updates back when data moves too fast, this one produces updates
 * when no data moves at all. Same mechanism, opposite jobs.
 */
const RELATIVE_TIME_TICK_MS = 30_000

/** The clocks and the interval, injected so a test can move them by hand.
 *  A test that waited a real quarter-second to watch a throttle would be a
 *  test that depends on timing — broken on a fast machine too. */
export interface OperationsViewDeps extends RenderThrottleDeps {
  /** The clock the relative "when" is read against; defaults to the wall
   *  clock. Shared with the throttle's `now` when both are given. */
  tickMs?: number
}

export function createOperationsView(
  model: OperationsModel,
  deps: OperationsViewDeps = {},
): SidebarViewDescriptor {
  return {
    id: OPERATIONS_VIEW_ID,
    title: TITLE,
    icon: ArrowDownUpIcon,
    order: OPERATIONS_VIEW_ORDER,
    // Read by the ACTIVITY BAR, not by the panel: this is the half that has
    // to keep working while the view is not mounted at all.
    status: (): SidebarViewStatus => {
      const p = model.progress()
      return { count: model.activeCount(), progress: p === null ? null : p.fraction }
    },
    // Called rather than mounted as JSX, and that is deliberate: `deps` is
    // configuration read once (two clocks and an interval), not reactive
    // state, and passing it as a prop would make every read of it a
    // reactive read outside a tracked scope — which the linter is right
    // about and which would be a lie about what the value does.
    view: () => OperationsPanel(model, deps),
  }
}

function OperationsPanel(model: OperationsModel, deps: OperationsViewDeps) {
  /** The list AS DRAWN, a few times a second rather than on every frame the
   *  data moves — see render-throttle.ts for the two problems that needed
   *  and for why a phase change bypasses it. Still read once per pass and
   *  named, so the two reads below (is it empty, and what is in it) cannot
   *  see two different lists. */
  const operations = createThrottledOperations(() => model.operations(), deps)

  /** The clock the finished rows' "when" is read against. The panel owns it
   *  because the panel is what has to repaint when it moves, and one clock
   *  serves every row — a timer per row would be N timers for one tick. */
  /* GROUPED BY STATE, and the heading is what says the state. Every finished
     row used to carry a "Done" pill, which repeated down the list what one
     heading can say once, in the space the file name wanted — and a badge on
     every row is also a badge nobody reads. The row still marks an outcome
     that is NEWS (failed, cancelled, skipped); `written` is the expected end
     and the heading covers it (owner, 2026-08-22).

     THREE GROUPS AND NOT TWO, since nocx-hbdw4.6. Waiting work is not
     terminal, so it would land in "In progress" for free — and that heading
     would then be false about most of what is under it. Drop five files and
     it reads "In progress 5" while one file is moving and four have not
     started: the count is the very number a person is looking for, and it
     would lie at exactly the moment the queue exists to be seen. Its own
     heading answers both halves at a glance — one moving, four coming.

     Order is deliberate and chronological: what is moving, then what is
     coming, then what is done. Running leads because it is the only part
     that changes while you look at it. */
  const runningOps = () =>
    operations().filter((o) => !isWaitingPhase(o.phase) && !isTerminalPhase(o.phase))
  const queuedOps = () => operations().filter((o) => isWaitingPhase(o.phase))
  const finishedOps = () => operations().filter((o) => isTerminalPhase(o.phase))

  const clock = deps.now ?? ((): number => Date.now())
  const [now, setNow] = createSignal(clock())
  const tick = setInterval(() => setNow(clock()), deps.tickMs ?? RELATIVE_TIME_TICK_MS)
  onCleanup(() => clearInterval(tick))

  /** One group's rows. A function rather than a component so the two groups
   *  cannot drift apart, and so the keying below is written once.
   *
   *  KEYED BY THE OPERATION'S ID, and that is not a tidiness preference — it
   *  is the fix for nocx-hbdw4.1's cancel defect. `For` matches items by
   *  REFERENCE, and every source of operations is a projection that mints
   *  fresh objects on every read (see files/upload-operations.ts), so
   *  `each={ops()}` disposed and rebuilt every row on every store change —
   *  which is every progress frame, several times a second, for as long as a
   *  transfer runs. A person pressing Cancel then had the button replaced
   *  under their finger between mousedown and mouseup, and the browser fires
   *  `click` on the nearest common ancestor of the two: the list, never the
   *  button. The press was lost, and cancel — the single affordance this
   *  surface exists to offer — did nothing.
   *
   *  Keying on the id is what makes a row OUTLIVE the projection: the ids are
   *  strings, so `For` matches them by value, the DOM node stays put across a
   *  store change and only its props update. */
  const renderRows = (ops: () => Operation[]) => (
    <For each={ops().map((o) => o.id)}>
      {(id) => (
        <Show when={ops().find((o) => o.id === id)}>
          {(op) => (
            <OperationRow
              kind={op().kind}
              title={op().title}
              destination={op().destination}
              machine={op().machine}
              phase={op().phase}
              done={op().done}
              total={op().total}
              speedBytesPerSecond={op().speedBytesPerSecond}
              error={op().error}
              startedAt={op().startedAt}
              endedAt={op().endedAt}
              now={now()}
              // Whether there is a cancel at all is the operation's own
              // judgement, carried on the item. This surface never switches
              // on kind or phase to work it out.
              onCancel={op().cancel ?? undefined}
            />
          )}
        </Show>
      )}
    </For>
  )

  return (
    <div class="ops-panel" data-testid="operations-panel">
      <Show
        when={operations().length > 0}
        fallback={
          <EmptyState
            icon={<ArrowDownUpIcon />}
            title="Nothing is running"
            description="Uploads and other background work appear here while they run, and stay for a while after they finish."
          />
        }
      >
        {/* THREE LISTS, ONE PER STATE, each with its own role="list" — the
            kit's row carries role="listitem" and a listitem must be owned by
            a list, so a single list spanning both headings would put a
            heading inside a list where no listitem explains it. An empty
            group draws nothing at all rather than an empty heading. */}
        <Show when={runningOps().length > 0}>
          <h3 class="ops-group__heading">
            In progress
            <span class="ops-group__count">{runningOps().length}</span>
          </h3>
          <div class="ops-list" role="list" aria-label="In progress" data-testid="ops-list">
            {renderRows(runningOps)}
          </div>
        </Show>
        <Show when={queuedOps().length > 0}>
          <h3 class="ops-group__heading">
            Queued
            <span class="ops-group__count">{queuedOps().length}</span>
          </h3>
          <div class="ops-list" role="list" aria-label="Queued" data-testid="ops-list-queued">
            {renderRows(queuedOps)}
          </div>
        </Show>
        <Show when={finishedOps().length > 0}>
          <h3 class="ops-group__heading">
            Finished
            <span class="ops-group__count">{finishedOps().length}</span>
          </h3>
          <div class="ops-list" role="list" aria-label="Finished" data-testid="ops-list-finished">
            {renderRows(finishedOps)}
          </div>
        </Show>
      </Show>
    </div>
  )
}
