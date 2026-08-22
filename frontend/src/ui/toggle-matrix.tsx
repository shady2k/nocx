/**
 * ToggleMatrix — the kit's grid of controls, where a row meets a column
 * (nocx-3mniv).
 *
 * The shape a flat list cannot express: N things × M places, where the
 * question a reader has is "which of these reaches which of those". Ten
 * sentences in a list answer it one pair at a time and never show the shape;
 * a grid shows it at a glance and makes an empty row or an empty column
 * visible as such.
 *
 * It is a real `<table>`, not a `div` grid, and that is the whole
 * accessibility argument: `scope="col"` and `scope="row"` are what associate
 * a control with the two headers that name its position, so a screen reader
 * announcing one cell says where it is. Without them the surface is a grid of
 * identically-named switches — which is why the caller must ALSO give each
 * control an accessible name that stands on its own; the headers locate a
 * control, they do not name it.
 *
 * The matrix owns the geometry and nothing else. The caller renders each
 * cell's control — a kit component, placed here and never repainted — the way
 * `EditableRowList` takes its rows' fields.
 *
 * Identities: `ui-toggle-matrix` (the table), `ui-toggle-matrix__corner` (the
 * empty top-left cell), `ui-toggle-matrix__column` (a column header),
 * `ui-toggle-matrix__row` (a row), `ui-toggle-matrix__row-header` (a row
 * header), `ui-toggle-matrix__cell` (one cell).
 *
 * Two absences are deliberate and different:
 *
 *   - `renderCell` returning `null` means the caller offers NO control for
 *     that pair. The cell is drawn and left empty rather than filled with an
 *     apology — an impossible choice is absent, not offered and declined.
 *   - `rowHidden` / `columnHidden` mean the caller has FILTERED an axis out of
 *     view. The row or column is hidden with CSS and its cells stay in the
 *     DOM, so a control inside it keeps its identity, its focus and its
 *     addressable id across a filter change. Removing it would make a search
 *     box destroy the thing it is meant to be finding.
 *
 * Visibility is a PREDICATE rather than a field on the axis for that same
 * reason. `For` keys by reference, so an axis list rebuilt to carry a new
 * `hidden` flag would dispose every cell in the grid and build it again — the
 * cost the hiding was there to avoid. The axes are the caller's stable
 * description of the grid; what is currently filtered is asked, per render.
 */
import { For, type JSX } from 'solid-js'

/** One entry on either axis: the id the caller keys cells by, and its label. */
export interface ToggleMatrixAxis {
  id: string
  label: string
}

export interface ToggleMatrixProps {
  /** Accessible name for the table itself. */
  ariaLabel: string
  rows: readonly ToggleMatrixAxis[]
  columns: readonly ToggleMatrixAxis[]
  /**
   * Renders the control where a row meets a column, or `null` when there is
   * no such pair. The control must carry an accessible name that reads on its
   * own — the headers say where it is, not what it does.
   */
  renderCell: (row: ToggleMatrixAxis, column: ToggleMatrixAxis) => JSX.Element | null
  /** Whether this row is filtered out of view. Read per render. */
  rowHidden?: (row: ToggleMatrixAxis) => boolean
  /** Whether this column is filtered out of view. Read per render. */
  columnHidden?: (column: ToggleMatrixAxis) => boolean
}

export function ToggleMatrix(props: ToggleMatrixProps) {
  const rowHidden = (row: ToggleMatrixAxis) =>
    props.rowHidden?.(row) === true ? 'true' : undefined
  const columnHidden = (column: ToggleMatrixAxis) =>
    props.columnHidden?.(column) === true ? 'true' : undefined

  return (
    <table class="ui-toggle-matrix" aria-label={props.ariaLabel}>
      <thead>
        <tr>
          {/* The top-left cell names neither axis, so it is a `td` rather than
              an empty `th` a screen reader would announce as a header. */}
          <td class="ui-toggle-matrix__corner" />
          <For each={props.columns}>
            {(column) => (
              <th
                class="ui-toggle-matrix__column"
                scope="col"
                data-column={column.id}
                data-hidden={columnHidden(column)}
              >
                {column.label}
              </th>
            )}
          </For>
        </tr>
      </thead>
      <tbody>
        <For each={props.rows}>
          {(row) => (
            <tr class="ui-toggle-matrix__row" data-row={row.id} data-hidden={rowHidden(row)}>
              <th class="ui-toggle-matrix__row-header" scope="row">
                {row.label}
              </th>
              <For each={props.columns}>
                {(column) => (
                  <td
                    class="ui-toggle-matrix__cell"
                    data-row={row.id}
                    data-column={column.id}
                    data-hidden={columnHidden(column)}
                  >
                    {props.renderCell(row, column)}
                  </td>
                )}
              </For>
            </tr>
          )}
        </For>
      </tbody>
    </table>
  )
}
