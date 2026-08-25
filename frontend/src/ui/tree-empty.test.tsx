// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@solidjs/testing-library'
import { readFileSync } from 'node:fs'
import { TreeEmpty } from './tree-empty'

afterEach(cleanup)

const CSS = readFileSync('src/styles/components/tree-empty.css', 'utf8')

describe('TreeEmpty', () => {
  it('says the folder is empty, one step in from it', () => {
    const { container } = render(() => <TreeEmpty depth={2} />)
    const el = container.querySelector<HTMLElement>('.ui-tree-empty')
    expect(el).not.toBeNull()
    expect(el?.textContent).toBe('Empty')
    // The indent is the NUMBER, the same technique TreeRow uses, so a tree
    // that grows a level does not need this component changed.
    expect(el?.getAttribute('data-depth')).toBe('2')
    expect(el?.style.getPropertyValue('--tree-row-depth')).toBe('2')
  })

  it('takes a truer word when the surface has one', () => {
    const { container } = render(() => <TreeEmpty depth={1} label="Nothing matched" />)
    expect(container.querySelector('.ui-tree-empty')?.textContent).toBe('Nothing matched')
  })

  it('is out of the accessibility tree, because it is not an entry in it', () => {
    // A reader walking a `role="tree"` hears aria-level and was never misled;
    // the confusion this exists to fix is a sighted one. A `treeitem` that
    // cannot be opened, selected or acted on would be worse than no row.
    const { container } = render(() => <TreeEmpty depth={1} />)
    const el = container.querySelector<HTMLElement>('.ui-tree-empty')
    expect(el?.getAttribute('aria-hidden')).toBe('true')
    expect(el?.getAttribute('role')).toBeNull()
  })

  it('is indented by the kit step, not by one of its own', () => {
    // Two owners of "how far in is a level" would agree until a theme moved
    // one of them. The row declares --tree-row-indent and so does this, with
    // the same default, and nothing else names a number.
    expect(CSS).toContain('--tree-row-indent: var(--space-4)')
    expect(CSS).toContain('var(--tree-row-depth) * var(--tree-row-indent)')
  })
})
