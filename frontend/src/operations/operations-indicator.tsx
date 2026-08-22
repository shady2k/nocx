// The operations indicator — the activity bar's answer to "is anything
// happening, and can I stop it?".
//
// ## Where it is, and why there
//
// The activity bar's BOTTOM zone, beside the Settings gear. That zone is
// the only part of the sidebar that stays on screen whatever the panel is
// doing, which is the whole requirement: an upload survives a WebSocket
// drop and runs on its own SSH lease, and while the Files panel owned the
// only list of transfers a 2 GB upload became invisible and uncancellable
// the moment somebody switched sidebar view or pressed Cmd+B. A view in the
// top zone was considered and rejected — views are mutually exclusive and
// vanish with the panel, so one would not have answered that at all.
//
// The zone's contract was widened for it rather than special-cased: an
// INDICATOR is the second kind of entry there, declared in sidebar.tsx
// beside the action it is not.
//
// ## The icon is always there; the badge and the bar are not
//
// The icon is NOT conditional on anything running. A fixed position is one
// a person learns, nothing jumps in and out of the bar, and there is always
// somewhere to click to ask the question.
//
// The badge counts ACTIVE OPERATIONS and is absent at zero. It does not
// count unread anything: this is not a notification centre, and merging the
// two was considered and rejected — an operation is state with progress and
// a cancel, a notification is a past-tense event. So a finished operation
// leaves the badge AT ONCE and stays in the list, where somebody who goes
// to look can see it really landed.
//
// The progress bar carries the aggregate and appears only while something
// runs. Determinate, never a spinner: a 20-minute upload must not put
// permanent motion in somebody's peripheral vision.
//
// ## What it deliberately does not do
//
// It does not announce a failure. That is already `showToast`'s, from
// files/upload-surface.ts, and a second attention mechanism for one event
// is two owners of it. Nor does it persist anything: finished entries live
// in session memory, bounded by whoever remembers them, and history across
// restarts is a separate conversation the notification design defers on
// purpose.

import { For, Show, createSignal } from 'solid-js'
import type { SidebarIndicator } from '../sidebar'
import { Badge } from '../ui/badge'
import { EmptyState } from '../ui/empty-state'
import { IconButton } from '../ui/icon-button'
import { ArrowDownUpIcon } from '../ui/icons'
import { OperationRow } from '../ui/operation-row'
import { Popover } from '../ui/popover'
import { ProgressBar } from '../ui/progress-bar'
import type { OperationsModel } from './operations'

/** The zone entry's id, and the toolbar's roving key for it. Not exported:
 *  the composition root registers the indicator whole and never needs to
 *  name it, and an id a caller can name is one a caller can disagree with. */
const OPERATIONS_INDICATOR_ID = 'operations'

/** The panel's accessible name, used for the popover's region and for the
 *  list inside it. One string, because they are one thing to a reader. */
const TITLE = 'Background operations'

/** How far the panel hangs off the bar. The popover clamps itself onto the
 *  screen from here (ui/overlay/anchored.ts), which is also what opens it
 *  upward from a button this close to the bottom of the window. */
const ANCHOR_GAP_PX = 4

export function createOperationsIndicator(model: OperationsModel): SidebarIndicator {
  return {
    id: OPERATIONS_INDICATOR_ID,
    render: (props) => {
      /** The popover's anchor, measured at click time and null while it is
       *  closed. The button does not move while the panel is up, so the
       *  measurement is taken once per open rather than tracked. */
      const [anchor, setAnchor] = createSignal<{ x: number; y: number } | null>(null)

      const count = () => model.activeCount()
      const progress = () => model.progress()

      /** The accessible name carries the count, because a person who
       *  reaches this button with the keyboard cannot see the badge. */
      const label = () => (count() === 0 ? TITLE : `${TITLE} — ${count()} running`)

      const toggle = (e: MouseEvent): void => {
        if (anchor() !== null) {
          setAnchor(null)
          return
        }
        const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
        setAnchor({ x: r.right + ANCHOR_GAP_PX, y: r.bottom })
      }

      return (
        <div class="ops-indicator">
          <IconButton
            size="lg"
            data-indicator={OPERATIONS_INDICATOR_ID}
            data-testid="ops-indicator"
            title={label()}
            ariaLabel={label()}
            selected={anchor() !== null}
            tabIndex={props.tabIndex}
            onClick={toggle}
          >
            <ArrowDownUpIcon />
          </IconButton>
          {/* The badge is the surface's own element placed over the kit's
              Badge — the count, its wording and its tone are the kit's. */}
          <Show when={count() > 0}>
            <span class="ops-indicator__badge" data-testid="ops-badge">
              <Badge tone="info">{String(count())}</Badge>
            </span>
          </Show>
          {/* `keyed` so the fraction arrives as a value rather than being
              re-read with a fallback: the model owns what "no progress"
              means, and it means the bar is not here at all. */}
          <Show when={progress()} keyed>
            {(p) => (
              <span class="ops-indicator__progress" data-testid="ops-progress">
                <ProgressBar value={p.fraction} ariaLabel={`${TITLE} progress`} />
              </span>
            )}
          </Show>
          <Popover
            open={anchor() !== null}
            x={anchor()?.x ?? 0}
            y={anchor()?.y ?? 0}
            ariaLabel={TITLE}
            data-testid="ops-popover"
            onClose={() => setAnchor(null)}
          >
            <Show
              when={model.operations().length > 0}
              fallback={
                <EmptyState
                  icon={<ArrowDownUpIcon />}
                  title="Nothing is running"
                  description="Uploads and other background work appear here while they run, and stay for a while after they finish."
                />
              }
            >
              {/* role="list" is the surface's, because the kit's row
                  carries role="listitem" and a listitem must be owned by a
                  list — see ui/collection-view.tsx. */}
              <div class="ops-list" role="list" aria-label={TITLE} data-testid="ops-list">
                <For each={model.operations()}>
                  {(op) => (
                    <OperationRow
                      kind={op.kind}
                      title={op.title}
                      destination={op.destination}
                      phase={op.phase}
                      done={op.done}
                      total={op.total}
                      speedBytesPerSecond={op.speedBytesPerSecond}
                      error={op.error}
                      // Whether there is a cancel at all is the operation's
                      // own judgement, carried on the item. This surface
                      // never switches on kind or phase to work it out.
                      onCancel={op.cancel ?? undefined}
                    />
                  )}
                </For>
              </div>
            </Show>
          </Popover>
        </div>
      )
    },
  }
}
