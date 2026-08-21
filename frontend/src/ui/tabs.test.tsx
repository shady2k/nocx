// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { createSignal } from 'solid-js'
import { render, screen, cleanup, fireEvent } from '@solidjs/testing-library'
import { Tabs, type TabsProps, type TabItemStatus } from './tabs'

afterEach(() => cleanup())

function subject(overrides?: Partial<TabsProps>) {
  const props: TabsProps = {
    items: [
      { id: 'a', label: 'A', content: () => 'Content A' },
      { id: 'b', label: 'B', content: () => 'Content B' },
    ],
    active: 'a',
    onChange: () => {},
    ...overrides,
  }
  return render(() => <Tabs {...props} />)
}

describe('Tabs', () => {
  it('renders all tab labels', () => {
    subject()
    expect(screen.getByText('A')).toBeTruthy()
    expect(screen.getByText('B')).toBeTruthy()
  })

  it('has ui-tabs class identity', () => {
    subject()
    const el = document.querySelector('.ui-tabs')
    expect(el).toBeTruthy()
  })

  it('has ui-tabs__list class identity', () => {
    subject()
    const el = document.querySelector('.ui-tabs__list')
    expect(el).toBeTruthy()
  })

  describe('status indicator', () => {
    it('does not render .ui-status-dot when no status is set', () => {
      subject()
      expect(document.querySelector('.ui-status-dot')).toBeNull()
    })

    it('renders .ui-status-dot when status is set', () => {
      const status: TabItemStatus = { tone: 'ok', accessibleName: 'Available' }
      subject({
        items: [
          { id: 'a', label: 'Store A', content: () => 'A', status },
          { id: 'b', label: 'Store B', content: () => 'B' },
        ],
      })
      expect(document.querySelector('.ui-status-dot')).toBeTruthy()
    })

    it('sets data-tone attribute matching the tone', () => {
      const status: TabItemStatus = { tone: 'ok', accessibleName: 'Available' }
      subject({
        items: [{ id: 'a', label: 'A', content: () => 'A', status }],
      })
      const marker = document.querySelector('.ui-status-dot')
      expect(marker!.getAttribute('data-tone')).toBe('ok')
    })

    it('renders all three tones ok/warning/error', () => {
      subject({
        items: [
          { id: 'a', label: 'A', content: () => 'A', status: { tone: 'ok', accessibleName: 'Ok' } },
          {
            id: 'b',
            label: 'B',
            content: () => 'B',
            status: { tone: 'warning', accessibleName: 'Warn' },
          },
          {
            id: 'c',
            label: 'C',
            content: () => 'C',
            status: { tone: 'error', accessibleName: 'Err' },
          },
        ],
      })
      const markers = document.querySelectorAll('.ui-status-dot')
      expect(markers[0].getAttribute('data-tone')).toBe('ok')
      expect(markers[1].getAttribute('data-tone')).toBe('warning')
      expect(markers[2].getAttribute('data-tone')).toBe('error')
    })

    it('accessible name is reachable via accessible query on the tab', () => {
      subject({
        items: [
          {
            id: 's',
            label: 'Test Store',
            content: () => 'Content',
            status: { tone: 'error', accessibleName: 'Not responding' },
          },
        ],
      })
      // The button's computed accessible name includes both the visible label
      // and the visually-hidden status text. Regex to confirm both are present.
      const tab = screen.getByRole('tab', { name: /Test Store.*Not responding/ })
      expect(tab).toBeTruthy()
    })

    it('marker is aria-hidden', () => {
      subject({
        items: [
          {
            id: 'a',
            label: 'A',
            content: () => 'A',
            status: { tone: 'ok', accessibleName: 'Available' },
          },
        ],
      })
      const marker = document.querySelector('.ui-status-dot')
      expect(marker!.getAttribute('aria-hidden')).toBe('true')
    })

    it('marker is visible on the unselected row', () => {
      subject({
        active: 'b',
        items: [
          {
            id: 'a',
            label: 'Store A',
            content: () => 'A',
            status: { tone: 'warning', accessibleName: 'Unstable' },
          },
          { id: 'b', label: 'Store B', content: () => 'B' },
        ],
      })
      const marker = document.querySelector('.ui-status-dot')
      expect(marker).toBeTruthy()
      // The marker must be visible even when its row is not selected —
      // that is the entire point of the feature.
      const style = window.getComputedStyle(marker!)
      expect(style.display).not.toBe('none')
      expect(style.visibility).not.toBe('hidden')
    })
  })

  describe('active section sizing', () => {
    it('hides the inactive panel so only the active section sizes the box', () => {
      subject()
      const panels = document.querySelectorAll<HTMLElement>('.ui-tabs__panel')
      expect(panels).toHaveLength(2)
      // `hidden` renders as `display: none` in every browser: the inactive
      // panel contributes no height, so the box is the ACTIVE section's size,
      // not the tallest's — the empty space below a short section's footer is
      // gone. It also keeps the panel out of the tab order and the
      // accessibility tree, which is what `visibility: hidden` used to buy.
      expect(panels[0].hidden).toBe(false)
      expect(panels[1].hidden).toBe(true)
    })

    it('hides whichever section is inactive', () => {
      subject({ active: 'b' })
      const panels = document.querySelectorAll<HTMLElement>('.ui-tabs__panel')
      expect(panels[0].hidden).toBe(true)
      expect(panels[1].hidden).toBe(false)
    })

    it('keeps the inactive panel out of the accessibility tree', () => {
      subject()
      // Only the active panel is exposed as a tabpanel — `hidden` renders as
      // `display: none`, which removes the inactive one from the tree, the
      // same exclusion `visibility: hidden` used to provide.
      const panels = screen.queryAllByRole('tabpanel')
      expect(panels).toHaveLength(1)
      expect(panels[0].getAttribute('id')).toBe('ui-tabpanel-a')
    })
  })

  // ── The identity contract ───────────────────────────────────────────────
  //
  // Callers write `items` inline in JSX. Any signal that expression reads —
  // a label carrying a count, a status derived from the form — rebuilds the
  // whole array on every keystroke, with new item objects in it. Keyed by
  // reference, every panel was disposed and rebuilt, and the input the person
  // was typing into went with it: FOCUS LEFT THE FIELD AFTER EVERY
  // CHARACTER. These are the two tests that keep it gone.

  describe('a rebuilt items array does not rebuild the panels', () => {
    /** A caller of the shape every real one has: the items array reads a
     *  signal, so it is a new array with new objects on every change. */
    function typingSubject() {
      const [text, setText] = createSignal('')
      render(() => (
        <Tabs
          active="a"
          onChange={() => {}}
          items={[
            {
              id: 'a',
              // The label reads the signal — this is what makes the array
              // reactive, and it is what a count on a tab looks like.
              label: `A ${text().length}`,
              content: () => (
                <input
                  data-testid="field"
                  value={text()}
                  onInput={(e) => setText(e.currentTarget.value)}
                />
              ),
            },
            { id: 'b', label: 'B', content: () => 'Content B' },
          ]}
        />
      ))
      return { text }
    }

    it('the field a person is typing into keeps the focus', () => {
      typingSubject()
      const field = screen.getByTestId('field')
      field.focus()
      expect(document.activeElement).toBe(field)

      fireEvent.input(field, { target: { value: 'x' } })

      // The SAME element, still focused. Before the fix this node had been
      // discarded and replaced, so activeElement was <body>.
      expect(screen.getByTestId('field')).toBe(field)
      expect(document.activeElement).toBe(field)
    })

    it('and the label still updates in place', () => {
      typingSubject()
      const field = screen.getByTestId('field')
      fireEvent.input(field, { target: { value: 'xy' } })
      // The point of keeping the DOM is not to freeze it: the count on the
      // tab is exactly the reactive label that used to cause the rebuild.
      expect(screen.getByText('A 2')).toBeTruthy()
    })
  })
})
