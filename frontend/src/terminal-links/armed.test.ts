// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { trackLinkModifier } from './armed'

function key(type: 'keydown' | 'keyup', init: KeyboardEventInit): void {
  window.dispatchEvent(new KeyboardEvent(type, init))
}

describe('trackLinkModifier', () => {
  it('starts disarmed', () => {
    const t = trackLinkModifier()
    expect(t.armed()).toBe(false)
    t.dispose()
  })

  it('arms on either modifier and disarms on release', () => {
    const t = trackLinkModifier()
    key('keydown', { key: 'Meta', metaKey: true })
    expect(t.armed()).toBe(true)
    key('keyup', { key: 'Meta', metaKey: false })
    expect(t.armed()).toBe(false)
    key('keydown', { key: 'Control', ctrlKey: true })
    expect(t.armed()).toBe(true)
    t.dispose()
  })

  it('arms on a chord that began before the pointer arrived', () => {
    // ⌘ was already down; the next key seen is `d`, not `Meta`.
    const t = trackLinkModifier()
    key('keydown', { key: 'd', metaKey: true })
    expect(t.armed()).toBe(true)
    t.dispose()
  })

  it('disarms when the window loses focus mid-chord', () => {
    const t = trackLinkModifier()
    key('keydown', { key: 'Meta', metaKey: true })
    window.dispatchEvent(new Event('blur'))
    expect(t.armed()).toBe(false)
    t.dispose()
  })

  it('notifies subscribers on transitions only', () => {
    const t = trackLinkModifier()
    const seen: boolean[] = []
    t.subscribe((a) => seen.push(a))
    key('keydown', { key: 'Meta', metaKey: true })
    key('keydown', { key: 'a', metaKey: true })
    key('keyup', { key: 'Meta', metaKey: false })
    expect(seen).toEqual([true, false])
    t.dispose()
  })

  it('stops listening after dispose', () => {
    const t = trackLinkModifier()
    t.dispose()
    key('keydown', { key: 'Meta', metaKey: true })
    expect(t.armed()).toBe(false)
  })
})
