// @vitest-environment jsdom
// FloatingPanel — the shared floating-panel primitive (ui/README table):
// the surface shell both the completion dropdown and the recall overlay are
// variants of. Owns anchoring, content-sized width between a floor and a
// ceiling (measured once per list, never per selection change), max-height
// and scrolling, the row list, the group caption, the footer of key hints,
// the match highlight, and row overflow (ellipsis, never a clipped glyph).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  FloatingPanel,
  MIN_PANEL_WIDTH_PX,
  MAX_PANEL_WIDTH_PX,
  type FloatingPanelRow,
  type FloatingPanelVariant,
} from './floating-panel'

const row = (over: Partial<FloatingPanelRow> & { id: string }): FloatingPanelRow => ({
  displayText: over.id,
  matchRanges: [],
  ...over,
})

/** The panel mounted in a sized container, plus the stylesheet the real
 *  component CSS supplies. The overflow/ellipsis assertions read COMPUTED
 *  styles, so the test injects the component's rules — jsdom does not load
 *  the project's CSS. (jsdom computes longhands but never resolves var(),
 *  so the match-contrast proof lives in the theme-catalogue test below and
 *  the e2e computed-style assertion — see match-contrast.test.ts.) */
const mount = (variant: FloatingPanelVariant = 'completion') => {
  const container = document.createElement('div')
  Object.defineProperty(container, 'clientWidth', { value: 1200 })
  document.body.appendChild(container)
  const onHover = vi.fn()
  const onPick = vi.fn()
  const panel = new FloatingPanel({
    variant,
    role: 'listbox',
    ariaLabel: 'test',
    callbacks: { onHover, onPick },
  })
  panel.mount(container)
  return { panel, container, onHover, onPick }
}

const style = () => {
  const el = document.createElement('style')
  el.textContent = `
    .ui-floating-panel__row {
      white-space: nowrap;
    }
    .ui-floating-panel__row .ui-collection-row__info {
      overflow: hidden;
      text-overflow: ellipsis;
    }
  `
  document.head.appendChild(el)
}

beforeEach(() => {
  style()
})
afterEach(() => {
  document.head.querySelectorAll('style').forEach((s) => s.remove())
  document.body.replaceChildren()
})

/** jsdom reports scrollWidth 0, so fake it on the prototype — each show()
 *  mints a fresh list element, so a per-element property would be lost.
 *  offsetWidth is faked to the same value: the clamp reads the panel's
 *  laid-out width, which jsdom never computes. */
const withGeometry = <T>(scroll: number, offset: number, fn: () => T): T => {
  const proto = HTMLElement.prototype
  Object.defineProperty(proto, 'scrollWidth', { configurable: true, get: () => scroll })
  Object.defineProperty(proto, 'offsetWidth', { configurable: true, get: () => offset })
  try {
    return fn()
  } finally {
    delete (proto as { scrollWidth?: number }).scrollWidth
    delete (proto as { offsetWidth?: number }).offsetWidth
  }
}
const selected = (panel: FloatingPanel) =>
  panel.root.querySelector('.ui-floating-panel__row[data-selected="true"]')

