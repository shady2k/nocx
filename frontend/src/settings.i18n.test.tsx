// @vitest-environment jsdom
/**
 * i18n tests for the settings surface (nocx-dej6 stage 3g) — the proof that
 * its chrome reads through the catalogue rather than the literal: the surface
 * mounted under 'ru' shows the Russian catalogue values, where the hardcoded
 * checker can only prove that the literal sits inside a t() call. The same
 * distinction api.i18n.test draws for its surface.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from '@solidjs/testing-library'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { SettingsComponent } from './settings'
import { setActiveLocale } from './i18n/locale-model'

/** The describe-mock shape settings.test.ts uses, trimmed to what the mount
 *  needs: declarations, groups, the section→group map, and a snapshot with
 *  nothing modified. The describe call resolves on a microtask, so callers
 *  that read the rail wait for it. */
async function mountSettings(): Promise<HTMLElement> {
  const client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
  vi.spyOn(client, 'describeSettings').mockResolvedValue({
    declarations: [
      {
        key: 'app.confirmQuit',
        section: 'Application',
        label: 'Confirm Quit',
        description: 'Show confirmation dialog before quitting',
        control: 'toggle',
        dataClass: 'publicConfig',
        default: true,
      },
    ],
    groups: [{ id: 'application', title: 'Application', order: 0 }],
    sectionGroups: { Application: 'application' },
  })
  vi.spyOn(client, 'getSnapshot').mockResolvedValue({
    values: {},
    overridden: [],
    revision: 0,
  })
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <SettingsComponent profileClient={client} />, { container })
  // The search box is in the rail's leading slot from the first paint, in
  // either locale — the marker that the surface is up.
  await vi.waitFor(() => {
    expect(container.querySelector('.ui-settings-search input')).not.toBeNull()
  })
  return container
}

// The locale signal is module state; every test starts and leaves it English
// so one test's switch cannot decide what the next test reads.
beforeEach(() => {
  setActiveLocale('en')
})

afterEach(() => {
  setActiveLocale('en')
  vi.clearAllMocks()
  document.body.innerHTML = ''
})

describe('settings surface renders through the catalogue', () => {
  it('shows the Russian search placeholder under ru', async () => {
    setActiveLocale('ru')
    const container = await mountSettings()
    const field = container.querySelector<HTMLInputElement>('.ui-settings-search input')
    expect(field?.getAttribute('placeholder')).toBe('Поиск настроек…')
    expect(field?.getAttribute('placeholder')).not.toBe('Search settings…')
  })

  it('shows a Russian section heading under ru', async () => {
    setActiveLocale('ru')
    const container = await mountSettings()
    // The Page heading is settings.title — a minted chrome key, read here in
    // its Russian catalogue form. It is the surface's own heading, distinct
    // from anything the fixture's Go-side declarations supply.
    expect(container.textContent).toContain('Настройки')
    expect(container.textContent).not.toContain('Failed to load settings.')
  })

  it('shows the English chrome under en', async () => {
    const container = await mountSettings()
    expect(container.textContent).toContain('Settings')
    const field = container.querySelector<HTMLInputElement>('.ui-settings-search input')
    expect(field?.getAttribute('placeholder')).toBe('Search settings…')
  })
})
