// @vitest-environment jsdom
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CollectionRow, CollectionView } from './collection-view'

afterEach(cleanup)

describe('CollectionView', () => {
  it('composes one searchable toolbar and shared rows', () => {
    const onSearch = vi.fn()
    const { container } = render(() => (
      <CollectionView
        searchValue=""
        onSearch={onSearch}
        searchPlaceholder="Filter things"
        searchLabel="Filter things"
        actions={<button type="button">Add</button>}
        hasItems
        empty={<p>Empty</p>}
      >
        <CollectionRow info={<span>Item</span>} actions={<button type="button">Edit</button>} />
      </CollectionView>
    ))

    fireEvent.input(container.querySelector('input')!, { target: { value: 'item' } })
    expect(onSearch).toHaveBeenCalledWith('item')
    expect(container.querySelector('.ui-collection-row')?.textContent).toContain('Item')
    expect(container.querySelector('.ui-collection-view__actions')?.textContent).toBe('Add')
  })

  it('renders the supplied empty state without a list body', () => {
    const { container } = render(() => (
      <CollectionView
        searchValue=""
        onSearch={() => undefined}
        searchPlaceholder="Filter things"
        searchLabel="Filter things"
        actions={null}
        hasItems={false}
        empty={<p>No things</p>}
      >
        <span>Hidden</span>
      </CollectionView>
    ))

    expect(container.textContent).toContain('No things')
    expect(container.querySelector('.ui-collection-view__body')).toBeNull()
  })
})
