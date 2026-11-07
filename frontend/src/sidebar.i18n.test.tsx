// @vitest-environment jsdom
/**
 * i18n tests for the activity bar's labels (nocx-dej6.3.8's review, defect 2).
 *
 * A descriptor's title and the bar's count suffix are resolved when they are
 * READ, not when the shell builds them: the sidebar is mounted per connection
 * and outlives a language switch, so a string captured at mount was the
 * language the window started in — the header, the rail's accessible name and
 * "Notifications — 3 running" all kept it. The count phrase is a catalogue
 * plural, so Russian agrees with the number instead of stapling it to a fixed
 * word.
 */
import { describe, it, expect, afterEach, beforeEach } from 'vitest'
import type { Component } from 'solid-js'
import { mountSidebar, type SidebarViewDescriptor, type SidebarViewStatus } from './sidebar'
import { setActiveLocale, t } from './i18n/locale-model'

const TestIcon: Component = () => <svg />

function views(count: number): SidebarViewDescriptor[] {
  return [
    {
      id: 'notifications',
      title: () => t('app.page.notifications', 'Notifications'),
      icon: TestIcon,
      view: () => <div />,
      order: 0,
      status: (): SidebarViewStatus => ({ count, kind: 'unread', progress: null }),
    },
  ]
}

function mount(count: number) {
  const bar = document.body.appendChild(document.createElement('div'))
  const panel = document.body.appendChild(document.createElement('div'))
  mountSidebar(bar, panel, views(count), [])
  return bar
}

const label = (bar: HTMLElement) =>
  bar.querySelector('button[data-view="notifications"]')?.getAttribute('aria-label')

beforeEach(() => setActiveLocale('en'))
afterEach(() => {
  document.body.innerHTML = ''
  setActiveLocale('en')
})

describe('the rail says its names in the active language', () => {
  it('re-reads the title when the language switches, with a mounted bar', () => {
    setActiveLocale('en')
    const bar = mount(0)
    expect(label(bar)).toBe('Notifications')
    setActiveLocale('ru')
    expect(label(bar)).toBe('Уведомления')
  })

  it('agrees with the count in Russian, and says the number in the active language', () => {
    setActiveLocale('ru')
    const one = mount(1)
    expect(label(one)).toBe('Уведомления — 1 непрочитанное')
    const many = mount(3)
    expect(label(many)).toBe('Уведомления — 3 непрочитанных')
  })
})
