// @vitest-environment jsdom
/**
 * RecordRow — the kit's composite for "describe a record in a row"
 * (nocx-pp3y.3). The composite owns the grammar: a title, AT MOST ONE kind
 * badge, meta text, and a status as the kit's dot + text. A surface cannot
 * mix vocabularies because there is no slot to mix them in.
 *
 * The one-badge invariant is structural: `kind` is a typed `{label, tone}`,
 * not a JSX element, so a surface physically cannot pass a second badge.
 * That is asserted twice below — the rendered count, and a compile-time
 * @ts-expect-error proving the type refuses a JSX element in the slot.
 */
import { cleanup, fireEvent, render, within } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RecordRow } from './record-row'
import { Badge } from './badge'

afterEach(cleanup)

describe("RecordRow — the kit's record grammar", () => {
  it('renders title, one kind badge with its description, meta text and the status dot + text', () => {
    const { container } = render(() => (
      <RecordRow
        title="provider"
        kind={{ label: 'OpenAI-compatible', description: 'The provider protocol' }}
        meta="3 models"
        status={{ tone: 'ok', text: 'Key saved' }}
        actions={<button type="button">Edit</button>}
      />
    ))

    const row = container.querySelector('.ui-collection-row')
    expect(row).not.toBeNull()
    const title = container.querySelector('.ui-record-row__title')
    expect(title?.textContent).toBe('provider')

    // Exactly one kind badge — the composite renders it from the typed slot.
    const badges = container.querySelectorAll('.ui-badge')
    expect(badges.length).toBe(1)
    expect(badges[0].textContent).toBe('OpenAI-compatible')
    expect(badges[0].getAttribute('title')).toBe('The provider protocol')

    expect(container.querySelector('.ui-record-row__meta-text')?.textContent).toBe('3 models')

    // Status is the kit's dot + text: the StatusDot identity, its typed
    // tone, and the text beside it.
    const dot = container.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot?.getAttribute('data-tone')).toBe('ok')
    expect(container.querySelector('.ui-record-row__status')?.textContent).toContain('Key saved')
  })

  it('puts the kind badge beside the name, not on the meta line (nocx-6jc4f)', () => {
    // WHERE PROVENANCE BELONGS. The badge used to open the meta line, so a
    // row read "name / [builtin] its own description" and the record's
    // category was the first thing said in the record's own sentence. It is
    // not part of that sentence: it is what the record IS, which is what the
    // name says — so it sits on the name's line, and the meta line is the
    // record's words alone. The kit owns the geometry, so this is asserted
    // here once rather than in every list that has a badge.
    const { container } = render(() => (
      <RecordRow
        title="skill-authoring"
        kind={{ label: 'builtin' }}
        meta="How to write a skill for this machine."
        actions={null}
      />
    ))

    const heading = container.querySelector('.ui-record-row__heading')
    expect(heading).not.toBeNull()
    // The two things on the heading line, in reading order: the name, then
    // what kind of record it is.
    expect(heading?.querySelector('.ui-record-row__title')?.textContent).toBe('skill-authoring')
    expect(heading?.querySelector('.ui-badge')?.textContent).toBe('builtin')
    // And nowhere else. A badge in both places is two answers to one
    // question, which is the defect this composite exists to prevent.
    expect(container.querySelector('.ui-record-row__meta .ui-badge')).toBeNull()
    expect(container.querySelector('.ui-record-row__meta-text')?.textContent).toBe(
      'How to write a skill for this machine.',
    )
  })

  it('renders no badge and no meta when the slots are absent', () => {
    const { container } = render(() => (
      <RecordRow title="bare" actions={<button type="button">Edit</button>} />
    ))
    expect(container.querySelectorAll('.ui-badge').length).toBe(0)
    expect(container.querySelector('.ui-record-row__meta-text')).toBeNull()
    expect(container.querySelector('.ui-status-dot')).toBeNull()
    expect(container.querySelector('.ui-record-row__detail')).toBeNull()
  })

  it('renders one line of the record’s own words under the meta line', () => {
    // The `detail` slot (nocx-edhcu): verbatim evidence — the last line a
    // pane printed. Typed as a string, so it cannot become a second
    // free-form `info` slot by another name.
    const { container } = render(() => (
      <RecordRow
        title="claude"
        meta="deploy@srv-01"
        detail="Should I drop the column?"
        actions={null}
      />
    ))
    const detail = container.querySelector('.ui-record-row__detail')
    expect(detail?.textContent).toBe('Should I drop the column?')
  })

  it('compiles only with a typed kind: a JSX badge in the slot is refused', () => {
    // The slot is `kind?: { label: string; tone?: BadgeTone }`. A surface
    // that tries to pass its own badge element gets a compile error — the
    // structural half of "at most one kind badge". If this @ts-expect-error
    // stops being an error, the type has loosened and a second dialect is
    // possible again: fix the props, not this line.
    // @ts-expect-error — kind is a typed {label, tone}, never a JSX element
    void (() => <RecordRow title="x" kind={<Badge tone="info">two</Badge>} actions={null} />)
  })

  it('passes activation through: the name opens the record, the actions do their own thing', () => {
    const onActivate = vi.fn()
    const onEdit = vi.fn()
    const { container } = render(() => (
      <RecordRow
        title="provider"
        onActivate={onActivate}
        actions={
          <button type="button" onClick={onEdit}>
            Edit
          </button>
        }
      />
    ))
    // The keyboard target USED TO BE the row itself (tabIndex 0 here), and
    // that changed deliberately with nocx-5xwub: the row announced "list
    // item" and nothing else while acting on Enter. The control is now the
    // record's name — see the describe below, which reads it by role.
    const row = container.querySelector('.ui-collection-row') as HTMLElement
    expect(row.tabIndex).toBe(-1)

    fireEvent.click(container.querySelector('.ui-record-row__title')!)
    expect(onActivate).toHaveBeenCalledTimes(1)

    fireEvent.click(container.querySelector('.ui-collection-row__actions button')!)
    expect(onEdit).toHaveBeenCalledTimes(1)
    expect(onActivate).toHaveBeenCalledTimes(1)
  })
})