describe('FloatingPanel', () => {
  it('starts closed with no rows', () => {
    const { panel } = mount()
    expect(panel.isOpen).toBe(false)
    expect(panel.root.dataset.open).toBe('false')
    expect(panel.root.dataset.variant).toBe('completion')
    expect(panel.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(0)
  })

  it('renders one row per row, with the selected variance on the selected index', () => {
    const { panel } = mount()
    panel.show({
      rows: [row({ id: 'a' }), row({ id: 'b' }), row({ id: 'c' })],
      selectedIndex: 1,
    })
    const rows = panel.root.querySelectorAll<HTMLElement>('.ui-floating-panel__row')
    expect(rows).toHaveLength(3)
    expect(rows[0].getAttribute('aria-selected')).toBe('false')
    expect(rows[1].getAttribute('aria-selected')).toBe('true')
    expect(rows[1].dataset.selected).toBe('true')
    expect(rows[2].getAttribute('aria-selected')).toBe('false')
    expect(selected(panel)?.textContent).toContain('b')
  })

  it('shows displayText with the matched ranges as marks', () => {
    const { panel } = mount()
    panel.show({
      rows: [row({ id: 'repos/', displayText: 'repos/', matchRanges: [{ from: 0, to: 2 }] })],
      selectedIndex: 0,
    })
    const info = panel.root.querySelector('.ui-collection-row__info')
    const mark = panel.root.querySelector('.ui-floating-panel__match')
    expect(mark).not.toBeNull()
    expect(mark?.textContent).toBe('re')
    expect(info?.textContent).toBe('repos/')
  })

  it('places the variant actions at the row end', () => {
    const { panel } = mount()
    const badge = document.createElement('span')
    badge.className = 'ui-badge'
    badge.textContent = 'Directory'
    panel.show({ rows: [row({ id: 'x', actions: [badge] })], selectedIndex: 0 })
    const actions = panel.root.querySelector('.ui-collection-row__actions')
    expect(actions?.textContent).toBe('Directory')
  })

  it('renders the group caption when the section changes between rows', () => {
    const { panel } = mount()
    panel.show({
      rows: [
        row({ id: 'repos/' }),
        row({ id: 'cd /old', group: 'History' }),
        row({ id: 'cd /newer', group: 'History' }),
      ],
      selectedIndex: 0,
    })
    const groups = panel.root.querySelectorAll('.ui-floating-panel__group')
    expect(groups).toHaveLength(1)
    expect(groups[0].textContent).toBe('History')
  })

  it('renders no group caption when the variant stamps no sections', () => {
    // The variant DECIDES sections: a pure-history completion list stamps no
    // groups (there is nothing to separate from), so the primitive renders
    // no caption.
    const { panel } = mount()
    panel.show({
      rows: [row({ id: 'a' }), row({ id: 'b' })],
      selectedIndex: 0,
    })
    expect(panel.root.querySelectorAll('.ui-floating-panel__group')).toHaveLength(0)
  })

  it('renders the footer of key hints, one span per hint', () => {
    const { panel } = mount()
    panel.show({
      rows: [row({ id: 'a' })],
      selectedIndex: 0,
      footer: ['↵ to insert', 'tab ↹ to cycle', 'esc to dismiss'],
    })
    const footer = panel.root.querySelector('.ui-floating-panel__footer')
    expect(footer).not.toBeNull()
    const hints = footer?.querySelectorAll('span')
    expect(hints).toHaveLength(3)
    expect(hints?.[0].textContent).toBe('↵ to insert')
  })

  it('places variant sections between the list and the footer', () => {
    const { panel } = mount()
    const header = document.createElement('div')
    header.className = 'ui-floating-panel__header'
    header.textContent = 'history'
    const detail = document.createElement('div')
    detail.className = 'ui-floating-panel__detail'
    detail.textContent = 'exit code 0'
    panel.show({
      rows: [row({ id: 'a' })],
      selectedIndex: 0,
      before: [header],
      after: [detail],
      footer: ['esc to dismiss'],
    })
    const children = [...panel.root.children].map((c) => c.className)
    expect(children).toEqual([
      'ui-floating-panel__header',
      'ui-floating-panel__list',
      'ui-floating-panel__detail',
      'ui-floating-panel__footer',
    ])
  })

  it('showEmpty renders one non-selectable row and no footer', () => {
    const { panel } = mount()
    panel.showEmpty('No matches')
    const rows = panel.root.querySelectorAll<HTMLElement>('.ui-floating-panel__row')
    expect(rows).toHaveLength(1)
    expect(rows[0].dataset.empty).toBe('true')
    expect(rows[0].getAttribute('aria-disabled')).toBe('true')
    expect(rows[0].getAttribute('aria-selected')).toBe('false')
    expect(panel.root.querySelector('.ui-floating-panel__footer')).toBeNull()
  })

  it('hide closes the panel and clears its rows', () => {
    const { panel } = mount()
    panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 })
    expect(panel.isOpen).toBe(true)
    panel.hide()
    expect(panel.isOpen).toBe(false)
    expect(panel.root.dataset.open).toBe('false')
    expect(panel.root.querySelectorAll('.ui-floating-panel__row')).toHaveLength(0)
  })
  it('supports optional dismissal without affecting unconfigured panels', () => {
    const container = document.createElement('div')
    const boundary = document.createElement('button')
    container.appendChild(boundary)
    document.body.appendChild(container)
    const onDismiss = vi.fn()
    const panel = new FloatingPanel({
      variant: 'grant',
      role: 'listbox',
      ariaLabel: 'test',
      dismissBoundary: boundary,
      callbacks: { onDismiss },
    })
    panel.mount(container)
    const unconfigured = new FloatingPanel({
      variant: 'completion',
      role: 'listbox',
      ariaLabel: 'unconfigured',
    })
    unconfigured.mount(container)
    unconfigured.show({ rows: [row({ id: 'unconfigured' })], selectedIndex: 0 })

    const unconfiguredEscape = new KeyboardEvent('keydown', {
      key: 'Escape',
      bubbles: true,
      cancelable: true,
    })
    document.dispatchEvent(unconfiguredEscape)
    expect(unconfiguredEscape.defaultPrevented).toBe(false)
    unconfigured.hide()

    panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 })

    const escape = new KeyboardEvent('keydown', {
      key: 'Escape',
      bubbles: true,
      cancelable: true,
    })
    document.dispatchEvent(escape)
    expect(escape.defaultPrevented).toBe(true)
    expect(onDismiss).toHaveBeenLastCalledWith('escape')

    boundary.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    panel.root.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onDismiss).toHaveBeenCalledTimes(1)

    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onDismiss).toHaveBeenLastCalledWith('outside')
    expect(onDismiss).toHaveBeenCalledTimes(2)

    panel.hide()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onDismiss).toHaveBeenCalledTimes(2)
  })

  it('reports hover and pick with the row index', () => {
    const { panel, onHover, onPick } = mount()
    panel.show({ rows: [row({ id: 'a' }), row({ id: 'b' })], selectedIndex: 0 })
    const rows = panel.root.querySelectorAll<HTMLElement>('.ui-floating-panel__row')
    rows[1].dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }))
    expect(onHover).toHaveBeenCalledWith(1)
    rows[1].dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
    expect(onPick).toHaveBeenCalledWith(1)
  })

  // ── the sizing rules the two variants must SHARE (report 5) ──────────

  it('sizes to the widest row, floored and capped — never the pane width', () => {
    const { panel } = mount()
    withGeometry(0, 0, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    // The floor applies to a tiny (here, unmeasurable) list. It is per
    // variant — recall is a browsing surface and must not collapse to the
    // completion's floor — while the RULE (hug the content between a floor
    // and the ceiling) is the one both share.
    expect(panel.root.style.width).toBe(`${MIN_PANEL_WIDTH_PX.completion}px`)
    panel.hide()

    withGeometry(500, 500, () => {
      panel.show({ rows: [row({ id: 'a' }), row({ id: 'widest' })], selectedIndex: 0 })
      // The panel follows its widest content…
      expect(panel.root.style.width).toBe('500px')
    })
    panel.hide()

    withGeometry(5000, 5000, () =>
      panel.show({
        rows: [row({ id: 'endless', displayText: 'x'.repeat(300) })],
        selectedIndex: 0,
      }),
    )
    // …but never past the ceiling.
    expect(panel.root.style.width).toBe(`${MAX_PANEL_WIDTH_PX}px`)
  })

  it('recall does not collapse to the completion floor: an empty history is still a panel', () => {
    // What the owner saw: recall open on "no history yet", sized to rows it
    // did not have, rendered as a narrow column beside a full-width pane.
    const { panel } = mount('recall')
    withGeometry(0, 0, () => panel.show({ rows: [], selectedIndex: -1 }))
    expect(panel.root.style.width).toBe(`${MIN_PANEL_WIDTH_PX.recall}px`)
    expect(MIN_PANEL_WIDTH_PX.recall).toBeGreaterThan(MIN_PANEL_WIDTH_PX.completion)
    // The ceiling is still shared — a higher floor is not a second rule.
    panel.hide()
    withGeometry(5000, 5000, () =>
      panel.show({
        rows: [row({ id: 'endless', displayText: 'x'.repeat(300) })],
        selectedIndex: 0,
      }),
    )
    expect(panel.root.style.width).toBe(`${MAX_PANEL_WIDTH_PX}px`)
  })

  it('the width is stable for the life of one open list — a selection change never re-measures', () => {
    const { panel } = mount()
    withGeometry(520, 520, () => {
      panel.show({ rows: [row({ id: 'alpha' }), row({ id: 'b' })], selectedIndex: 0 })
      expect(panel.root.style.width).toBe('520px')
      // The selection moves to the SHORT row — the panel must not shrink
      // under the cursor (the owner's "every Tab press makes the window
      // narrower"): the list content is unchanged, so the width stays.
      panel.show({ rows: [row({ id: 'alpha' }), row({ id: 'b' })], selectedIndex: 1 })
      expect(panel.root.style.width).toBe('520px')
      // A list whose content CHANGES re-measures (a late batch widening in).
      panel.show({
        rows: [row({ id: 'alpha' }), row({ id: 'b' }), row({ id: 'gamma' })],
        selectedIndex: 1,
      })
      expect(panel.root.style.width).toBe('520px')
    })
    // hide() clears the cache: a fresh list measures fresh.
    panel.hide()
    withGeometry(400, 400, () => {
      panel.show({ rows: [row({ id: 'delta' })], selectedIndex: 0 })
      expect(panel.root.style.width).toBe('400px')
    })
  })

  it('anchors at the caret, clamped inside the editor', () => {
    const { panel } = mount()
    withGeometry(320, 320, () => {
      panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0, anchorLeft: 700 })
      // 700 fits: 1200 - 320 floor.
      expect(panel.root.style.left).toBe('700px')
      // The panel never runs off the editor's right edge: clamped to
      // parentWidth - width.
      panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0, anchorLeft: 2000 })
      expect(panel.root.style.left).toBe('880px')
      // No anchor: the kit's left-edge default.
      panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0, anchorLeft: null })
      expect(panel.root.style.left).toBe('')
    })
  })

  // ── report 4: a row longer than the ceiling ellipsises, never clips ───

  it('a row too long for the ceiling ellipsises — never a clipped glyph', () => {
    const { panel } = mount()
    withGeometry(5000, 5000, () =>
      panel.show({
        rows: [
          row({
            id: 'long',
            displayText: 'repos/meshynet/' + 'x'.repeat(120),
          }),
        ],
        selectedIndex: 0,
      }),
    )
    const info = panel.root.querySelector<HTMLElement>('.ui-collection-row__info')!
    const rowEl = panel.root.querySelector<HTMLElement>('.ui-floating-panel__row')!
    const infoStyle = getComputedStyle(info)
    expect(infoStyle.overflow).toBe('hidden')
    expect(infoStyle.textOverflow).toBe('ellipsis')
    expect(getComputedStyle(rowEl).whiteSpace).toBe('nowrap')
    // The row is capped: the panel never exceeds the ceiling to fit it.
    expect(panel.root.style.width).toBe(`${MAX_PANEL_WIDTH_PX}px`)
  })
})

