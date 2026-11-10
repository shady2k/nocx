// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { createSignal, type Component } from 'solid-js'
import { mountSidebar, type SidebarAction, type SidebarViewDescriptor } from './sidebar'
import { createUpdateController } from './update-controller'
import { ToastHost } from './ui/toast'

const TestIcon: Component = () => <svg data-test-icon="1" />
const TestView: Component = () => <div>Updates test view</div>
const settings: SidebarAction = {
  id: 'settings',
  title: () => 'Settings',
  icon: TestIcon,
  onActivate: vi.fn(),
}
const views: SidebarViewDescriptor[] = [
  { id: 'test', title: () => 'Test', icon: TestIcon, view: TestView, order: 0 },
]

afterEach(() => {
  cleanup()
  document.body.replaceChildren()
})

describe('activity bar update indicator', () => {
  it('adds a reactive update indicator to the existing roving toolbar without changing its panel', () => {
    const bar = document.createElement('div')
    const panel = document.createElement('div')
    document.body.append(bar, panel)
    const [available, setAvailable] = createSignal(false)
    const onActivate = vi.fn()
    const handle = mountSidebar(
      bar,
      panel,
      views,
      [settings],
      null,
      undefined,
      undefined,
      undefined,
      {
        title: () => 'Update available',
        icon: TestIcon,
        level: () => 'neutral',
        available,
        onActivate,
      },
    )
    expect(bar.querySelector('[aria-label="Update available"]')).toBeNull()
    setAvailable(true)
    const indicator = bar.querySelector<HTMLButtonElement>('[aria-label="Update available"]')!
    expect(indicator.getAttribute('tabindex')).toBe('-1')
    expect(bar.querySelectorAll('button:not([tabindex="-1"])')).toHaveLength(1)
    const toolbar = bar.querySelector('[role="toolbar"]')!
    const collapsed = panel.classList.contains('collapsed')
    const panelText = panel.textContent
    fireEvent.keyDown(toolbar, { key: 'End' })
    expect(document.activeElement).toBe(indicator)
    fireEvent.keyDown(toolbar, { key: 'Enter' })
    expect(onActivate).toHaveBeenCalledTimes(1)
    expect(panel.classList.contains('collapsed')).toBe(collapsed)
    expect(panel.textContent).toBe(panelText)
    handle.destroy()
  })

  it('shows an available update beside Settings and exposes one Install control when activated', () => {
    const bar = document.createElement('div')
    const panel = document.createElement('div')
    document.body.append(bar, panel)
    const [available] = createSignal(true)
    const controller = createUpdateController({ apply: vi.fn() })
    controller.showAvailable('2.0.0', 'https://example.com/releases/2.0.0')
    render(() => <ToastHost />)
    mountSidebar(bar, panel, views, [settings], null, undefined, undefined, undefined, {
      title: () => 'Update available',
      icon: TestIcon,
      level: () => 'neutral',
      available,
      onActivate: controller.showOffer,
    })

    const bottomZone = bar.querySelector('[aria-label="Actions"]')
    const indicator = bottomZone?.querySelector<HTMLButtonElement>('[data-update-indicator]')
    const settingsButton = bottomZone?.querySelector<HTMLButtonElement>('[data-action="settings"]')
    expect(indicator).not.toBeNull()
    expect(settingsButton).not.toBeNull()
    expect(
      settingsButton!.compareDocumentPosition(indicator!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    fireEvent.click(indicator!)
    expect(document.body.querySelectorAll('button').length).toBeGreaterThan(2)
    expect(
      [...document.body.querySelectorAll('button')].filter(
        (button) => button.textContent?.trim() === 'Install',
      ),
    ).toHaveLength(1)
  })
})
