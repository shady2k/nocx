// @vitest-environment jsdom
//
// What a person can do, through the seam they actually reach: hold ⌘, click
// the underlined path in a frozen block, and the file opens — while a plain
// click still selects, because copy-on-select is what that gesture already
// means here.
import { describe, expect, it } from 'vitest'
import { attachLinkClicks, ARMED_CLASS } from './surface'
import { decorateLinks } from './decorate'
import { trackLinkModifier } from './armed'
import type { LinkOpener } from './open'
import type { LinkTarget } from './detect'
import type { ActiveOrigin } from '../pane-content'

const origin: Omit<ActiveOrigin, 'paneId'> = {
  sessionId: 's1',
  kind: 'local',
  cwd: '/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

function setup(html = 'see docs/architecture.md:101 now') {
  const root = document.createElement('div')
  root.innerHTML = `<span class="term-line">${html}</span>`
  document.body.append(root)
  decorateLinks(root)
  const opened: LinkTarget[] = []
  const opener: LinkOpener = {
    open: (t) => {
      opened.push(t)
      return Promise.resolve()
    },
  }
  const armed = trackLinkModifier()
  const detach = attachLinkClicks(root, { opener, origin: () => origin, armed })
  const link = root.querySelector('a') as HTMLElement
  return { root, link, opened, detach, armed }
}

function click(el: HTMLElement, init: MouseEventInit = {}): MouseEvent {
  const e = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init })
  el.dispatchEvent(e)
  return e
}

describe('attachLinkClicks', () => {
  it('opens the link on ⌘-click', () => {
    const { link, opened, detach } = setup()
    click(link, { metaKey: true })
    expect(opened).toEqual([{ kind: 'path', path: 'docs/architecture.md', line: 101 }])
    detach()
  })

  it('opens on ⌃-click too', () => {
    const { link, opened, detach } = setup()
    click(link, { ctrlKey: true })
    expect(opened).toHaveLength(1)
    detach()
  })

  it('leaves a plain click to the selection, which already owns it', () => {
    const { link, opened, detach } = setup()
    const e = click(link)
    expect(opened).toEqual([])
    expect(e.defaultPrevented).toBe(false)
    detach()
  })

  it('suppresses the selection gesture on an armed click', () => {
    // Selection starts on mousedown; without this the file opens AND the
    // word lands on the clipboard.
    const { link, detach } = setup()
    const down = new MouseEvent('mousedown', {
      bubbles: true,
      cancelable: true,
      button: 0,
      metaKey: true,
    })
    link.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    detach()
  })

  it('ignores a click that is not on a link', () => {
    const { root, opened, detach } = setup()
    click(root.querySelector('.term-line') as HTMLElement, { metaKey: true })
    expect(opened).toEqual([])
    detach()
  })

  it('ignores a non-primary button', () => {
    const { link, opened, detach } = setup()
    click(link, { metaKey: true, button: 2 })
    expect(opened).toEqual([])
    detach()
  })

  it('marks the root while the modifier is held, so links look clickable', () => {
    const { root, detach } = setup()
    expect(root.classList.contains(ARMED_CLASS)).toBe(false)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Meta', metaKey: true }))
    expect(root.classList.contains(ARMED_CLASS)).toBe(true)
    window.dispatchEvent(new KeyboardEvent('keyup', { key: 'Meta', metaKey: false }))
    expect(root.classList.contains(ARMED_CLASS)).toBe(false)
    detach()
  })

  it('stops opening — and stops marking — once detached', () => {
    const { root, link, opened, detach } = setup()
    detach()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Meta', metaKey: true }))
    click(link, { metaKey: true })
    expect(opened).toEqual([])
    expect(root.classList.contains(ARMED_CLASS)).toBe(false)
  })

  it('reads the origin at click time, not at attach time', () => {
    const root = document.createElement('div')
    root.innerHTML = '<span class="term-line">a/b.ts:2</span>'
    decorateLinks(root)
    const seen: Array<Omit<ActiveOrigin, 'paneId'> | null> = []
    const opener: LinkOpener = {
      open: (_t, o) => {
        seen.push(o)
        return Promise.resolve()
      },
    }
    let current: Omit<ActiveOrigin, 'paneId'> | null = null
    const armed = trackLinkModifier()
    const detach = attachLinkClicks(root, { opener, origin: () => current, armed })
    current = { ...origin, cwd: '/moved' }
    click(root.querySelector('a') as HTMLElement, { metaKey: true })
    expect(seen[0]?.cwd).toBe('/moved')
    detach()
  })

  it('opens a url target from the same gesture', () => {
    const { link, opened, detach } = setup('open https://example.com/x')
    click(link, { metaKey: true })
    expect(opened).toEqual([{ kind: 'url', url: 'https://example.com/x' }])
    detach()
  })
})
