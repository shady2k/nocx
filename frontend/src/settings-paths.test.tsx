// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from 'vitest'
import { cleanup } from '@solidjs/testing-library'
import { SettingsContent } from './settings-content'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import type { Declaration } from './settings-domain'
import type { DialogClient } from './dialog-client'
import type { PaneHost } from './pane-content'

const WRITABLE_PATHS_KEY = 'sandbox.allowedWritablePaths'
const READ_ONLY_PATHS_KEY = 'sandbox.allowedReadOnlyPaths'

const WRITABLE_DECL: Declaration = {
  key: WRITABLE_PATHS_KEY,
  section: 'Experimental',
  label: 'Sandbox read & write folders',
  description: 'Additional folders available read/write in every new sandboxed tab.',
  control: 'paths',
  dataClass: 'privateMetadata',
  default: [],
}

const READ_ONLY_DECL: Declaration = {
  key: READ_ONLY_PATHS_KEY,
  section: 'Experimental',
  label: 'Sandbox read-only folders',
  description: 'Additional folders available read-only in every new sandboxed tab.',
  control: 'paths',
  dataClass: 'privateMetadata',
  default: [],
}

function mockTabHost(): PaneHost {
  return {
    contentSettled: vi.fn(),
    setTitle: vi.fn(),
    updateTooltip: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
  }
}

function pathsList(key: string): HTMLElement {
  const row = document.getElementById(`st-setting-${key}`)
  const list = row?.querySelector<HTMLElement>('.ui-settings-paths .ui-row-list')
  if (!list) throw new Error(`paths EditableRowList not rendered for ${key}`)
  return list
}

function addButton(key: string): HTMLButtonElement {
  const row = document.getElementById(`st-setting-${key}`)
  const btn = row?.querySelector<HTMLButtonElement>('.ui-settings-paths .ui-row-list__add button')
  if (!btn) throw new Error(`paths add button not rendered for ${key}`)
  return btn
}

function pathRows(key: string): HTMLElement[] {
  return Array.from(pathsList(key).querySelectorAll<HTMLElement>('.ui-settings-paths-row'))
}