describe('RecordRow — a record you can open says so (nocx-5xwub)', () => {
  it("makes the record's own name the control, named by the title", () => {
    const onActivate = vi.fn()
    const { container } = render(() => (
      <RecordRow title="Deploy failed" actions={<></>} onActivate={onActivate} />
    ))

    // Read by ROLE and NAME, never by class: what a person listening to the
    // row is told is the entire point. A row is not a button — it is a
    // record — so the control is the record's NAME, which is also where its
    // accessible name comes from for free.
    const control = within(container).getByRole('button', { name: 'Deploy failed' })
    fireEvent.click(control)
    expect(onActivate).toHaveBeenCalledTimes(1)

    // The row stops being a keyboard target of its own. It was one, and it
    // announced "list item" and nothing else — a stop that promises nothing
    // and delivers an action is the defect, not the missing role.
    const row = container.querySelector('.ui-collection-row') as HTMLElement
    expect(row.tabIndex).toBe(-1)
    expect(row.getAttribute('role')).toBe('listitem')
    expect(row.getAttribute('data-activatable')).toBe('true')

    // And the whole-row click survives as what it always was: a shortcut for
    // the mouse, on top of the control, never instead of it.
    fireEvent.click(container.querySelector('.ui-record-row__meta')!)
    expect(onActivate).toHaveBeenCalledTimes(2)
  })

  it('gives a row that cannot be opened no control at all', () => {
    const { container } = render(() => <RecordRow title="Deploy failed" actions={<></>} />)
    expect(within(container).queryByRole('button', { name: 'Deploy failed' })).toBeNull()
  })

  it('opens once when the name is clicked, not twice', () => {
    const onActivate = vi.fn()
    const { container } = render(() => (
      <RecordRow title="Deploy failed" actions={<></>} onActivate={onActivate} />
    ))
    fireEvent.click(within(container).getByRole('button', { name: 'Deploy failed' }))
    expect(onActivate).toHaveBeenCalledTimes(1)
  })
})

