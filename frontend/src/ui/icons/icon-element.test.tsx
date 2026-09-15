// @vitest-environment jsdom
//
// The bug this reproduces (nocx-9bpeq.12 round 3): a kit icon called bare
// (`Icon({})`, the vanilla-emitted pattern this file's `iconElement` now
// owns) returns solid-refresh's HMR-proxy accessor instead of an element
// once that SAME icon has ALSO been rendered through Solid's own pipeline
// (`createComponent` — what a computed JSX tag like a ContextMenuItem's
// `<item.icon />` compiles to) at least once in this module's lifetime. See
// `icon-element.ts`'s header for the full mechanism (`$DEVCOMP`,
// `solid-refresh`'s `createProxy`).
//
// `TestIcon` must be declared at this FILE's top level, shaped like a real
// kit icon (a static `<svg>`, PascalCase name), so vite-plugin-solid's dev
// transform wraps it the same way it wraps every icon in `ui/icons/` —
// the wrapping this test is actually exercising, not a stand-in for it.
import { describe, expect, it } from 'vitest'
import { createComponent, createRoot, type Component } from 'solid-js'
import { iconElement } from './icon-element'

const TestIcon: Component = () => (
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <rect width="18" height="18" x="3" y="3" />
  </svg>
)

describe('iconElement', () => {
  it('resolves an icon nobody has rendered through Solid yet', () => {
    const el = iconElement(TestIcon)
    expect(el).toBeInstanceOf(SVGElement)
    expect(el.tagName.toLowerCase()).toBe('svg')
  })

  it('still resolves a real element after the SAME icon has been rendered through createComponent — the ambient state a menu item’s `icon: TestIcon` leaves behind', () => {
    // Reproduce the poisoning directly: this is what rendering
    // `<item.icon />` inside a real Solid tree does to the underlying
    // function, once, the first time. No menu, no ContextMenu — the same
    // mechanism, isolated.
    createRoot((dispose) => {
      createComponent(TestIcon, {})
      dispose()
    })

    const el = iconElement(TestIcon)
    expect(el).toBeInstanceOf(SVGElement)
    expect(el.tagName.toLowerCase()).toBe('svg')
  })

  it('throws rather than hand back whatever a broken icon produced', () => {
    const notAnIcon = (() => 'not an element') as unknown as Component
    expect(() => iconElement(notAnIcon)).toThrow()
  })

  it('disposes its own root — nothing it creates outlives the call', () => {
    // A regression guard for the fix's OWN mechanism: if iconElement ever
    // stopped disposing its root, a poisoned icon's memo would leak one
    // computation per call, which is the exact defect class this fix
    // exists to close, just moved one level down.
    createRoot((dispose) => {
      createComponent(TestIcon, {})
      dispose()
    })
    expect(() => {
      for (let i = 0; i < 5; i++) iconElement(TestIcon)
    }).not.toThrow()
  })
})
