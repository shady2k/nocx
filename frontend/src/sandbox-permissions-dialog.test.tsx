// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { cleanup } from '@solidjs/testing-library'
import { showSandboxPermissions, type SandboxPermissionsResult } from './sandbox-permissions-dialog'

const toasts: { message: string; level?: string }[] = []
vi.mock('./ui/toast', () => ({
  showToast: (t: { message: string; level?: string }) => {
    toasts.push(t)
  },
}))

const READ_ONLY_LIST = 'Read-only folders added for this tab'
const WRITABLE_LIST = 'Read & write folders added for this tab'
const SANDBOX_PERMISSIONS_CSS = resolve(
  import.meta.dirname ?? '.',
  'styles/surfaces/sandbox-permissions.css',
)

/** Find the baseline checkbox whose label is exactly `path`. */
function checkboxFor(path: string): HTMLInputElement {
  const labels = Array.from(document.querySelectorAll<HTMLLabelElement>('dialog .ui-checkbox'))
  const label = labels.find((l) => l.textContent?.trim() === path)
  if (!label) throw new Error(`checkbox for "${path}" not found`)
  return label.querySelector<HTMLInputElement>('input')!
}

function dialog(): HTMLDialogElement {
  const d = document.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
  if (!d) throw new Error('dialog not mounted')
  return d
}

/** The EditableRowList for one class section, by its accessible name. */
function sectionList(ariaLabel: string): HTMLElement {
  const list = Array.from(document.querySelectorAll<HTMLElement>('.ui-row-list')).find(
    (candidate) => candidate.getAttribute('aria-label') === ariaLabel,
  )
  if (!list) throw new Error(`row list "${ariaLabel}" not found`)
  return list
}

function addButton(ariaLabel: string): HTMLButtonElement {
  const btn = sectionList(ariaLabel).querySelector<HTMLButtonElement>('.ui-row-list__add button')
  if (!btn) throw new Error(`add button for "${ariaLabel}" not found`)
  return btn
}

function removeButtons(ariaLabel: string): HTMLButtonElement[] {
  return Array.from(
    sectionList(ariaLabel).querySelectorAll<HTMLButtonElement>('.ui-row-list__remove button'),
  )
}

/** Uncheck a currently-checked baseline row through the change event. */
function uncheck(path: string): void {
  const cb = checkboxFor(path)
  cb.checked = false
  cb.dispatchEvent(new Event('change', { bubbles: true }))
}

function footerButtons(): HTMLButtonElement[] {
  return Array.from(dialog().querySelectorAll<HTMLButtonElement>('.nocx-dialog__actions button'))
}

afterEach(() => {
  cleanup()
  document.body.innerHTML = ''
  toasts.length = 0
})

