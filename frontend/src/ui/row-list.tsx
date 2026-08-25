/**
 * EditableRowList — a repeating editable row list (nocx-wzc4.3).
 *
 * The kit primitive for "a list of rows the user edits": each row renders
 * the caller's controls (which must be kit fields), a per-row remove
 * control, and an add control at the foot. Repeating editable rows are
 * exactly the control surfaces hand-roll as a bespoke div stack; the kit
 * owns the row rhythm, the separator and the two affordances so every
 * surface's list reads as one vocabulary.
 *
 * Identities: `ui-row-list` (the list), `ui-row-list__row` (one row),
 * `ui-row-list__content` (the caller's fields), `ui-row-list__remove`
 * (placement wrapper for the per-row remove IconButton),
 * `ui-row-list__empty` (the empty message), `ui-row-list__add` (placement
 * wrapper for the foot add Button). The remove and add are kit Buttons —
 * the wrappers only place them, they never repaint them.
 *
 * A group-level error — "at least one row must have a name" — is the same
 * concept as a field's error and renders through the kit's one error
 * identity (`ui-field-error`, painted by field.css), so the surface never
 * re-declares the colour or the size. It sits below the add control, at
 * the foot of the list: that is where a reader looks for a message about
 * the field they just finished, and it never separates the rows from the
 * control that adds one.
 *
 * TWO SHAPES, ONE VOCABULARY. `cards` (the default) is a bordered box per
 * row, which is right for a row of several labelled fields — a port forward,
 * a mount. `table` is a grid with a header, which is right for rows of two or
 * three SHORT values where the labels would otherwise be repeated on every
 * row: query parameters, headers, environment variables. The card shape was
 * being used for all of them, and a list of three parameters came out as
 * three boxes with "Name" and "Value" printed six times.
 *
 * A variant rather than a second component, because everything else is the
 * same: the caller owns the rows, the kit owns the rhythm, the remove
 * affordance and the add control at the foot. In `table` the render callback
 * returns CELLS (`<td>`) instead of fields — the one part of the contract
 * that differs, and it differs because a table row is made of cells.
 *
 * The rows are controlled: the caller owns the data and passes `rows`,
 * `renderRow`, `onRemove` and `onAdd`. Nothing here mutates state.
 *
 * The list keys rows by POSITION (`<Index>`, not `<For>`), and the row the
 * render callback receives is an ACCESSOR rather than a value. That is the
 * identity contract: the callers replace the row object on every edit
 * (their `updateModel` maps to a new object), and a value-keyed list would
 * dispose the row's DOM and build it again on each keystroke — taking
 * focus, IME composition, text selection and any open browser popup with
 * it. Keyed by position, the DOM survives and the accessor hands the
 * bindings the new object; a binding must therefore read `row().field`
 * inside JSX (or a kit field's `value` prop) and must never capture the
 * value into a `const` — a captured value is a snapshot of the first
 * render and will never see an edit.
 */
import { Index, Show, type JSX } from 'solid-js'
import { Button } from './button'
import { IconButton } from './icon-button'
import { CloseIcon, PlusIcon } from './icons'

/** One column of the table variant. A column whose control needs no visible
 *  heading — a tick box, the remove column — still owes assistive technology
 *  a name, so the label is hidden rather than absent. */
interface RowListColumn {
  label: string
  labelHidden?: boolean
}

