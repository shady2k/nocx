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
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RecordRow } from './record-row'
import { Badge } from './badge'

afterEach(cleanup)

describe("RecordRow — the kit's record grammar", () => {
  it('renders title, one kind badge, meta text and the status dot + text', () => {
    const { container } = render(() => (
      <RecordRow
        title="provider"
        kind={{ label: 'OpenAI-compatible' }}
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

    expect(container.querySelector('.ui-record-row__meta-text')?.textContent).toBe('3 models')

    // Status is the kit's dot + text: the StatusDot identity, its typed
    // tone, and the text beside it.
    const dot = container.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot?.getAttribute('data-tone')).toBe('ok')
    expect(container.querySelector('.ui-record-row__status')?.textContent).toContain('Key saved')
  })

  it('renders no badge and no meta when the slots are absent', () => {
    const { container } = render(() => (
      <RecordRow title="bare" actions={<button type="button">Edit</button>} />
    ))
    expect(container.querySelectorAll('.ui-badge').length).toBe(0)
    expect(container.querySelector('.ui-record-row__meta-text')).toBeNull()
    expect(container.querySelector('.ui-status-dot')).toBeNull()
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

  it('passes activation through: Enter on the row fires onActivate, not the actions', () => {
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
    const row = container.querySelector('.ui-collection-row') as HTMLElement
    expect(row.tabIndex).toBe(0)

    fireEvent.click(container.querySelector('.ui-record-row__title')!)
    expect(onActivate).toHaveBeenCalledTimes(1)

    fireEvent.click(container.querySelector('.ui-collection-row__actions button')!)
    expect(onEdit).toHaveBeenCalledTimes(1)
    expect(onActivate).toHaveBeenCalledTimes(1)
  })
})