describe('showSandboxPermissions', () => {
  it('renders the workspace and both baselines, checked by default, and confirms empty deltas', async () => {
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: ['/a', '/b'],
      baselineReadOnly: ['/r1'],
      openDirectory: vi.fn(),
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    expect(dialog().querySelector('.sandbox-permissions-workspace')?.textContent).toBe('/workspace')
    expect(checkboxFor('/a').checked).toBe(true)
    expect(checkboxFor('/b').checked).toBe(true)
    expect(checkboxFor('/r1').checked).toBe(true)
    expect(dialog().textContent).toContain(
      'Folders strictly below host HOME also appear at their usual ~/… paths',
    )
    expect(dialog().textContent).toContain('HOME and ancestor grants stay absolute-only')
    expect(dialog().textContent).toContain('Projected folders can contain credentials')

    footerButtons()[1].click()
    const result = await promise
    expect(result).toEqual({
      addWritable: [],
      removeWritable: [],
      addReadOnly: [],
      removeReadOnly: [],
    })
  })

  it('computes exact deltas: unchecked baseline entries become removals, additions stay adds', async () => {
    const openDirectory = vi
      .fn<() => Promise<{ path: string }>>()
      .mockResolvedValueOnce({ path: '/d' })
      .mockResolvedValueOnce({ path: '/e' })
      .mockResolvedValueOnce({ path: '' })

    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: ['/a', '/b', '/c'],
      baselineReadOnly: ['/r1', '/r2'],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    // Uncheck /b (writable) and /r1 (read-only) → exact per-class removals.
    uncheck('/b')
    uncheck('/r1')

    // Add /d to the writable section and /e to the read-only section.
    addButton(WRITABLE_LIST).click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))
    addButton(READ_ONLY_LIST).click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(2))

    await vi.waitFor(() => {
      expect(removeButtons(WRITABLE_LIST).length).toBe(1)
      expect(removeButtons(READ_ONLY_LIST).length).toBe(1)
    })

    // Remove the read-only addition (/e) — only the writable /d remains.
    removeButtons(READ_ONLY_LIST)[0].click()
    await vi.waitFor(() => expect(removeButtons(READ_ONLY_LIST).length).toBe(0))

    footerButtons()[1].click()
    const result = (await promise) as SandboxPermissionsResult
    expect(result.addWritable).toEqual(['/d'])
    expect(result.removeWritable).toEqual(['/b'])
    expect(result.addReadOnly).toEqual([])
    expect(result.removeReadOnly).toEqual(['/r1'])
  })

  it('re-enables a removed baseline path in the same class instead of emitting conflicting deltas', async () => {
    const openDirectory = vi
      .fn<() => Promise<{ path: string }>>()
      .mockResolvedValue({ path: '/baseline' })
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: ['/baseline'],
      baselineReadOnly: [],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    uncheck('/baseline')
    addButton(WRITABLE_LIST).click()
    await vi.waitFor(() => expect(checkboxFor('/baseline').checked).toBe(true))

    footerButtons()[1].click()
    await expect(promise).resolves.toEqual({
      addWritable: [],
      removeWritable: [],
      addReadOnly: [],
      removeReadOnly: [],
    })
  })

  it('refuses a cross-class duplicate pick with visible feedback and no contradictory delta', async () => {
    const openDirectory = vi
      .fn<() => Promise<{ path: string }>>()
      .mockResolvedValue({ path: '/shared' })
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: [],
      baselineReadOnly: ['/shared'],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    // /shared is active in the read-only class; adding it to read & write must
    // be refused with feedback rather than emitted as an addWritable delta.
    addButton(WRITABLE_LIST).click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))
    await vi.waitFor(() => expect(toasts.some((t) => t.message.includes('/shared'))).toBe(true))

    footerButtons()[1].click()
    const result = await promise
    expect(result).toEqual({
      addWritable: [],
      removeWritable: [],
      addReadOnly: [],
      removeReadOnly: [],
    })
  })

  it('caps each class at 32 additions', async () => {
    const openDirectory = vi.fn<() => Promise<{ path: string }>>()
    for (let i = 0; i < 33; i++) openDirectory.mockResolvedValueOnce({ path: `/${i}` })

    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: [],
      baselineReadOnly: [],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    for (let i = 1; i <= 33; i++) {
      addButton(WRITABLE_LIST).click()
      await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(i))
      await vi.waitFor(() => {
        expect(removeButtons(WRITABLE_LIST).length).toBe(Math.min(i, 32))
      })
    }

    expect(toasts.some((t) => t.message.includes('At most 32'))).toBe(true)

    footerButtons()[1].click()
    const result = await promise
    expect(result?.addWritable).toHaveLength(32)
  })

  it('a cancelled picker adds nothing and leaves the deltas unchanged', async () => {
    const openDirectory = vi.fn<() => Promise<{ path: string }>>().mockResolvedValue({ path: '' })

    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: ['/a'],
      baselineReadOnly: ['/r1'],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    addButton(WRITABLE_LIST).click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))

    footerButtons()[1].click()
    const result = await promise
    expect(result).toEqual({
      addWritable: [],
      removeWritable: [],
      addReadOnly: [],
      removeReadOnly: [],
    })
  })

  it('resolves null on Cancel', async () => {
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: ['/a'],
      baselineReadOnly: [],
      openDirectory: vi.fn(),
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    footerButtons()[0].click()
    await expect(promise).resolves.toBeNull()
  })

  it('settles and disposes exactly once — the dialog leaves the DOM', async () => {
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baselineWritable: ['/a'],
      baselineReadOnly: [],
      openDirectory: vi.fn(),
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    footerButtons()[0].click()
    await promise

    // The deferred dispose removes the host after Dialog's own cleanup.
    await vi.waitFor(() => {
      expect(document.querySelector('dialog.nocx-dialog')).toBeNull()
    })
  })
})

describe('sandbox permissions layout', () => {
  it('stacks baseline directory entries from top to bottom', () => {
    const css = readFileSync(SANDBOX_PERMISSIONS_CSS, 'utf8')

    expect(css).toMatch(
      /\.sandbox-permissions-baseline\s*\{[^}]*display:\s*flex;[^}]*flex-direction:\s*column;/s,
    )
  })
})
