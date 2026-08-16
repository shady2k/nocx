// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach, type MockInstance } from 'vitest'
import { cleanup } from '@solidjs/testing-library'
import { SettingsContent } from './settings-content'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import type { Declaration } from './settings-domain'
import type { DialogClient } from './dialog-client'
import type { TabHost } from './tab-content'

const PATHS_KEY = 'sandbox.allowedWritablePaths'

const PATHS_DECL: Declaration = {
  key: PATHS_KEY,
  section: 'Experimental',
  label: 'Sandbox writable allowlist',
  description: 'Additional folders available read/write in every new sandboxed tab.',
  control: 'paths',
  dataClass: 'privateMetadata',
  default: [],
}

function mockTabHost(): TabHost {
  return {
    setTitle: vi.fn(),
    updateTooltip: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
  }
}

function pathsList(target: HTMLElement): HTMLElement {
  const list = target.querySelector<HTMLElement>('.ui-settings-paths .ui-row-list')
  if (!list) throw new Error('paths EditableRowList not rendered')
  return list
}

function addButton(target: HTMLElement): HTMLButtonElement {
  const btn = target.querySelector<HTMLButtonElement>('.ui-settings-paths .ui-row-list__add button')
  if (!btn) throw new Error('paths add button not rendered')
  return btn
}

describe('settings paths control (sandbox.allowedWritablePaths)', () => {
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
      declarations: [PATHS_DECL],
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

  async function mount(values: unknown): Promise<SettingsContent> {
    vi.spyOn(client, 'getSnapshot').mockResolvedValue({
      values: { [PATHS_KEY]: values },
      overridden: [PATHS_KEY],
      revision: 0,
    })
    const dialogClient = { openDirectoryDialog: openDirectory } as unknown as DialogClient
    const content = new SettingsContent(client, undefined, undefined, undefined, dialogClient)
    await content.mount(target, mockTabHost(), new AbortController().signal)
    return content
  }

  it('Add folder appends the picked directory and saves the complete array', async () => {
    await mount([])
    openDirectory.mockResolvedValue({ path: '/picked' })

    addButton(target).click()
    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(PATHS_KEY, ['/picked'])
    })
  })

  it('a cancelled picker is a no-op — nothing is saved', async () => {
    await mount([])
    openDirectory.mockResolvedValue({ path: '' })

    addButton(target).click()
    await vi.waitFor(() => expect(openDirectory).toHaveBeenCalledTimes(1))

    expect(setSetting).not.toHaveBeenCalled()
  })

  it('per-row remove sends the complete remaining array', async () => {
    await mount(['/a', '/b'])

    const remove = pathsList(target).querySelector<HTMLButtonElement>('.ui-row-list__remove button')
    expect(remove).toBeTruthy()
    remove!.click()

    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith(PATHS_KEY, ['/b'])
    })
  })

  it('a rejected save renders in the row\u2019s existing error slot', async () => {
    await mount([])
    openDirectory.mockResolvedValue({ path: '/picked' })
    setSetting.mockRejectedValueOnce(new Error('path does not exist'))

    addButton(target).click()

    await vi.waitFor(() => {
      const error = target.querySelector<HTMLElement>('.ui-settings-error')
      expect(error?.textContent).toContain('path does not exist')
    })
  })

  it('an unavailable picker surfaces the failure in the error slot', async () => {
    await mount([])
    openDirectory.mockRejectedValue(new Error('no native runtime'))

    addButton(target).click()

    await vi.waitFor(() => {
      const error = target.querySelector<HTMLElement>('.ui-settings-error')
      expect(error?.textContent).toContain('no native runtime')
    })
    expect(setSetting).not.toHaveBeenCalled()
  })
})
