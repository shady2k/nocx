// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { FolderPicker, showFolderPicker, type FolderEntry } from './folder-picker'
import { clearStack } from './overlay/stack'

afterEach(() => {
  cleanup()
  clearStack()
  document.body.innerHTML = ''
})

/** One directory tree, deliberately NOT in alphabetical order: the component
 *  renders what it is given, in the order it is given (files.list owns the
 *  order, and a second owner in the renderer is the defect). */
const TREE: Record<string, FolderEntry[]> = {
  '/home/dev': [
    { name: 'zeta', isDirectory: true },
    { name: 'notes.txt', isDirectory: false },
    { name: 'alpha', isDirectory: true },
  ],
  '/home/dev/alpha': [{ name: 'inner', isDirectory: true }],
  '/home': [{ name: 'dev', isDirectory: true }],
  '/': [{ name: 'home', isDirectory: true }],
}

/** The whole seam: a stub, no client, no dispatcher, no backend. */
function tree(overrides: Record<string, () => Promise<never>> = {}) {
  return vi.fn(async (path: string) => {
    const fail = overrides[path]
    if (fail) return fail()
    const entries = TREE[path]
    if (!entries) throw new Error(`no such directory: ${path}`)
    return { path, entries }
  })
}

const rows = (c: Element) => Array.from(c.querySelectorAll('.ui-folder-picker__row'))
const names = (c: Element) =>
  rows(c).map((r) => r.querySelector('.ui-folder-picker__label')?.textContent ?? '')
const pathInput = (c: Element) => c.querySelector<HTMLInputElement>('.ui-text-field__input')!
const button = (c: Element, label: string) =>
  Array.from(c.querySelectorAll<HTMLButtonElement>('button')).find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  )!

async function open(props: {
  initialPath?: string
  list: (path: string) => Promise<{ path: string; entries: FolderEntry[] }>
  onResolve: (chosen: string | null) => void
}) {
  const r = render(() => (
    <FolderPicker
      initialPath={props.initialPath ?? '/home/dev'}
      list={props.list}
      onResolve={props.onResolve}
    />
  ))
  await vi.waitFor(() => {
    expect(r.container.querySelector('.ui-folder-picker')).toBeTruthy()
  })
  return r
}