/** A field at a known place in the viewport, and the anchored panel that
 *  opens against it.
 *
 *  jsdom lays nothing out: every rect is zero and every element reports
 *  itself visible, so `toBeVisible()` on this panel proves nothing at all
 *  (it passed for the whole time the panel was opening above the top of the
 *  window). What CAN be tested here is the arithmetic — feed the component a
 *  field rect and a panel size and assert the top and left it computes. The
 *  proof that the numbers correspond to a real screen is the e2e spec
 *  (e2e/secret-panel-position.spec.ts), which reads the live rect. */
const anchoredMount = (rect: { top: number; bottom: number; left: number }) => {
  const field = document.createElement('input')
  document.body.appendChild(field)
  field.getBoundingClientRect = (): DOMRect => ({
    top: rect.top,
    bottom: rect.bottom,
    left: rect.left,
    right: rect.left,
    width: 0,
    height: rect.bottom - rect.top,
    x: rect.left,
    y: rect.top,
    toJSON: () => ({}),
  })
  const container = document.createElement('div')
  Object.defineProperty(container, 'clientWidth', { value: 1200 })
  document.body.appendChild(container)
  const panel = new FloatingPanel({
    variant: 'secret',
    role: 'listbox',
    ariaLabel: 'test',
    anchor: () => field,
  })
  panel.mount(container)
  return { panel, field, container }
}