describe('RecordRow — the disclosure (nocx-ctl6q task 3)', () => {
  const actions = <></>

  it('a row given no disclosure props is a leaf and reserves nothing', () => {
    // "Renders exactly as it does today": Connections, Endpoints, Footprint,
    // Notes and the Notifications panel never heard of the disclosure, and a
    // column reserved for a control none of their rows can offer would indent
    // every one of them for nothing. The leading slot appears when a caller
    // says the row is part of a list that discloses — see the next test.
    const { container } = render(() => <RecordRow title="provider" actions={actions} />)
    const row = container.querySelector('.ui-record-row')
    expect(row?.getAttribute('data-disclosure')).toBe('leaf')
    expect(container.querySelector('.ui-record-row__leading')).toBeNull()
    expect(container.querySelector('.ui-record-row__disclosure')).toBeNull()
    expect(container.querySelector('.ui-record-row__disclosed')).toBeNull()
  })

  it('a row told it is not expandable is still a leaf, and reserves the width', () => {
    // A feed where some rows expand and some do not: the leaf holds the
    // disclosure's width open so every title in the list forms one column —
    // TreeRow's leading slot, the same words and the same reason.
    const { container } = render(() => (
      <RecordRow title="one occurrence" expandable={false} actions={actions} />
    ))
    const row = container.querySelector('.ui-record-row')
    expect(row?.getAttribute('data-disclosure')).toBe('leaf')
    expect(container.querySelector('.ui-record-row__leading')).not.toBeNull()
    expect(container.querySelector('.ui-record-row__disclosure')).toBeNull()
  })

  it('an expandable row offers a native button carrying the expanded state', () => {
    const onToggle = vi.fn()
    const { container } = render(() => (
      <RecordRow title="deploy ×12" expandable onToggle={onToggle} actions={actions} />
    ))
    const row = container.querySelector('.ui-record-row')
    expect(row?.getAttribute('data-disclosure')).toBe('collapsed')
    const disclosure = container.querySelector('.ui-record-row__disclosure')
    // A native button is what makes Enter and Space work without the kit
    // re-implementing activation — the half of "keyboard operable" a test
    // cannot fire in jsdom, which synthesises no click from a keydown.
    expect(disclosure?.tagName).toBe('BUTTON')
    expect(disclosure?.getAttribute('type')).toBe('button')
    expect(disclosure?.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(disclosure!)
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  it('an expanded row announces it and turns the same chevron', () => {
    const { container } = render(() => (
      <RecordRow title="deploy ×12" expandable expanded onToggle={vi.fn()} actions={actions} />
    ))
    expect(container.querySelector('.ui-record-row')?.getAttribute('data-disclosure')).toBe(
      'expanded',
    )
    expect(
      container.querySelector('.ui-record-row__disclosure')?.getAttribute('aria-expanded'),
    ).toBe('true')
    // The gesture is the kit's one chevron, rotated by the row's state —
    // never a second glyph for the same concept.
    expect(container.querySelector('.ui-record-row__disclosure-icon svg')).not.toBeNull()
  })

  it('activating the disclosure does not activate the row', () => {
    // Expanding is not opening. A click that did both would make expansion
    // unreachable with a mouse: the row would open under the pointer every
    // time you tried to see inside it.
    const onActivate = vi.fn()
    const onToggle = vi.fn()
    const { container } = render(() => (
      <RecordRow
        title="deploy ×12"
        expandable
        onToggle={onToggle}
        onActivate={onActivate}
        actions={actions}
      />
    ))
    const disclosure = container.querySelector('.ui-record-row__disclosure') as HTMLElement
    fireEvent.click(disclosure)
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(onActivate).not.toHaveBeenCalled()

    // The keyboard half: the row listens for Enter and Space, and the button
    // is inside it. Without the disclosure keeping its own keys, pressing
    // Enter on the chevron would expand the row AND open it.
    fireEvent.keyDown(disclosure, { key: 'Enter' })
    fireEvent.keyDown(disclosure, { key: ' ' })
    expect(onActivate).not.toHaveBeenCalled()

    // And the row itself still activates from its own body.
    fireEvent.click(container.querySelector('.ui-record-row__title')!)
    expect(onActivate).toHaveBeenCalledTimes(1)
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  it('discloses the caller’s own children, and only while expanded', () => {
    // The kit decides the geometry and never what goes inside: the slot is
    // rendered verbatim.
    const collapsed = render(() => (
      <RecordRow title="deploy ×12" expandable onToggle={vi.fn()} actions={actions}>
        <ul class="run-members">
          <li>09:41 build finished</li>
        </ul>
      </RecordRow>
    ))
    expect(collapsed.container.querySelector('.ui-record-row__disclosed')).toBeNull()
    expect(collapsed.container.querySelector('.run-members')).toBeNull()
    cleanup()

    const { container } = render(() => (
      <RecordRow title="deploy ×12" expandable expanded onToggle={vi.fn()} actions={actions}>
        <ul class="run-members">
          <li>09:41 build finished</li>
        </ul>
      </RecordRow>
    ))
    const disclosed = container.querySelector('.ui-record-row__disclosed')
    expect(disclosed).not.toBeNull()
    expect(disclosed?.querySelector('.run-members')?.textContent).toBe('09:41 build finished')
  })

  it('a click inside the disclosed children does not activate the row', () => {
    // What is inside the expansion is the caller's, including its clicks: a
    // row that opened itself when you clicked one of its occurrences would
    // take the pointer away from the thing you aimed at.
    const onActivate = vi.fn()
    const onOccurrence = vi.fn()
    const { container } = render(() => (
      <RecordRow
        title="deploy ×12"
        expandable
        expanded
        onToggle={vi.fn()}
        onActivate={onActivate}
        actions={actions}
      >
        <button type="button" class="run-member" onClick={onOccurrence}>
          09:41 build finished
        </button>
      </RecordRow>
    ))
    fireEvent.click(container.querySelector('.run-member')!)
    expect(onOccurrence).toHaveBeenCalledTimes(1)
    expect(onActivate).not.toHaveBeenCalled()
    fireEvent.keyDown(container.querySelector('.run-member')!, { key: 'Enter' })
    expect(onActivate).not.toHaveBeenCalled()
  })

  it('names the disclosure by what it discloses', () => {
    // One accessible name per control, and it says which row it opens — the
    // same "Expand <name>" / "Collapse <name>" TreeRow uses.
    const collapsed = render(() => (
      <RecordRow title="deploy ×12" expandable onToggle={vi.fn()} actions={actions} />
    ))
    expect(
      collapsed.container.querySelector('.ui-record-row__disclosure')?.getAttribute('aria-label'),
    ).toBe('Expand deploy ×12')
    cleanup()
    const { container } = render(() => (
      <RecordRow title="deploy ×12" expandable expanded onToggle={vi.fn()} actions={actions} />
    ))
    expect(container.querySelector('.ui-record-row__disclosure')?.getAttribute('aria-label')).toBe(
      'Collapse deploy ×12',
    )
  })
})

describe("RecordRow — the row's state has a cell of its own (nocx-xa0cq)", () => {
  // Everything here is read STRUCTURALLY rather than in pixels, because jsdom
  // lays nothing out: `getBoundingClientRect` answers zeros for every element,
  // so a coordinate assertion would pass just as happily on the ragged row it
  // is meant to catch. What actually holds the column is where the control
  // sits — the row's own state cell, at the trailing edge, outside the actions
  // whose contents decide each other's positions — and that is what is read.
  it("holds the state control at the row's trailing edge, outside the actions", () => {
    const { container } = render(() => (
      <RecordRow
        title="deploy"
        state={<input type="checkbox" role="switch" aria-label="deploy enabled" />}
        actions={<button type="button">Delete</button>}
      />
    ))

    const cell = container.querySelector('.ui-record-row__state')
    expect(cell).not.toBeNull()
    expect(cell?.querySelector('[role="switch"]')).not.toBeNull()

    // The caller's actions are not in the cell, and the cell is the last
    // thing in the row's trailing region — so its place is the row's right
    // edge and not the width of whatever buttons precede it.
    const trailing = container.querySelector('.ui-collection-row__actions')!
    expect(cell?.querySelector('button')).toBeNull()
    expect(trailing.lastElementChild).toBe(cell)
    expect(trailing.querySelector('button')?.textContent).toBe('Delete')
  })

  it('reserves nothing on a row whose list has no state control', () => {
    // The disclosure's absent case, on the other end of the row: a column
    // reserved for a control none of a list's rows can offer would hold width
    // open on every one of them for nothing.
    const { container } = render(() => (
      <RecordRow title="provider" actions={<button type="button">Edit</button>} />
    ))
    expect(container.querySelector('.ui-record-row__state')).toBeNull()
  })

  it('reserves the empty cell on a row that has no state control beside rows that do', () => {
    // `expandable={false}`'s reason, mirrored: a row in a list that DOES have
    // state controls holds the column open even when it has nothing to put in
    // it, or its own actions run to the edge the others stop short of and the
    // raggedness has only moved to the buttons.
    const { container } = render(() => (
      <RecordRow title="builtin" state={null} actions={<button type="button">Edit</button>} />
    ))
    const cell = container.querySelector('.ui-record-row__state')
    expect(cell).not.toBeNull()
    expect(cell?.childElementCount).toBe(0)
    expect(container.querySelector('.ui-collection-row__actions')!.lastElementChild).toBe(cell)
  })
})
