import { readFileSync } from 'node:fs'
import { describe, expect, expectTypeOf, it } from 'vitest'
import { Mark, Row, Run, SessionFrame, Style } from './generated/session.frame'

// The frame contract is declared once, in contracts/session.frame.schema.json,
// and the renderer's type is generated from it. This file is that pair's
// executable form: the fixture below is typed with every name the generated
// module exports, so a field the schema requires and the type lacks fails to
// compile, and the expectTypeOf pins fail to compile when the generated type
// declares a field name the schema's property list does not — the exact defect
// the contract pair exists to prevent. The runtime assertions then hold the
// committed schema to the same shape, so neither end can move alone.
//
// Until the session runtime actually sends a frame this test is also the
// generated module's only consumer, which is what keeps the dead-exports
// ratchet green with no baseline entry.

// The schema's own shape, narrowed to the parts this file reads. JSON.parse
// hands back something looser than this, and a narrower local type keeps the
// assertions honest about what they pin. `items` is a list on the two tuple
// defs (mark, run) and a single schema on the array fields — both shapes this
// schema uses.
interface SchemaObject {
  type?: string
  required?: string[]
  enum?: unknown[]
  const?: unknown
  oneOf?: SchemaObject[]
  minimum?: number
  maximum?: number
  minItems?: number
  maxItems?: number
  additionalItems?: boolean
  items?: SchemaObject | SchemaObject[]
  properties?: Record<string, SchemaObject>
  $defs: Record<string, SchemaObject>
}
const schema = JSON.parse(
  readFileSync(new URL('../../contracts/session.frame.schema.json', import.meta.url), 'utf8'),
) as SchemaObject

const requiredOf = (node: SchemaObject): string[] => [...(node.required ?? [])].sort()
const propertiesOf = (node: SchemaObject): string[] => Object.keys(node.properties ?? {}).sort()
const keysOf = (value: object): string[] => Object.keys(value).sort()
// The mark and run ride as fixed three- and two-position tuples; their
// closed-ness is the min/max/additionalItems triple, not a required list.
const tupleItemsOf = (node: SchemaObject): SchemaObject[] => {
  if (!Array.isArray(node.items)) throw new Error('expected a tuple items list')
  return node.items
}
// style is `oneOf [{const: 0}, {type: array, items: [...]}]` — the array
// branch is the one with the 5-slot tuple this file pins.
const styleArrayBranch = (): SchemaObject => {
  const branch = (schema.$defs.style.oneOf ?? []).find((b) => Array.isArray(b.items))
  if (branch === undefined) throw new Error('expected an array branch on $defs/style')
  return branch
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
    expectTypeOf<keyof Row>().toEqualTypeOf<'text' | 'marks' | 'runs' | 'wrap' | 'continuation'>()
    // The mark is the positional tuple [position, codepoints, width] and the
    // run the positional tuple [style, length] — the wire shape brought to
    // its compact, text-carrying form in nocx-zg3k3.2.12. The type pins are
    // the arity-and-type contract a named-field object cannot express.
    expectTypeOf<Mark>().toEqualTypeOf<[number, number, 1 | 2 | 4]>()
    expectTypeOf<Run>().toEqualTypeOf<[Style, number]>()
    // Style is either the bare default (0) or the 5-element tuple; colour is
    // now one packed integer, not a named-field object.
    expectTypeOf<Style>().toEqualTypeOf<
      0 | [number, number, number, number, 0 | 1 | 2 | 3 | 4 | 5]
    >()
  })

  it('a complete frame carries exactly the schema’s declared shape', () => {
    const packedRGB = 257 + ((10 << 16) | (20 << 8) | 30)
    const style: Style = [1 + 3, packedRGB, 0, 1, 0]
    const mark: Mark = [0, 1, 2]
    const run: Run = [style, 2]
    const row: Row = { text: '汉', marks: [mark], runs: [run] }
    const frame: SessionFrame = {
      revision: 7,
      geometry: { cols: 80, rows: 24, cellWidthPx: 9, cellHeightPx: 18, revision: 2 },
      cursor: { x: 0, y: 0, visible: true },
      rows: [row],
    }

    expect(requiredOf(schema)).toEqual(['revision', 'geometry', 'cursor', 'rows'].sort())
    expect(keysOf(frame)).toEqual(requiredOf(schema))
    expect(keysOf(frame.geometry)).toEqual(requiredOf(schema.properties!.geometry))
    expect(keysOf(frame.cursor)).toEqual(requiredOf(schema.properties!.cursor))
    // The row's only REQUIRED field is text — marks, runs, wrap and
    // continuation are each optional, absence carrying a stated default.
    expect(requiredOf(schema.$defs.row)).toEqual(['text'])
    expect(propertiesOf(schema.$defs.row)).toEqual(
      ['text', 'marks', 'runs', 'wrap', 'continuation'].sort(),
    )
    // Tuple arity is the required-shape equivalent for the two positional
    // defs: three slots for the mark, two for the run.
    expect(tupleItemsOf(schema.$defs.mark)).toHaveLength(3)
    expect(schema.$defs.mark.minItems).toBe(3)
    expect(schema.$defs.mark.maxItems).toBe(3)
    expect(schema.$defs.mark.additionalItems).toBe(false)
    expect(tupleItemsOf(schema.$defs.run)).toHaveLength(2)
    expect(schema.$defs.run.minItems).toBe(2)
    expect(schema.$defs.run.maxItems).toBe(2)
    expect(schema.$defs.run.additionalItems).toBe(false)
    // Style: the default-shortcut branch is the literal 0, the other is the
    // 5-slot tuple.
    const branches = schema.$defs.style.oneOf ?? []
    expect(branches).toHaveLength(2)
    expect(branches.some((b) => b.const === 0)).toBe(true)
    const arrayBranch = styleArrayBranch()
    expect(tupleItemsOf(arrayBranch)).toHaveLength(5)
    expect(arrayBranch.minItems).toBe(5)
    expect(arrayBranch.maxItems).toBe(5)
    expect(arrayBranch.additionalItems).toBe(false)
    // Colour is a plain integer now, not a named-field object.
    expect(schema.$defs.color.type).toBe('integer')
    expect(schema.$defs.color.properties).toBeUndefined()
  })

  it('the closed sets are the Go enumerations, read through the packed integers', () => {
    const markItems = tupleItemsOf(schema.$defs.mark)
    // emulator.Width, minus unknown (0, never sent) and spacerTail (3, not a
    // position in this vocabulary): narrow is the unmarked default, wide and
    // spacerHead are what a mark can name.
    expect(markItems[2].enum).toEqual([1, 2, 4])
    const arrayBranch = styleArrayBranch()
    const styleItems = tupleItemsOf(arrayBranch)
    // emulator.Underline: none, single, double, curly, dotted, dashed —
    // slot 4 of the style tuple.
    expect(styleItems[4].enum).toEqual([0, 1, 2, 3, 4, 5])
    // emulator.Attributes: eight decorations, one bit each, and no ninth —
    // slot 3.
    expect(styleItems[3].minimum).toBe(0)
    expect(styleItems[3].maximum).toBe(255)
    // A run covers at least one column.
    expect(tupleItemsOf(schema.$defs.run)[1].minimum).toBe(1)
  })
})