/** The laid-out size jsdom never computes. Height is what the vertical
 *  placement reads; width is the horizontal clamp's, as above. */
const withSize = <T>(width: number, height: number, fn: () => T): T => {
  const proto = HTMLElement.prototype
  Object.defineProperty(proto, 'scrollWidth', { configurable: true, get: () => width })
  Object.defineProperty(proto, 'offsetWidth', { configurable: true, get: () => width })
  Object.defineProperty(proto, 'offsetHeight', { configurable: true, get: () => height })
  try {
    return fn()
  } finally {
    delete (proto as { scrollWidth?: number }).scrollWidth
    delete (proto as { offsetWidth?: number }).offsetWidth
    delete (proto as { offsetHeight?: number }).offsetHeight
  }
}

/** The viewport the placement is computed against. jsdom's default is
 *  1024x768; naming it makes the expected numbers readable. */
const viewport = (width: number, height: number): void => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: width })
  Object.defineProperty(window, 'innerHeight', { configurable: true, value: height })
}

describe('FloatingPanel anchored to a field (nocx-vzdna)', () => {
  beforeEach(() => viewport(1024, 768))

  it('declares the anchored variance, and the editor-mounted panel does not', () => {
    const { panel } = anchoredMount({ top: 400, bottom: 424, left: 100 })
    expect(panel.root.dataset.anchor).toBe('viewport')
    expect(mount().panel.root.dataset.anchor).toBeUndefined()
  })

  it('opens INSIDE the viewport, above the field it belongs to', () => {
    const { panel } = anchoredMount({ top: 400, bottom: 424, left: 100 })
    withSize(480, 200, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    // 400 (the field's top) - 6 (the gap) - 200 (the panel) = 194.
    const top = Number.parseFloat(panel.root.style.top)
    expect(top).toBe(194)
    // Criterion 1, as geometry: the whole panel is on screen.
    expect(top).toBeGreaterThanOrEqual(0)
    expect(top + 200).toBeLessThanOrEqual(768)
    expect(panel.root.style.left).toBe('100px')
  })

  it('is anchored to ITS OWN field: two fields at different offsets open at different heights', () => {
    const near = anchoredMount({ top: 80, bottom: 104, left: 40 })
    const far = anchoredMount({ top: 600, bottom: 624, left: 240 })
    withSize(480, 40, () => {
      near.panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 })
      far.panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 })
    })
    expect(near.panel.root.style.top).toBe('34px') // 80 - 6 - 40
    expect(far.panel.root.style.top).toBe('554px') // 600 - 6 - 40
    expect(near.panel.root.style.left).toBe('40px')
    expect(far.panel.root.style.left).toBe('240px')
  })

  it('flips BELOW the field when the panel does not fit above it', () => {
    const { panel } = anchoredMount({ top: 40, bottom: 64, left: 100 })
    withSize(480, 200, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    // 40 - 6 - 200 is negative — off the top of the window, which is the
    // defect itself. Below the field instead: 64 + 6.
    expect(panel.root.style.top).toBe('70px')
  })

  it('clamps into the window when the panel fits neither above nor below', () => {
    const { panel } = anchoredMount({ top: 100, bottom: 700, left: 100 })
    withSize(480, 300, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    // Below would be 706, putting 238px of a 300px panel off the bottom.
    // Clamped to innerHeight - height.
    const top = Number.parseFloat(panel.root.style.top)
    expect(top).toBe(468)
    expect(top).toBeGreaterThanOrEqual(0)
    expect(top + 300).toBeLessThanOrEqual(768)
  })

  it('never runs off the right edge — the anchorLeft guard, read against the window', () => {
    const { panel } = anchoredMount({ top: 400, bottom: 424, left: 900 })
    withSize(480, 100, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    // 900 + 480 would be 1380 against a 1024 window: clamped to 1024 - 480.
    expect(panel.root.style.left).toBe('544px')
  })

  it('follows its field when the page scrolls under an open panel', () => {
    const { panel, field } = anchoredMount({ top: 400, bottom: 424, left: 100 })
    withSize(480, 100, () => {
      panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 })
      expect(panel.root.style.top).toBe('294px')
      field.getBoundingClientRect = () =>
        ({ top: 300, bottom: 324, left: 100, height: 24 }) as DOMRect
      document.dispatchEvent(new Event('scroll'))
      expect(panel.root.style.top).toBe('194px')
    })
  })

  it('stops following once closed', () => {
    const { panel, field } = anchoredMount({ top: 400, bottom: 424, left: 100 })
    withSize(480, 100, () => {
      panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 })
      panel.hide()
      field.getBoundingClientRect = () =>
        ({ top: 300, bottom: 324, left: 100, height: 24 }) as DOMRect
      document.dispatchEvent(new Event('scroll'))
      expect(panel.root.style.top).toBe('294px')
    })
  })

  it('places itself somewhere visible when its field has gone', () => {
    const field = document.createElement('input')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const panel = new FloatingPanel({
      variant: 'secret',
      role: 'listbox',
      ariaLabel: 'test',
      anchor: () => (field.isConnected ? field : null),
    })
    panel.mount(container)
    withSize(480, 100, () => panel.showEmpty('loading secrets…'))
    expect(panel.root.style.top).toBe('0px')
    expect(panel.root.style.left).toBe('0px')
  })

  it('moves into the open dialog its field lives in — the top layer, not the body', () => {
    const dialog = document.createElement('dialog')
    dialog.setAttribute('open', '')
    document.body.appendChild(dialog)
    const field = document.createElement('input')
    dialog.appendChild(field)
    const body = document.createElement('div')
    document.body.appendChild(body)
    const panel = new FloatingPanel({
      variant: 'secret',
      role: 'listbox',
      ariaLabel: 'test',
      anchor: () => field,
    })
    panel.mount(body)
    expect(panel.root.parentElement).toBe(body)
    withSize(480, 100, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    // A native modal <dialog> paints in the browser's top layer, outside
    // every stacking context: a panel left on the body would be behind the
    // dialog's own scrim, which is the same defect as being off-screen.
    expect(panel.root.parentElement).toBe(dialog)
    // And back, once the field is not in a dialog any more.
    dialog.removeAttribute('open')
    withSize(480, 100, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    expect(panel.root.parentElement).toBe(body)
  })

  it('leaves the editor-mounted panel entirely to CSS — the terminal path is untouched', () => {
    // prompt-vault.ts mounts into editor.root (position: relative), where
    // `bottom: 100%` already means "directly above the prompt". A panel with
    // no anchor must therefore compute NO top at all: an inline top would
    // silently move the terminal's panel.
    const { panel } = mount('secret')
    withSize(480, 100, () => panel.show({ rows: [row({ id: 'a' })], selectedIndex: 0 }))
    expect(panel.root.style.top).toBe('')
    expect(panel.root.style.position).toBe('')
    expect(panel.root.dataset.anchor).toBeUndefined()
  })
})
