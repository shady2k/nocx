// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { ClipboardBannerImpl } from './banner'

// The real banner implementation. The gate-policy tests in tabs.test.ts use a
// fake banner, so nothing exercised this class — which is how a latched
// `shown` flag shipped, silently turning dismiss into "never show again".

const PANES = 'panes'

function mountPanes(): HTMLElement {
  const panes = document.createElement('div')
  panes.id = PANES
  document.body.append(panes)
  return panes
}

function click(sel: string): void {
  const el = document.querySelector<HTMLElement>(sel)
  if (!el) throw new Error(`no element for ${sel}`)
  el.click()
}

describe('ClipboardBannerImpl', () => {
  let panes: HTMLElement

  beforeEach(() => {
    panes = mountPanes()
  })

  afterEach(() => {
    document.body.replaceChildren()
  })

  it('reports shown only while a banner is on screen', async () => {
    const banner = new ClipboardBannerImpl()
    expect(banner.shown).toBe(false)

    const choice = banner.show()
    expect(banner.shown).toBe(true)
    expect(panes.querySelector('.clipboard-banner')).not.toBeNull()

    click('.clipboard-banner-dismiss')
    await choice

    expect(banner.shown).toBe(false)
    expect(panes.querySelector('.clipboard-banner')).toBeNull()
  })

  it('asks again after a dismiss — dismiss is "not now", not "never"', async () => {
    const banner = new ClipboardBannerImpl()

    const first = banner.show()
    click('.clipboard-banner-dismiss')
    expect(await first).toBe('dismiss')

    // The next blocked write must raise a real banner again. While `shown`
    // stayed latched, this second call resolved immediately with 'dismiss'
    // and rendered nothing, so the user could never reach the allow action.
    const second = banner.show()
    expect(banner.shown).toBe(true)
    expect(panes.querySelector('.clipboard-banner')).not.toBeNull()

    click('.clipboard-banner-allow')
    expect(await second).toBe('allow')
  })

  it('does not stack a second banner while one is on screen', async () => {
    const banner = new ClipboardBannerImpl()

    const first = banner.show()
    // A program emitting OSC 52 in a loop hits this path.
    expect(await banner.show()).toBe('dismiss')
    expect(panes.querySelectorAll('.clipboard-banner')).toHaveLength(1)

    click('.clipboard-banner-suppress')
    expect(await first).toBe('suppress')
  })
})
