import { describe, expect, it } from 'vitest'
import { scanSource } from './check-menu-icons.mjs'

/**
 * The menu-icons checker's own tests (nocx-inbw1).
 *
 * The kit's ContextMenu reserves the icon column whether or not an icon is
 * passed, so an unmarked row is invisible to every other check in the repo:
 * it compiles, it renders, it passes its unit tests, and it reaches a person
 * as a menu with an empty gutter. Three of the four call sites shipped that
 * way. The rule: an object literal carrying `id`, `label` and `onSelect` —
 * the ContextMenuItem / WorkspaceMenuRow signature — must also carry an icon.
 *
 * Both directions are asserted. A rule that over-reaches onto every option
 * object in the codebase gets turned off, which is the same outcome as not
 * having it at all.
 */

const row = (body) => `export const items = [{ ${body} }]`

describe('must trip', () => {
  it('flags a menu row with no icon', () => {
    const hits = scanSource('surface.tsx', row("id: 'copy', label: 'Copy', onSelect: () => {}"))
    expect(hits.length).toBe(1)
    expect(hits[0].id).toBe('copy')
    expect(hits[0].label).toBe('Copy')
    expect(hits[0].reason).toBe('no icon')
  })

  it('flags `icon: undefined` — the KEY is not the mark', () => {
    const hits = scanSource(
      'surface.tsx',
      row("id: 'copy', label: 'Copy', icon: undefined, onSelect: () => {}"),
    )
    expect(hits.length).toBe(1)
    expect(hits[0].reason).toBe('icon is undefined')
  })

  it('flags `icon: null` for the same reason', () => {
    const hits = scanSource(
      'surface.tsx',
      row("id: 'copy', label: 'Copy', icon: null, onSelect: () => {}"),
    )
    expect(hits.length).toBe(1)
  })

  it('flags a row built in a .ts module — workspace-menu.ts is one', () => {
    // The rows are built where the two placements cannot come to differ,
    // and that module is deliberately renderer-free plain TypeScript. A
    // checker that only read .tsx would not guard the one call site that
    // exists BECAUSE the rows must not be built twice.
    const hits = scanSource(
      'workspace-menu.ts',
      "export const rows = [{ id: 'workspace-close', label: 'Close workspace', onSelect: () => {} }]",
    )
    expect(hits.length).toBe(1)
    expect(hits[0].id).toBe('workspace-close')
  })

  it('flags each unmarked row separately, so a menu reports all of them', () => {
    const hits = scanSource(
      'surface.tsx',
      `export const items = [
         { id: 'a', label: 'A', onSelect: () => {} },
         { id: 'b', label: 'B', icon: PencilIcon, onSelect: () => {} },
         { id: 'c', label: 'C', onSelect: () => {} },
       ]`,
    )
    expect(hits.map((h) => h.id)).toEqual(['a', 'c'])
  })

  it('fails CLOSED on a file it cannot parse, rather than skipping it', () => {
    const hits = scanSource('broken.tsx', 'export const x = {{{')
    expect(hits.length).toBe(1)
    expect(hits[0].reason).toBe('PARSE')
  })
})

describe('must NOT trip', () => {
  it('lets a marked row alone', () => {
    expect(
      scanSource(
        'surface.tsx',
        row("id: 'copy', label: 'Copy', icon: CopyIcon, onSelect: () => {}"),
      ),
    ).toEqual([])
  })

  it('lets the shorthand form alone — `icon` is still the mark', () => {
    expect(
      scanSource('surface.tsx', row("id: 'copy', label: 'Copy', icon, onSelect: () => {}")),
    ).toEqual([])
  })

  it('lets an option object alone: id + label with no onSelect is not a menu row', () => {
    // Select options, view descriptors and settings rows all wear id+label.
    // A rule that reported them would be reporting most of the codebase.
    expect(scanSource('surface.tsx', row("id: 'ssh', label: 'SSH', value: 22"))).toEqual([])
  })

  it('lets a handler object alone: onSelect with no label is not a row either', () => {
    expect(scanSource('surface.tsx', row("id: 'x', onSelect: () => {}"))).toEqual([])
  })

  it("lets the kit's own interface alone — a type is not a literal", () => {
    expect(
      scanSource(
        'context-menu.tsx',
        'export interface ContextMenuItem { id: string; label: string; onSelect: () => void }',
      ),
    ).toEqual([])
  })

  it('parses a .ts file with generic type parameters — jsx is a .tsx thing', () => {
    // `<T,>(v: T) => v` is an unclosed JSX element if the parser is told to
    // expect JSX, and the checker fails closed on a parse error — so a
    // checker that turned jsx on for every extension would have reported a
    // permanent PARSE violation in a module that has no menu row in it.
    expect(scanSource('fixtures.ts', 'export const identity = <T,>(v: T): T => v')).toEqual([])
  })
})
