// @vitest-environment jsdom
//
// ModeIndicator (ui/README table) — the kit's badge wearing the operable
// target-switch variance: what a submit reaches, and the person's one
// explicit switch (ADR-0004 §3, nocx-4ff.7). The component is
// target-agnostic: word, tone and the menu's rows are inputs, never a
// lookup here.
//
// Since nocx-9bpeq.15 (spec 2026-09-15 §6) a click no longer switches
// directly — it opens the kit ContextMenu, a render island. Most of these
// tests therefore assert through the DOM the render island actually
// produces (role="menu", role="menuitem"), the same seam
// scrollback/blocks.test.ts already asserts the block-actions menu through.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { createModeIndicator } from './mode-indicator'

const RUN_ASK_ITEMS = [
  { targetId: 'shell', word: 'Run' },
  { targetId: 'agent', word: 'Ask' },
]

describe('createModeIndicator', () => {
  // The menu is a Solid render island portalled into document.body and
  // stays open until dismissed (scrollback/blocks.test.ts's block-actions
  // menu carries the same rule, verbatim): a test that opens one and ends
  // without picking a row or clicking again must still close it, or the
  // NEXT test's querySelectorAll('[role="menuitem"]') finds THIS menu's
  // rows first (they share one body-level query) and clicks an item wired
  // to a dead test's onSelect. Escape closes it through the component
  // itself — removing the portalled node by hand would leave its Solid
  // root, and the document listeners it owns, alive.
  afterEach(() => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  })

  it('is the kit badge with a stable identity and the typed tone variance', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    expect(el.tagName).toBe('BUTTON')
    expect(el.type).toBe('button')
    expect(el.classList.contains('ui-badge')).toBe(true)
    expect(el.classList.contains('ui-mode-indicator')).toBe(true)
    expect(el.dataset.tone).toBe('neutral')
    // The word is the only text — the chevron is an aria-hidden svg and the
    // divider carries no character, so neither reaches textContent.
    expect(el.textContent).toBe('Run')
  })

  it('carries the registry’s target id as the data-target hook, never a derivation', () => {
    const el = createModeIndicator({
      word: 'Ask',
      tone: 'info',
      targetId: 'agent',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    expect(el.dataset.target).toBe('agent')
  })

  it('names what the control does in the aria-label, and marks itself a menu button', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    expect(el.getAttribute('aria-label')).toBe('Enter goes to Run. Click to choose.')
    expect(el.getAttribute('aria-haspopup')).toBe('menu')
    expect(el.getAttribute('aria-expanded')).toBe('false')
  })

  it('renders a trailing chevron, then a divider after the whole switch (round 18: "Run ⌄ │")', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    expect(el.querySelector('svg')).not.toBeNull()
    const divider = el.querySelector('.ui-mode-indicator__divider')
    expect(divider).not.toBeNull()
    expect(divider?.getAttribute('aria-hidden')).toBe('true')
    expect(divider?.textContent).toBe('')
    // The divider separates the SWITCH (word + chevron) from whatever sits
    // beside it (the composer's draft) — never the word from its own
    // chevron, which is what the earlier placement drew as a seam inside
    // the control (the owner's "divider inside the pill").
    const children = [...el.children]
    const dividerIndex = children.findIndex((c) =>
      c.classList.contains('ui-mode-indicator__divider'),
    )
    const chevronIndex = children.findIndex((c) =>
      c.classList.contains('ui-mode-indicator__chevron'),
    )
    expect(dividerIndex).toBeGreaterThan(chevronIndex)
  })

  it('a mousedown never moves the caret or steals the editor’s focus', () => {
    const el = createModeIndicator({
      word: 'Ask',
      tone: 'info',
      targetId: 'agent',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    const press = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    const prevented = !el.dispatchEvent(press)
    expect(prevented).toBe(true)
  })

  it('click opens a kit ContextMenu listing every item, the active one checked', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    document.body.appendChild(el)
    el.click()

    const menu = document.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    const items = [...document.querySelectorAll('[role="menuitem"]')]
    expect(items.map((i) => i.textContent)).toEqual(['Run', 'Ask'])
    expect(el.getAttribute('aria-expanded')).toBe('true')

    // The active row (shell/Run) wears the check icon in the icon column;
    // the inactive row's column is empty rather than reaching for a
    // placeholder that would read as a second action.
    const [runItem, askItem] = items
    expect(runItem.querySelector('.ui-context-menu__icon svg')).not.toBeNull()
    expect(askItem.querySelector('.ui-context-menu__icon svg')).toBeNull()

    el.remove()
  })

  it('picking a row fires onSelect with that row’s target id and closes the menu', () => {
    const onSelect = vi.fn()
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      items: RUN_ASK_ITEMS,
      onSelect,
    })
    document.body.appendChild(el)
    el.click()

    const ask = [...document.querySelectorAll('[role="menuitem"]')].find(
      (i) => i.textContent === 'Ask',
    ) as HTMLButtonElement
    ask.click()

    expect(onSelect).toHaveBeenCalledExactlyOnceWith('agent')
    expect(document.querySelector('[role="menu"]')).toBeNull()
    expect(el.getAttribute('aria-expanded')).toBe('false')

    el.remove()
  })

  it('a second click closes an already-open menu', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      items: RUN_ASK_ITEMS,
      onSelect: () => {},
    })
    document.body.appendChild(el)
    el.click()
    expect(document.querySelector('[role="menu"]')).not.toBeNull()
    el.click()
    expect(document.querySelector('[role="menu"]')).toBeNull()
    el.remove()
  })

  it('renders any word and tone it is given — the presentation is the host’s vocabulary', () => {
    const el = createModeIndicator({
      word: 'Recall',
      tone: 'warning',
      targetId: 'recall',
      items: [{ targetId: 'recall', word: 'Recall' }],
      onSelect: () => {},
    })
    expect(el.textContent).toBe('Recall')
    expect(el.dataset.tone).toBe('warning')
    expect(el.dataset.target).toBe('recall')
  })
})