describe('FolderPicker', () => {
  it('lists the initial directory and renders its entries in the order given', async () => {
    const list = tree()
    const { container } = await open({ list, onResolve: vi.fn() })

    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))
    expect(list).toHaveBeenCalledWith('/home/dev')
    // Given order, not sorted order — alpha stays last.
    expect(names(container)).toEqual(['zeta', 'notes.txt', 'alpha'])
    expect(pathInput(container).value).toBe('/home/dev')
  })

  it('resolves the absolute path of the directory that was chosen', async () => {
    const onResolve = vi.fn()
    const { container } = await open({ list: tree(), onResolve })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    fireEvent.click(rows(container)[2]) // alpha
    expect(pathInput(container).value).toBe('/home/dev/alpha')
    fireEvent.click(button(container, 'Choose'))

    expect(onResolve).toHaveBeenCalledWith('/home/dev/alpha')
  })

  it('descends into a directory on double click and can then choose it', async () => {
    const onResolve = vi.fn()
    const list = tree()
    const { container } = await open({ list, onResolve })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    fireEvent.dblClick(rows(container)[2]) // alpha
    await vi.waitFor(() => expect(names(container)).toEqual(['inner']))
    expect(list).toHaveBeenCalledWith('/home/dev/alpha')

    fireEvent.click(button(container, 'Choose'))
    expect(onResolve).toHaveBeenCalledWith('/home/dev/alpha')
  })

  it('cannot choose a file: clicking one selects nothing and never resolves', async () => {
    const onResolve = vi.fn()
    const { container } = await open({ list: tree(), onResolve })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    const file = rows(container)[1] // notes.txt
    expect(file.getAttribute('aria-disabled')).toBe('true')
    fireEvent.click(file)
    fireEvent.dblClick(file)

    expect(onResolve).not.toHaveBeenCalled()
    expect(pathInput(container).value).toBe('/home/dev')

    // And the answer the dialog would give is still the directory.
    fireEvent.click(button(container, 'Choose'))
    expect(onResolve).toHaveBeenCalledWith('/home/dev')
  })

  it('shows the reason in place when a listing fails, keeps the listing, and recovers', async () => {
    const onResolve = vi.fn()
    const list = tree({
      '/home/dev/zeta': () => Promise.reject(new Error('permission denied')),
    })
    const { container } = await open({ list, onResolve })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    fireEvent.dblClick(rows(container)[0]) // zeta
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-status-card')?.textContent).toContain('permission denied')
    })
    expect(container.querySelector('.ui-status-card')?.textContent).toContain('/home/dev/zeta')

    // Not closed, not emptied: the previous listing is still on screen and the
    // rest of the dialog still works.
    expect(onResolve).not.toHaveBeenCalled()
    expect(names(container)).toEqual(['zeta', 'notes.txt', 'alpha'])

    // A later success clears the reason.
    fireEvent.input(pathInput(container), { target: { value: '/home/dev/alpha' } })
    fireEvent.click(button(container, 'Go'))
    await vi.waitFor(() => expect(names(container)).toEqual(['inner']))
    expect(container.querySelector('.ui-status-card')).toBeNull()
  })

  it('resolves a typed path even when no listing ever succeeded', async () => {
    const onResolve = vi.fn()
    const list = vi.fn(() => Promise.reject(new Error('backend is asleep')))
    const { container } = await open({ list, onResolve })

    await vi.waitFor(() => {
      expect(container.querySelector('.ui-status-card')?.textContent).toContain('backend is asleep')
    })

    fireEvent.input(pathInput(container), { target: { value: '/srv/data' } })
    fireEvent.click(button(container, 'Choose'))

    expect(onResolve).toHaveBeenCalledWith('/srv/data')
  })

  it('confirms a typed path with Enter in the path field', async () => {
    const onResolve = vi.fn()
    const { container } = await open({ list: tree(), onResolve })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    const input = pathInput(container)
    fireEvent.input(input, { target: { value: '/srv/data' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(onResolve).toHaveBeenCalledWith('/srv/data')
  })

  it('goes up to the parent directory, and stops at the root', async () => {
    const list = tree()
    const { container } = await open({ list, onResolve: vi.fn() })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    fireEvent.click(button(container, 'Up one level'))
    await vi.waitFor(() => expect(names(container)).toEqual(['dev']))
    expect(pathInput(container).value).toBe('/home')

    fireEvent.click(button(container, 'Up one level'))
    await vi.waitFor(() => expect(names(container)).toEqual(['home']))
    expect(pathInput(container).value).toBe('/')

    expect(button(container, 'Up one level').disabled).toBe(true)
  })

  it('cancel resolves null and changes nothing', async () => {
    const onResolve = vi.fn()
    const { container } = await open({ list: tree(), onResolve })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    fireEvent.click(button(container, 'Cancel'))
    expect(onResolve).toHaveBeenCalledWith(null)
  })

  it('says so when a directory holds nothing', async () => {
    const list = vi.fn((path: string) => Promise.resolve({ path, entries: [] }))
    const { container } = await open({ list, onResolve: vi.fn() })

    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })
    expect(rows(container)).toHaveLength(0)
  })

  it('ignores a listing that is overtaken by a later one', async () => {
    let releaseSlow: (() => void) | null = null
    const list = vi.fn(async (path: string) => {
      if (path === '/home/dev/zeta') {
        await new Promise<void>((r) => {
          releaseSlow = r
        })
        return { path, entries: [{ name: 'stale', isDirectory: true }] }
      }
      return { path, entries: TREE[path] ?? [] }
    })
    const { container } = await open({ list, onResolve: vi.fn() })
    await vi.waitFor(() => expect(rows(container)).toHaveLength(3))

    fireEvent.dblClick(rows(container)[0]) // zeta — hangs
    await vi.waitFor(() => expect(releaseSlow).not.toBeNull())

    fireEvent.input(pathInput(container), { target: { value: '/home/dev/alpha' } })
    fireEvent.click(button(container, 'Go'))
    await vi.waitFor(() => expect(names(container)).toEqual(['inner']))

    releaseSlow!()
    await Promise.resolve()
    expect(names(container)).toEqual(['inner'])
  })
})

describe('showFolderPicker', () => {
  it('resolves the chosen path and tears its host down', async () => {
    const promise = showFolderPicker({ initialPath: '/home/dev', list: tree() })

    await vi.waitFor(() => {
      expect(document.querySelector('.ui-folder-picker__row')).toBeTruthy()
    })
    const host = document.querySelector('.ui-folder-picker')!.closest('body > div')!
    fireEvent.click(Array.from(document.querySelectorAll('.ui-folder-picker__row'))[2])
    fireEvent.click(button(document.body, 'Choose'))

    await expect(promise).resolves.toBe('/home/dev/alpha')
    await vi.waitFor(() => expect(host.isConnected).toBe(false))
  })

  it('resolves null when cancelled', async () => {
    const promise = showFolderPicker({ initialPath: '/home/dev', list: tree() })

    await vi.waitFor(() => {
      expect(document.querySelector('.ui-folder-picker')).toBeTruthy()
    })
    fireEvent.click(button(document.body, 'Cancel'))

    await expect(promise).resolves.toBeNull()
  })
})
