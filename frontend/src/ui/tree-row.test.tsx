// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TreeRow, type TreeRowKind } from './tree-row'

afterEach(cleanup)

/** Render one row and return the .ui-tree-row element, for glyph assertions. */
function rowFor(props: {
  name: string
  kind: TreeRowKind
  linkKind?: TreeRowKind
  cyclic?: boolean
  expanded?: boolean
}): HTMLElement {
  const { container } = render(() => <TreeRow depth={0} {...props} />)
  return container.querySelector('.ui-tree-row') as HTMLElement
}

/** The type-icon glyph's inner SVG markup — the wire distinguishes kinds, the
 *  kit decides the glyph, and this is how a jsdom test (no layout) tells them
 *  apart. */
function glyphOf(row: HTMLElement): string {
  return row.querySelector('.ui-tree-row__type-icon svg')?.innerHTML ?? ''
}
describe('TreeRow', () => {
  it('renders a row at a depth with the name visible', () => {
    const { container } = render(() => (
      <TreeRow name="src" depth={2} kind="dir" onToggle={() => undefined} />
    ))
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-depth')).toBe('2')
    expect(row?.getAttribute('aria-level')).toBe('3')
    expect(row?.textContent).toContain('src')
  })

  it('defaults the row title to the name when no hint is given', () => {
    render(() => <TreeRow name="src" depth={0} kind="dir" />)
    const title = screen.getByRole('treeitem', { name: 'src' })
    const nameSpan = title.querySelector('.ui-tree-row__name') as HTMLElement
    expect(nameSpan.getAttribute('title')).toBe('src')
  })

  it('a hint becomes the hover title while the name stays the text and accessible name', () => {
    render(() => <TreeRow name="secret.key" depth={0} kind="regular" hint="permission denied" />)
    const row = screen.getByRole('treeitem', { name: 'secret.key' })
    const nameSpan = row.querySelector('.ui-tree-row__name') as HTMLElement
    expect(nameSpan.getAttribute('title')).toBe('permission denied')
    expect(nameSpan.textContent).toBe('secret.key')
  })

  it('offers a keyboard-operable disclosure for a directory and announces its expanded state', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <TreeRow name="src" depth={0} kind="dir" expanded onToggle={onToggle} />
    ))
    const disclosure = container.querySelector('.ui-tree-row__disclosure')
    expect(disclosure).not.toBeNull()
    expect(disclosure?.tagName).toBe('BUTTON')
    expect(disclosure?.getAttribute('aria-expanded')).toBe('true')
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-disclosure')).toBe('expanded')
    expect(row?.getAttribute('aria-expanded')).toBe('true')
    fireEvent.click(disclosure!)
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  it('a collapsed directory announces collapsed and its disclosure activates the callback', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <TreeRow name="src" depth={0} kind="dir" onToggle={onToggle} />
    ))
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-disclosure')).toBe('collapsed')
    expect(row?.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(container.querySelector('.ui-tree-row__disclosure')!)
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  it('renders no disclosure for a file', () => {
    const { container } = render(() => <TreeRow name="main.ts" depth={1} kind="regular" />)
    const row = container.querySelector('.ui-tree-row')
    expect(container.querySelector('.ui-tree-row__disclosure')).toBeNull()
    expect(row?.getAttribute('data-disclosure')).toBe('leaf')
    expect(row?.getAttribute('aria-expanded')).toBeNull()
  })

  it('a symlink to a directory is expandable', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <TreeRow name="docs" depth={0} kind="symlink" linkKind="dir" onToggle={onToggle} />
    ))
    expect(container.querySelector('.ui-tree-row__disclosure')).not.toBeNull()
    expect(container.querySelector('.ui-tree-row')?.getAttribute('data-link-kind')).toBe('dir')
  })

  it('a cyclic symlink renders as a leaf with no disclosure, even with a toggle supplied', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <TreeRow name="loop" depth={0} kind="symlink" linkKind="dir" cyclic onToggle={onToggle} />
    ))
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-cyclic')).toBe('true')
    expect(container.querySelector('.ui-tree-row__disclosure')).toBeNull()
    expect(row?.getAttribute('data-disclosure')).toBe('leaf')
  })

  it('a broken symlink renders as a leaf', () => {
    const { container } = render(() => (
      <TreeRow name="gone" depth={0} kind="symlink" linkKind="other" />
    ))
    expect(container.querySelector('.ui-tree-row__disclosure')).toBeNull()
    expect(container.querySelector('.ui-tree-row')?.getAttribute('data-disclosure')).toBe('leaf')
  })

  it('an `other` entry — a FIFO, socket or device — lists and cannot be expanded', () => {
    // The wire lists these (design §5.1 Kind); mapping them to `regular` would
    // offer to open something whose read blocks forever, which is the hang the
    // openability table exists to prevent.
    const { container } = render(() => <TreeRow name="docker.sock" depth={1} kind="other" />)
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-kind')).toBe('other')
    expect(container.querySelector('.ui-tree-row__disclosure')).toBeNull()
    expect(row?.getAttribute('data-disclosure')).toBe('leaf')
    expect(row?.textContent).toContain('docker.sock')
  })

  it("kind 'unreadable' renders and cannot be expanded", () => {
    // Metadata that could not be read is a distinct wire kind, not `other` and
    // not a broken symlink: a listing must never fabricate plausible empties.
    const { container } = render(() => <TreeRow name="root-only" depth={1} kind="unreadable" />)
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-kind')).toBe('unreadable')
    expect(container.querySelector('.ui-tree-row__disclosure')).toBeNull()
    expect(row?.textContent).toContain('root-only')
  })

  it('renders an unreadable row instead of nothing', () => {
    const { container } = render(() => (
      <TreeRow name="secret.key" depth={0} kind="regular" disabled />
    ))
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-disabled')).toBe('true')
    expect(row?.getAttribute('aria-disabled')).toBe('true')
    expect(row?.textContent).toContain('secret.key')
  })

  it('marks a loading directory busy and disables its disclosure', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <TreeRow name="src" depth={0} kind="dir" busy onToggle={onToggle} />
    ))
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-busy')).toBe('true')
    const disclosure = container.querySelector('.ui-tree-row__disclosure') as HTMLButtonElement
    expect(disclosure.disabled).toBe(true)
    fireEvent.click(disclosure)
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('renders the trailing badge slot', () => {
    const { container } = render(() => (
      <TreeRow name="src" depth={0} kind="dir" badge={<span>12 items</span>} />
    ))
    expect(container.querySelector('.ui-tree-row__badge')?.textContent).toBe('12 items')
  })

  it('reflects selection and focus as typed state', () => {
    const { container } = render(() => (
      <TreeRow name="a" depth={0} kind="regular" selected focused />
    ))
    const row = container.querySelector('.ui-tree-row')
    expect(row?.getAttribute('data-selected')).toBe('true')
    expect(row?.getAttribute('data-focused')).toBe('true')
    expect(row?.getAttribute('aria-selected')).toBe('true')
  })

  describe('type icons', () => {
    it('a file row renders the file glyph', () => {
      expect(glyphOf(rowFor({ name: 'main.ts', kind: 'regular' }))).toContain(
        'M6 22a2 2 0 0 1-2-2V4',
      )
    })

    it('a directory follows the disclosure: closed folder collapsed, open folder expanded', () => {
      const collapsed = rowFor({ name: 'src', kind: 'dir' })
      const expanded = rowFor({ name: 'src', kind: 'dir', expanded: true })
      expect(glyphOf(collapsed)).toContain('M20 20a2 2 0 0 0 2-2V8')
      expect(glyphOf(expanded)).toContain('m6 14 1.5-2.9')
      expect(glyphOf(expanded)).not.toBe(glyphOf(collapsed))
    })

    it('a symlink to a file renders the symlink glyph, distinct from a plain file', () => {
      const link = rowFor({ name: 'notes.md', kind: 'symlink', linkKind: 'regular' })
      expect(glyphOf(link)).toContain('m10 18 3-3-3-3')
      expect(glyphOf(link)).not.toBe(glyphOf(rowFor({ name: 'notes.md', kind: 'regular' })))
    })

    it('a symlink into a directory is a folder, following the disclosure state', () => {
      const collapsed = rowFor({ name: 'docs', kind: 'symlink', linkKind: 'dir' })
      const expanded = rowFor({ name: 'docs', kind: 'symlink', linkKind: 'dir', expanded: true })
      expect(glyphOf(collapsed)).toContain('M20 20a2 2 0 0 0 2-2V8')
      expect(glyphOf(expanded)).toContain('m6 14 1.5-2.9')
    })

    it('a cyclic symlink renders a leaf glyph, not a folder', () => {
      const loop = rowFor({ name: 'loop', kind: 'symlink', linkKind: 'dir', cyclic: true })
      expect(glyphOf(loop)).toContain('m10 18 3-3-3-3')
      expect(glyphOf(loop)).not.toContain('M20 20a2 2 0 0 0 2-2V8')
    })

    it('an `other` entry — FIFO, socket or device — gets its own glyph, not the file glyph', () => {
      const other = rowFor({ name: 'docker.sock', kind: 'other' })
      expect(glyphOf(other)).toContain('width="20" height="8" x="2" y="2"')
      expect(glyphOf(other)).not.toBe(glyphOf(rowFor({ name: 'x', kind: 'regular' })))
    })

    it("an 'unreadable' entry renders the unreadable glyph, not a plain file", () => {
      const unreadable = rowFor({ name: 'root-only', kind: 'unreadable' })
      expect(glyphOf(unreadable)).toContain('m14.5 12.5-5 5')
      expect(glyphOf(unreadable)).not.toBe(glyphOf(rowFor({ name: 'x', kind: 'regular' })))
    })
  })

  describe('leading slot', () => {
    it('a leaf row and a directory row at the same depth put their names at the same offset', () => {
      // jsdom has no layout, so the alignment is asserted structurally: both
      // rows carry the same leading slot, and in both the name is preceded by
      // exactly the leading slot and the type icon. A file therefore starts
      // where a directory's name starts, one disclosure-width in.
      const { container } = render(() => (
        <div>
          <TreeRow name="main.ts" depth={0} kind="regular" />
          <TreeRow name="src" depth={0} kind="dir" onToggle={() => undefined} />
        </div>
      ))
      const rows = container.querySelectorAll('.ui-tree-row')
      expect(rows.length).toBe(2)
      for (const row of rows) {
        const leading = row.querySelector('.ui-tree-row__leading')
        const typeIcon = row.querySelector('.ui-tree-row__type-icon')
        const name = row.querySelector('.ui-tree-row__name')
        expect(leading).not.toBeNull()
        expect(typeIcon).not.toBeNull()
        expect(typeIcon?.previousElementSibling).toBe(leading)
        expect(name?.previousElementSibling).toBe(typeIcon)
      }
    })
  })

  describe('accessibility', () => {
    it('the icon is decorative: the row keeps one accessible name, the entry name', () => {
      render(() => <TreeRow name="main.ts" depth={0} kind="regular" />)
      expect(screen.getByRole('treeitem', { name: 'main.ts' })).toBeTruthy()
    })
  })
})
