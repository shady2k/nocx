import { readFileSync } from 'node:fs'
import { describe, expect, expectTypeOf, it } from 'vitest'
import { Cell, Color, Row, Run, SessionFrame, Style } from './generated/session.frame'

// The frame contract is declared once, in contracts/session.frame.schema.json,
// and the renderer's type is generated from it. This file is that pair's
// executable form: the fixture below is typed with every name the generated
// module exports, so a field the schema requires and the type lacks fails to
// compile, and the expectTypeOf pins fail to compile when the generated type
// declares a field name the schema's required list does not — the exact defect
// the contract pair exists to prevent. The runtime assertions then hold the
// committed schema to the same shape, so neither end can move alone.
//
// Until the session runtime actually sends a frame this test is also the
// generated module's only consumer, which is what keeps the dead-exports
// ratchet green with no baseline entry.

// The schema's own shape, narrowed to the parts this file reads. JSON.parse
// hands back something looser than this, and a narrower local type keeps the
// assertions honest about what they pin. `items` is a list on the two tuple
// defs (cell, run) and a single schema on the array fields — both shapes this
// schema uses.
interface SchemaObject {
  required?: string[]
  enum?: unknown[]
  minimum?: number
  maximum?: number
  minItems?: number
  maxItems?: number
  additionalItems?: boolean
  items?: SchemaObject | SchemaObject[]
  // Present on every object of THIS schema (additionalProperties: false and
  // an explicit required throughout), so they are declared required and the
  // assertions below need no non-null escapes. A schema that loses one fails
  // here loudly rather than passing vacuously.
  properties: Record<string, SchemaObject>
  $defs: Record<string, SchemaObject>
}
const schema = JSON.parse(
  readFileSync(new URL('../../contracts/session.frame.schema.json', import.meta.url), 'utf8'),
) as SchemaObject

const requiredOf = (node: SchemaObject): string[] => [...(node.required ?? [])].sort()
const keysOf = (value: object): string[] => Object.keys(value).sort()
// The cell and run ride as fixed three- and two-position tuples; their
// closed-ness is the min/max/additionalItems triple, not a required list.
const tupleItemsOf = (node: SchemaObject): SchemaObject[] => {
  if (!Array.isArray(node.items)) throw new Error('expected a tuple items list')
  return node.items
}

describe('session.frame contract parity', () => {
  // Field names pinned against the required lists by hand from the Go types —
  // sessionruntime.Snapshot/GeometryCommit and emulator.Row/Cell/Style/Color/
  // Cursor — not copied out of the schema. If the generator's naming and this
  // spelling ever disagree, tsc fails here first.
  it('the generated type names exactly the fields the schema requires', () => {
    expectTypeOf<keyof SessionFrame>().toEqualTypeOf<'revision' | 'geometry' | 'cursor' | 'rows'>()
    expectTypeOf<keyof SessionFrame['geometry']>().toEqualTypeOf<
      'cols' | 'rows' | 'cellWidthPx' | 'cellHeightPx' | 'revision'
    >()
    expectTypeOf<keyof SessionFrame['cursor']>().toEqualTypeOf<'x' | 'y' | 'visible'>()
    expectTypeOf<keyof Row>().toEqualTypeOf<'cells' | 'runs' | 'wrap' | 'continuation'>()
    // The cell is the positional tuple [grapheme, width, hasText] and the
    // run the positional tuple [style, length] — the wire shape chosen in
    // nocx-zg3k3.2.6 so a style is shared across adjacent cells instead of
    // repeated per cell. The type pins are the arity-and-type contract a
    // named-field object cannot express.
    expectTypeOf<Cell>().toEqualTypeOf<[string, 0 | 1 | 2 | 3 | 4, boolean]>()
    expectTypeOf<Run>().toEqualTypeOf<[Style, number]>()
    expectTypeOf<keyof Style>().toEqualTypeOf<
      'foreground' | 'background' | 'underlineColor' | 'attributes' | 'underline'
    >()
    expectTypeOf<keyof Color>().toEqualTypeOf<'kind' | 'palette' | 'rgb'>()
    expectTypeOf<keyof Color['rgb']>().toEqualTypeOf<'r' | 'g' | 'b'>()
  })

  it('a complete frame carries exactly the schema’s required shape', () => {
    const rgb = { r: 0, g: 0, b: 0 }
    const color: Color = { kind: 1, palette: 3, rgb }
    const style: Style = {
      foreground: color,
      background: color,
      underlineColor: color,
      attributes: 1,
      underline: 0,
    }
    const cell: Cell = ['é', 1, true]
    const run: Run = [style, 1]
    const row: Row = { cells: [cell], runs: [run], wrap: false, continuation: false }
    const frame: SessionFrame = {
      revision: 7,
      geometry: { cols: 80, rows: 24, cellWidthPx: 9, cellHeightPx: 18, revision: 2 },
      cursor: { x: 0, y: 0, visible: true },
      rows: [row],
    }

    expect(keysOf(frame)).toEqual(requiredOf(schema))
    expect(keysOf(frame.geometry)).toEqual(requiredOf(schema.properties.geometry))
    expect(keysOf(frame.cursor)).toEqual(requiredOf(schema.properties.cursor))
    expect(keysOf(row)).toEqual(requiredOf(schema.$defs.row))
    // Tuple arity is the required-shape equivalent for the two positional
    // defs: three slots for the cell, two for the run.
    expect(tupleItemsOf(schema.$defs.cell)).toHaveLength(3)
    expect(schema.$defs.cell.minItems).toBe(3)
    expect(schema.$defs.cell.maxItems).toBe(3)
    expect(schema.$defs.cell.additionalItems).toBe(false)
    expect(tupleItemsOf(schema.$defs.run)).toHaveLength(2)
    expect(schema.$defs.run.minItems).toBe(2)
    expect(schema.$defs.run.maxItems).toBe(2)
    expect(schema.$defs.run.additionalItems).toBe(false)
    expect(keysOf(style)).toEqual(requiredOf(schema.$defs.style))
    expect(keysOf(color)).toEqual(requiredOf(schema.$defs.color))
    expect(keysOf(color.rgb)).toEqual(requiredOf(schema.$defs.color.properties.rgb))
  })

  it('the closed sets are the Go enumerations', () => {
    const cellItems = tupleItemsOf(schema.$defs.cell)
    // emulator.Width: unknown, narrow, wide, spacerTail, spacerHead.
    expect(cellItems[1].enum).toEqual([0, 1, 2, 3, 4])
    // emulator.Underline: none, single, double, curly, dotted, dashed.
    expect(schema.$defs.style.properties.underline.enum).toEqual([0, 1, 2, 3, 4, 5])
    // emulator.ColorKind: default, palette, rgb.
    expect(schema.$defs.color.properties.kind.enum).toEqual([0, 1, 2])
    // emulator.Attributes: eight decorations, one bit each, and no ninth.
    expect(schema.$defs.style.properties.attributes.minimum).toBe(0)
    expect(schema.$defs.style.properties.attributes.maximum).toBe(255)
    // A run covers at least one cell.
    expect(tupleItemsOf(schema.$defs.run)[1].minimum).toBe(1)
  })
})
