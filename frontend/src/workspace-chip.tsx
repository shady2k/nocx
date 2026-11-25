import { Show } from 'solid-js'
import { Button } from './ui/button'
import type { GroupAttention } from './layout/workspace-colour'
import type { WorkspaceColour } from './layout/workspace-colours'
import { t } from './i18n/locale-model'

/**
 * WorkspaceChip — a workspace as a SEGMENT OF THE TAB ROW, not as a mode of
 * the window (workspaces UX rework; amends design §4.3).
 *
 * WHAT THIS REPLACED, AND WHY. §4.3 gave the horizontal strip one chip on the
 * left: the row drew the current workspace's tabs, and every other workspace
 * was reachable only by opening the chip's menu. Two complaints in first use,
 * and they are the same complaint: switching is a dropdown, and the tabs you
 * are not looking at are gone from the screen entirely. §4.3 had rejected
 * inline chips — the Firefox/Chrome/Edge shape — on the grounds that "lineage
 * depth does not fit in one row". That reason does not hold: the horizontal
 * strip never draws lineage at all, because the same section puts the tree in
 * the vertical strip and keeps this row flat. The rejection was answering a
 * problem this row does not have.
 *
 * SO A WORKSPACE IS NOW A RUN OF TABS WITH A PILL IN FRONT OF IT. Every
 * workspace is present in the row at all times. The one holding the current
 * tab shows its tabs; the rest are folded to their pill. Switching stops
 * being a mode change performed through a menu and becomes what it always
 * was on this row — clicking a tab, or clicking the pill that stands for a
 * run of them.
 *
 * THE PILL REPORTS, IT DOES NOT MERELY LABEL (`layout/workspace-colour.ts`).
 * A folded browser tab group is inert; a folded workspace here can hold three
 * agents, one of which is waiting on a human. The attention mark is what makes
 * folding safe, and it is the reason this design can hide anything at all.
 *
 * IT STILL ADVERTISES NAVIGATION AND NOTHING ELSE (§5.5). Membership, not a
 * fence: no shield, no lock, no "isolated". The colour is identity — the one
 * thing you read sideways — and never a status the workspace does not have.
 *
 * It PLACES a kit component and repaints none: the control is the kit's
 * workspace Button, which owns the full-height coloured badge and type. This
 * file positions it and carries the three `data-*` facts (colour, expanded,
 * attention) its contents read.
 */

export interface WorkspaceChipProps {
  /** The workspace's name. Never the default's — the default draws no pill at
   *  all (§4.2), so this component is never built for it. */
  readonly name: string
  /** The colour the USER chose, or null for a workspace nobody coloured. Null
   *  draws the dot in the neutral accent — a pill still has to be visible,
   *  and inventing a colour is what this replaced. */
  readonly colour: WorkspaceColour | null
  /** How many tabs the workspace holds, shown only while it is folded: an
   *  expanded group's tabs are on screen and counting them for the user is
   *  noise. */
  readonly count: number
  readonly attention: GroupAttention
  /** Whether this workspace's tabs are the ones the row is drawing. */
  readonly expanded: boolean
  /** Go to this workspace. One click, and it is the whole of switching now —
   *  there is no menu in the path. */
  onActivate: () => void
  /** The workspace's own actions (rename, close…), which live on the context
   *  menu rather than on a caret. A caret would put two controls a few pixels
   *  apart where one of them is used constantly and the other a few times a
   *  week, and the frequent one would keep hitting the rare one. */
  onMenu: (x: number, y: number) => void
}

export function WorkspaceChip(props: WorkspaceChipProps) {
  return (
    <div
      class="nocx-workspace-chip"
      data-colour={props.colour}
      data-expanded={props.expanded ? 'true' : undefined}
      data-attention={props.attention === 'quiet' ? undefined : props.attention}
    >
      <Button
        variant="workspace"
        // This is still the current workspace in a set of navigation choices;
        // the dedicated Button variant changes only its Edge-style paint.
        selected={props.expanded}
        title={t('workspaceChip.title', 'Workspace: {name}', { name: props.name })}
        onClick={() => props.onActivate()}
        onContextMenu={(e: MouseEvent) => {
          e.preventDefault()
          props.onMenu(e.clientX, e.clientY)
        }}
      >
        {/* NOT A Caption, and that was the first version's worst mistake.
            Caption is the kit's GROUP-CAPTION register — uppercase, letter-
            spaced, dim, deliberately fine print — and it is right over a
            column of rows in a rail. Here it made the workspace the smallest
            and quietest object in a row of 200px tabs, so the thing that
            names a container read as a footnote beside the things it
            contains. The pill is primary navigation and takes the Button's
            own type. */}
        {/* THE MARK LEADS THE NAME. It is the one thing on this pill read
            without looking at it, and a row of pills is scanned down its left
            edge — a dot sitting after a name of unpredictable length lands in
            a different place on every pill, so there is no column to scan.
            In front, every workspace's report is in the same place. */}
        <Show when={props.attention !== 'quiet'}>
          <span class="nocx-workspace-chip__attention" aria-hidden="true" />
        </Show>
        <span class="nocx-workspace-chip__name">{props.name}</span>
        {/* The count is the folded group's only account of itself, so it
            appears exactly when the tabs do not. */}
        <Show when={!props.expanded}>
          <span class="nocx-workspace-chip__count">{props.count}</span>
        </Show>
      </Button>
    </div>
  )
}