export interface EditableRowListProps<T> {
  /**
   * Which shape. `cards` (default) is a bordered box per row; `table` is a
   * grid with a header, for rows of short values whose labels would
   * otherwise repeat on every row.
   */
  variant?: 'cards' | 'table'
  /**
   * The table's columns, header order. Required by `table` and ignored by
   * `cards`. The remove column is the kit's own and is NOT listed here —
   * every table has one and it is not the caller's to place.
   */
  columns?: RowListColumn[]
  /** The rows being edited. Read-only: the caller owns the data. */
  rows: readonly T[]
  /**
   * Renders one row's fields. The row is an ACCESSOR, not the value (see
   * the identity contract above): read `row().field` inside a binding, and
   * the binding updates in place while the row's DOM survives edits. Called
   * once per position; keep it cheap.
   */
  renderRow: (row: () => T, index: number) => JSX.Element
  /** Called with the row's index when its remove control is activated. */
  onRemove?: (index: number) => void
  /** Called when the foot add control is activated. */
  onAdd?: () => void
  /** Visible label of the add control, e.g. "Add forward". */
  addLabel?: string
  /** Accessible name for one row's remove control, e.g. "Remove forward 2". */
  removeLabel?: (index: number) => string
  /** Shown above the add control when there are no rows. */
  emptyLabel?: string
  /**
   * A group-level error message, rendered through the kit's `ui-field-error`
   * identity at the foot of the list, below the add control — the position
   * a reader looks for a field's message, and never between the rows and
   * their add affordance. `undefined` renders nothing.
   */
  error?: string
  /** Accessible name for the list itself. */
  ariaLabel: string
  /** Hide editing affordances for a list that only explains inherited rows. */
  readOnly?: boolean
  disabled?: boolean
}

export function EditableRowList<T>(props: EditableRowListProps<T>) {
  const remove = (i: number): JSX.Element => (
    <IconButton
      size="sm"
      title="Remove"
      ariaLabel={props.removeLabel?.(i) ?? `Remove row ${i + 1}`}
      disabled={props.disabled === true}
      onClick={() => props.onRemove?.(i)}
    >
      <CloseIcon />
    </IconButton>
  )

  const foot = (): JSX.Element => (
    <Show when={props.readOnly !== true}>
      <>
        <div class="ui-row-list__add">
          <Button
            variant="ghost"
            disabled={props.disabled === true}
            onClick={() => props.onAdd?.()}
          >
            <PlusIcon />
            {props.addLabel ?? 'Add'}
          </Button>
        </div>
        <Show when={props.error !== undefined}>
          <p class="ui-field-error" role="alert">
            {props.error}
          </p>
        </Show>
      </>
    </Show>
  )

  return (
    <Show
      when={props.variant === 'table'}
      fallback={
        <div class="ui-row-list" role="list" aria-label={props.ariaLabel}>
          <Index each={props.rows}>
            {(row, i) => (
              <div class="ui-row-list__row" role="listitem">
                <div class="ui-row-list__content">{props.renderRow(row, i)}</div>
                <Show when={props.readOnly !== true}>
                  <div class="ui-row-list__remove">{remove(i)}</div>
                </Show>
              </div>
            )}
          </Index>
          <Show when={(props.rows?.length ?? 0) === 0 && props.emptyLabel}>
            <p class="ui-row-list__empty">{props.emptyLabel}</p>
          </Show>
          {foot()}
        </div>
      }
    >
      <div class="ui-row-list" data-variant="table">
        <Show when={(props.rows?.length ?? 0) > 0}>
          <table class="ui-row-list__table" aria-label={props.ariaLabel}>
            <thead>
              <tr>
                <Index each={props.columns ?? []}>
                  {(col) => (
                    <th scope="col">
                      <Show when={col().labelHidden !== true} fallback={srOnly(col().label)}>
                        {col().label}
                      </Show>
                    </th>
                  )}
                </Index>
                <Show when={props.readOnly !== true}>
                  <th scope="col">{srOnly('Remove')}</th>
                </Show>
              </tr>
            </thead>
            <tbody>
              <Index each={props.rows}>
                {(row, i) => (
                  <tr>
                    {props.renderRow(row, i)}
                    <Show when={props.readOnly !== true}>
                      <td class="ui-row-list__remove">{remove(i)}</td>
                    </Show>
                  </tr>
                )}
              </Index>
            </tbody>
          </table>
        </Show>
        <Show when={(props.rows?.length ?? 0) === 0 && props.emptyLabel}>
          <p class="ui-row-list__empty">{props.emptyLabel}</p>
        </Show>
        {foot()}
      </div>
    </Show>
  )
}

/** A heading only assistive technology reads. The class is the kit's, painted
 *  by row-list.css — a surface never re-declares the clip. */
function srOnly(label: string): JSX.Element {
  return <span class="ui-row-list__sr">{label}</span>
}