describe('settings paths control (sandbox read-only and read & write folders)', () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let openDirectory: ReturnType<typeof vi.fn>
  let setSetting: MockInstance<ProfileClient['setSetting']>

  beforeEach(() => {
    document.body.replaceChildren()
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher())
    openDirectory = vi.fn()
    vi.spyOn(client, 'describeSettings').mockResolvedValue({
      declarations: [READ_ONLY_DECL, WRITABLE_DECL],
      groups: [
        { id: 'assistant', title: 'Assistant', order: 0 },
        { id: 'vault', title: 'Vault', order: 1 },
        { id: 'application', title: 'Application', order: 2 },
        { id: 'developer', title: 'Developer', order: 3 },
      ],
      sectionGroups: { Experimental: 'developer' },
    })
    setSetting = vi.spyOn(client, 'setSetting').mockResolvedValue({ ok: true })
  })

  afterEach(() => cleanup())

  async function mount(writable: unknown = [], readOnly: unknown = []): Promise<SettingsContent> {
    vi.spyOn(client, 'getSnapshot').mockResolvedValue({
      values: {
        [WRITABLE_PATHS_KEY]: writable,
        [READ_ONLY_PATHS_KEY]: readOnly,
      },
      overridden: [WRITABLE_PATHS_KEY, READ_ONLY_PATHS_KEY],
      revision: 0,
    })
    const dialogClient = { openDirectoryDialog: openDirectory } as unknown as DialogClient
    const content = new SettingsContent(client, undefined, undefined, undefined, dialogClient)
    await content.mount(target, mockTabHost(), new AbortController().signal)
    const experimental = target.querySelector<HTMLButtonElement>(
      '.ui-grouped-nav__item[data-item="Experimental"] > .ui-button',
    )
    if (!experimental) throw new Error('Experimental settings page not rendered')
    experimental.click()
    await vi.waitFor(() => {
      expect(document.getElementById(`st-setting-${WRITABLE_PATHS_KEY}`)).toBeTruthy()
    })
    return content
  }

  it('renders both classes as separate paths controls', async () => {
    await mount(['/a'], ['/r1'])

    expect(pathsList(WRITABLE_PATHS_KEY)).toBeTruthy()
    expect(pathsList(READ_ONLY_PATHS_KEY)).toBeTruthy()
    expect(pathRows(WRITABLE_PATHS_KEY).map((r) => r.textContent)).toEqual(['/a'])
    expect(pathRows(READ_ONLY_PATHS_KEY).map((r) => r.textContent)).toEqual(['/r1'])
  })

  it('Add folder appends the picked directory and saves the complete array', async () => {
    await mount()
    openDirectory.mockResolvedValue({ path: '/picked' })

    addButton(WRITABLE_PATHS_KEY).click()
    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(WRITABLE_PATHS_KEY, ['/picked'])
    })
  })

  it('Add folder on the read-only list appends to that class only', async () => {
    await mount(['/w'], [])
    openDirectory.mockResolvedValue({ path: '/ro' })

    addButton(READ_ONLY_PATHS_KEY).click()
    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(READ_ONLY_PATHS_KEY, ['/ro'])
    })
    // The writable list was not touched.
    expect(setSetting).not.toHaveBeenCalledWith(WRITABLE_PATHS_KEY, expect.anything())
  })

  it('repeated picks append rather than replace the complete array', async () => {
    await mount()
    openDirectory.mockResolvedValueOnce({ path: '/a' }).mockResolvedValueOnce({ path: '/b' })

    addButton(WRITABLE_PATHS_KEY).click()
    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(WRITABLE_PATHS_KEY, ['/a'])
    })
    // Wait for the accepted value to land in the mirror before the next pick.
    await vi.waitFor(() => expect(pathRows(WRITABLE_PATHS_KEY).length).toBe(1))

    addButton(WRITABLE_PATHS_KEY).click()
    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenLastCalledWith(WRITABLE_PATHS_KEY, ['/a', '/b'])
    })
  })

  it('a cancelled picker is a no-op — nothing is saved', async () => {
    await mount()
    openDirectory.mockResolvedValue({ path: '' })

    addButton(WRITABLE_PATHS_KEY).click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))

    expect(setSetting).not.toHaveBeenCalled()
  })

  it('per-row remove sends the complete remaining array', async () => {
    await mount(['/a', '/b'])

    const remove = pathsList(WRITABLE_PATHS_KEY).querySelector<HTMLButtonElement>(
      '.ui-row-list__remove button',
    )
    expect(remove).toBeTruthy()
    remove!.click()

    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(WRITABLE_PATHS_KEY, ['/b'])
    })
  })

  it('a rejected save renders in the row\u2019s existing error slot', async () => {
    await mount()
    openDirectory.mockResolvedValue({ path: '/picked' })
    setSetting.mockRejectedValueOnce(new Error('path does not exist'))

    addButton(WRITABLE_PATHS_KEY).click()

    await vi.waitFor(() => {
      const error = target.querySelector<HTMLElement>('.ui-settings-error')
      expect(error?.textContent).toContain('path does not exist')
    })
  })

  it('an unavailable picker surfaces the failure in the error slot', async () => {
    await mount()
    openDirectory.mockRejectedValue(new Error('no native runtime'))

    addButton(READ_ONLY_PATHS_KEY).click()

    await vi.waitFor(() => {
      const error = target.querySelector<HTMLElement>('.ui-settings-error')
      expect(error?.textContent).toContain('no native runtime')
    })
    expect(setSetting).not.toHaveBeenCalled()
  })

  it('the same folder in both classes is refused with a visible error and no RPC', async () => {
    await mount(['/shared'], ['/ro-only'])
    openDirectory.mockResolvedValue({ path: '/shared' })

    addButton(READ_ONLY_PATHS_KEY).click()

    await vi.waitFor(() => {
      const error = pathsList(READ_ONLY_PATHS_KEY).querySelector<HTMLElement>('.ui-field-error')
      expect(error?.textContent).toContain('already a read & write folder')
    })
    // No RPC was sent.
    expect(setSetting).not.toHaveBeenCalled()
  })

  it('the same folder in both classes is refused from the writable side too', async () => {
    await mount([], ['/shared'])
    openDirectory.mockResolvedValue({ path: '/shared' })

    addButton(WRITABLE_PATHS_KEY).click()

    await vi.waitFor(() => {
      const error = pathsList(WRITABLE_PATHS_KEY).querySelector<HTMLElement>('.ui-field-error')
      expect(error?.textContent).toContain('already a read-only folder')
    })
    expect(setSetting).not.toHaveBeenCalled()
  })

  // The backend refuses a read-only path below a writable one, so letting the
  // pick through only moves the refusal somewhere the user cannot read it.
  // This surface used to compare exact strings, so it did exactly that.
  it('a read-only folder inside a read & write folder is refused, naming the folder', async () => {
    await mount(['/shared'], [])
    openDirectory.mockResolvedValue({ path: '/shared/sub' })

    addButton(READ_ONLY_PATHS_KEY).click()

    await vi.waitFor(() => {
      const error = pathsList(READ_ONLY_PATHS_KEY).querySelector<HTMLElement>('.ui-field-error')
      expect(error?.textContent).toContain('/shared')
    })
    expect(setSetting).not.toHaveBeenCalled()
  })

  // The reverse nesting is the point of having two classes (ADR-0039): a
  // writable island inside a broader read-only tree. The surface must not
  // invent a conflict the backend does not have.
  it('a read & write folder inside a read-only folder reaches the backend', async () => {
    await mount([], ['/shared'])
    openDirectory.mockResolvedValue({ path: '/shared/sub' })

    addButton(WRITABLE_PATHS_KEY).click()

    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(WRITABLE_PATHS_KEY, ['/shared/sub'])
    })
  })
})
