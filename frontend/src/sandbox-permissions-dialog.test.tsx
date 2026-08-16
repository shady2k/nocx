// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { cleanup } from '@solidjs/testing-library'
import { showSandboxPermissions, type SandboxPermissionsResult } from './sandbox-permissions-dialog'

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
})

describe('showSandboxPermissions', () => {
  it('renders the workspace and baseline, checked by default, and confirms empty deltas', async () => {
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baseline: ['/a', '/b'],
      openDirectory: vi.fn(),
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    expect(dialog().querySelector('.sandbox-permissions-workspace')?.textContent).toBe('/workspace')
    expect(checkboxFor('/a').checked).toBe(true)
    expect(checkboxFor('/b').checked).toBe(true)

    footerButtons()[1].click()
    const result = await promise
    expect(result).toEqual({ add: [], remove: [] })
  })

  it('computes exact deltas: unchecked baseline entries become remove, additions stay add', async () => {
    const openDirectory = vi
      .fn<() => Promise<{ path: string }>>()
      .mockResolvedValueOnce({ path: '/d' })
      .mockResolvedValueOnce({ path: '/e' })
      .mockResolvedValueOnce({ path: '' })

    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baseline: ['/a', '/b', '/c'],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    // Uncheck /b → remove.
    uncheck('/b')

    // Add /d, then /e, then remove /e — the addition disappears.
    const add = dialog().querySelector<HTMLButtonElement>('.ui-row-list__add button')!
    add.click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))
    add.click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(2))

    await vi.waitFor(() => {
      const removes = dialog().querySelectorAll<HTMLButtonElement>('.ui-row-list__remove button')
      expect(removes.length).toBe(2)
    })

    // Remove the second addition (/e).
    const removes = dialog().querySelectorAll<HTMLButtonElement>('.ui-row-list__remove button')
    removes[1].click()

    await vi.waitFor(() => {
      expect(
        dialog().querySelectorAll<HTMLButtonElement>('.ui-row-list__remove button').length,
      ).toBe(1)
    })

    footerButtons()[1].click()
    const result = (await promise) as SandboxPermissionsResult
    expect(result.add).toEqual(['/d'])
    expect(result.remove).toEqual(['/b'])
  })

  it('re-enables a removed baseline path instead of emitting conflicting deltas', async () => {
    const openDirectory = vi
      .fn<() => Promise<{ path: string }>>()
      .mockResolvedValue({ path: '/baseline' })
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baseline: ['/baseline'],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    uncheck('/baseline')
    dialog().querySelector<HTMLButtonElement>('.ui-row-list__add button')!.click()
    await vi.waitFor(() => expect(checkboxFor('/baseline').checked).toBe(true))

    footerButtons()[1].click()
    await expect(promise).resolves.toEqual({ add: [], remove: [] })
  })

  it('a cancelled picker adds nothing and leaves the deltas unchanged', async () => {
    const openDirectory = vi.fn<() => Promise<{ path: string }>>().mockResolvedValue({ path: '' })

    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baseline: ['/a'],
      openDirectory,
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    dialog().querySelector<HTMLButtonElement>('.ui-row-list__add button')!.click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))

    footerButtons()[1].click()
    const result = await promise
    expect(result).toEqual({ add: [], remove: [] })
  })

  it('resolves null on Cancel', async () => {
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baseline: ['/a'],
      openDirectory: vi.fn(),
    })
    await vi.waitFor(() => expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy())

    footerButtons()[0].click()
    await expect(promise).resolves.toBeNull()
  })

  it('settles and disposes exactly once — the dialog leaves the DOM', async () => {
    const promise = showSandboxPermissions({
      workspace: '/workspace',
      baseline: ['/a'],
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
