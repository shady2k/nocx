import { Show } from 'solid-js'

/**
 * SubagentRow — one CHILD AGENT's line in the tab strip (nocx-o1v0h).
 *
 * A feature component beside Tab rather than a variant of it, and beside the
 * kit's TreeRow rather than a use of it. Both were tried on paper first, and
 * the reasons are worth keeping because the next person will reach for them
 * in the same order:
 *
 * TAB is the strip's PANE row. Everything it carries — a numeric pane id, a
 * close, an adopt, a drag payload, a reorder target, a context menu, an
 * activity indicator, `aria-controls` on the panel it owns — is a property of
 * standing for a pane. A child stands for none of that: it has no pipe, no
 * pane, no id anywhere in the product, and nothing about it can be closed,
 * dragged or reordered. Adding a variant would have meant a branch at ten
 * sites inside one component, which is a component being two things.
 *
 * TREEROW is the kit's tree line and it is the right SHAPE, but its vocabulary
 * is the file tree's: `kind` is `regular | dir | symlink | other | unreadable`
 * and a type glyph is decided from it for every row. Drawing a child agent as
 * a regular file to borrow the indentation would put a wrong word and a wrong
 * icon on the screen — and the row is deliberately iconless, because its name
 * is a child's name and an agent mark beside it would read as a second agent.
 *
 * So: one module, one CSS file, one identity class, and the indentation
 * technique borrowed rather than the component — `data-depth` drives the
 * indent, never nested DOM, exactly as TreeRow and Tab both do, so a row is
 * one row at any depth.
 *
 * WHAT IT DOES NOT DO is most of what it is. No dismiss: a child is not
 * something a person closes, and a control that cannot deliver is worse than
 * none. No status dot: the pane's own row already carries the one indicator,
 * and a second would invite the reading this whole feature refuses — that a
 * child's state says something about the parent's. No disclosure: depth is
 * one, because the screen names one.
 */
export interface SubagentRowProps {
  /** The child's name, as its parent's screen named it. */
  name: string
  /** What it was given to do, or '' when the screen has not said yet.
   *  Absent is drawn as nothing, never as an empty line. */
  task: string
  /** How far in the row is drawn — the parent's depth plus one. Indentation
   *  is driven by the number and never by nested DOM. */
  depth: number
  /** The PARENT's pane, for `aria-controls`: activating this row activates
   *  that pane, because a child has no pane of its own. */
  parentPaneId: string
  /** Hidden by CSS when the parent is filtered out or folded away — a child
   *  drawn under a hidden parent is a row with no parent. */
  hidden?: boolean
  /** Activate the PARENT's pane. There is nowhere else to go. */
  onActivate: () => void
}

/**
 * A DIV WITH A ROLE, exactly as Tab is, and out of the keyboard order on
 * purpose. The action this row offers — go to the pane its parent runs in —
 * is already on the parent's own row, which IS in the strip's roving order and
 * one step above it. Putting a second control for one destination into the
 * sequence lengthens the walk through a rail of tabs and arrives nowhere new.
 */
export function SubagentRow(props: SubagentRowProps) {
  return (
    <div
      class="nocx-subagent"
      role="button"
      aria-controls={props.parentPaneId}
      data-depth={String(props.depth)}
      data-hidden={props.hidden === true ? 'true' : undefined}
      title={props.task === '' ? props.name : `${props.name} — ${props.task}`}
      tabIndex={-1}
      onClick={() => props.onActivate()}
    >
      <span class="nocx-subagent__label">{props.name}</span>
      {/* WHAT IT IS DOING, on the same line rather than under it. The strip's
          pane rows spend their second line on the pane's own preview; a child
          row has one fact and one line, and pushing the task to a line of its
          own would make a child taller than the pane it belongs to. */}
      <Show when={props.task !== ''}>
        <span class="nocx-subagent__task">{props.task}</span>
      </Show>
    </div>
  )
}
