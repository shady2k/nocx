// workspaceActionRows — what you can DO to a workspace, in one place
// (nocx-isoph.7; workspaces UX design §4.3).
//
// ONE MECHANISM, PLACED TWICE, and that is the whole reason this is a module
// rather than two lists. A window shows one workspace at a time behind a chip
// (§4.3), and a vertical strip shows every workspace at once under headings
// (§4.2) — two views of ONE set. The actions are the same in both, so they are
// built here and placed by whoever is drawing: the chip's switcher appends
// them to its navigation rows, and a vertical group heading opens them for the
// workspace it heads. Building them twice is how the two would come to differ,
// and they would differ first in the rule below, which is the one that matters.
//
// THE DEFAULT WORKSPACE OFFERS NOTHING, and that is a property here rather
// than a branch in each caller. It has no name to change (§4.2 — its stored
// name is never read, see layout/strip-groups.ts), it is permanent, so there
// is nothing to close, and it is not a member of the arrangement a user made,
// so there is nothing to move. The affordances DO NOT EXIST for it rather than
// existing and refusing: an enabled row that answers "no" is a promise the
// product cannot keep, and a disabled one is the same promise greyed out.
//
// NAVIGATION IS NOT HERE. Switching workspaces is the chip's own reason to
// exist and the vertical strip answers it by showing every group at once, so a
// switch row built here would be an action for one placement wearing the shape
// of a shared one.

import type { Component } from 'solid-js'
import { ArrowDownIcon, ArrowUpIcon, CloseIcon, PencilIcon } from './ui/icons'
import { t } from './i18n/locale-model'

/** One row of a workspace menu, in the kit ContextMenu's shape — including
 *  its mark, which is why this module is a `.tsx`: the rows are built here so
 *  the two placements cannot come to differ, and an icon chosen by each
 *  caller would be exactly such a difference. */
export interface WorkspaceMenuRow {
  readonly id: string
  readonly label: string
  readonly icon?: Component
  readonly onSelect: () => void
}

/** The set a workspace is a member of, as the menu needs to see it.
 *
 *  `ids` is the BACKEND's list in its own order, never the renderer's
 *  display list: a reorder must send a permutation of the rows the store
 *  actually holds (content.ReorderWorkspaces refuses anything else), and the
 *  display list can carry a synthesised default the store has not written
 *  yet. Passing what is drawn instead of what is stored is exactly how that
 *  refusal would be earned. */
export interface WorkspaceSet {
  readonly ids: readonly string[]
  readonly defaultWorkspaceId: string
}

/** What a menu row asks for. Every one of these is an INTENT — the answer
 *  comes back through the store, like every other strip action. */
export interface WorkspaceActions {
  readonly onRename: (workspaceId: string) => void
  /** Move `workspaceId` to `index` within the set, expressed as the whole new
   *  order by the caller of this module's `moveWorkspace`. */
  readonly onReorder: (ids: readonly string[]) => void
  readonly onClose: (workspaceId: string) => void
}

/**
 * The whole new order after moving `workspaceId` by `delta` places.
 *
 * The WHOLE order, never a move of one member, because that is what the wire
 * takes: `workspaces.reorder` writes positions 0..n-1 from the list it is
 * given and refuses anything that is not a permutation of what the store
 * holds. Returning null when the move would leave the set is what keeps the
 * caller from sending an unchanged order and calling it an edit.
 */
export function moveWorkspace(
  ids: readonly string[],
  workspaceId: string,
  delta: number,
): string[] | null {
  const from = ids.indexOf(workspaceId)
  if (from === -1) return null
  const to = from + delta
  if (to < 0 || to >= ids.length) return null
  const next = [...ids]
  next.splice(from, 1)
  next.splice(to, 0, workspaceId)
  return next
}

/**
 * The rows a menu offers for `subject`, or none at all for the default.
 *
 * Move rows appear only where the move is possible: a workspace at the top is
 * offered no "move up". The alternative — always offering both and letting the
 * move fail — puts a control in front of a person that does nothing when they
 * reach for it, which is the same defect as a disabled row with no reason.
 */
export function workspaceActionRows(
  subject: string,
  set: WorkspaceSet,
  actions: WorkspaceActions,
): WorkspaceMenuRow[] {
  if (subject === set.defaultWorkspaceId) return []
  if (!set.ids.includes(subject)) return []

  const rows: WorkspaceMenuRow[] = [
    {
      id: 'workspace-rename',
      label: t('ws.rename', 'Rename workspace…'),
      icon: PencilIcon,
      onSelect: () => actions.onRename(subject),
    },
  ]
  const up = moveWorkspace(set.ids, subject, -1)
  if (up) {
    rows.push({
      id: 'workspace-up',
      label: t('ws.moveUp', 'Move up'),
      icon: ArrowUpIcon,
      onSelect: () => actions.onReorder(up),
    })
  }
  const down = moveWorkspace(set.ids, subject, 1)
  if (down) {
    rows.push({
      id: 'workspace-down',
      label: t('ws.moveDown', 'Move down'),
      icon: ArrowDownIcon,
      onSelect: () => actions.onReorder(down),
    })
  }
  rows.push({
    id: 'workspace-close',
    label: t('ws.close', 'Close workspace'),
    icon: CloseIcon,
    onSelect: () => actions.onClose(subject),
  })
  return rows
}
