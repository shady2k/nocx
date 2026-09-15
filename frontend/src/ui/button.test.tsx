// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { Button, type ButtonProps } from './button'
afterEach(() => cleanup())

function subject(overrides?: Partial<ButtonProps>) {
  const props: ButtonProps = {
    onClick: vi.fn(),
    children: 'Click me',
    ...overrides,
  }
  return render(() => <Button {...props} />)
}

describe('Button', () => {
  it('renders the label text', () => {
    subject()
    expect(screen.getByText('Click me')).toBeTruthy()
  })

  it('calls onClick when clicked', () => {
    const onClick = vi.fn()
    subject({ onClick })
    fireEvent.click(screen.getByText('Click me'))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('is natively keyboard-activatable (Enter/Space handled by browser)', () => {
    subject()
    const btn = screen.getByText('Click me')
    // Native <button> handles Enter/Space activation — test that it's a real button
    expect(btn.tagName).toBe('BUTTON')
    expect(btn.getAttribute('type')).toBe('button')
  })

  it('renders base class ui-button', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.classList.contains('ui-button')).toBe(true)
  })

  it('defaults type to button', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('type')).toBe('button')
  })

  it('respects explicit type', () => {
    subject({ type: 'submit' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('type')).toBe('submit')
  })

  it('sets disabled attribute', () => {
    subject({ disabled: true })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('disabled')).not.toBeNull()
  })

  it('does not call onClick when disabled', () => {
    const onClick = vi.fn()
    subject({ disabled: true, onClick })
    const btn = screen.getByText('Click me')
    btn.click()
    expect(onClick).not.toHaveBeenCalled()
  })

  it('sets title', () => {
    subject({ title: 'Tooltip text' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('title')).toBe('Tooltip text')
  })

  it('sets aria-label', () => {
    subject({ ariaLabel: 'Dismiss', children: '✕' })
    expect(screen.getByLabelText('Dismiss')).toBeTruthy()
  })

  it('defaults data-variant to default', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('data-variant')).toBe('default')
  })

  it('renders data-variant="primary" for primary variant', () => {
    subject({ variant: 'primary' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('data-variant')).toBe('primary')
  })

  it('renders data-variant="danger" for danger variant', () => {
    subject({ variant: 'danger' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('data-variant')).toBe('danger')
  })

  it('renders data-size="sm" when size is sm', () => {
    subject({ size: 'sm' })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('data-size')).toBe('sm')
  })

  it('does not render data-size for md (default)', () => {
    subject({ size: 'md' })
    const btn = screen.getByText('Click me')
    expect(btn.hasAttribute('data-size')).toBe(false)
  })

  it('has role button for accessibility', () => {
    subject()
    expect(screen.getByRole('button')).toBeTruthy()
  })

  it('is focusable via tab', () => {
    subject()
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('tabindex')).toBeNull() // natively focusable
  })
  it('renders a secondary line beneath the label and in the accessible name', () => {
    const { container } = subject({
      children: 'Allow once',
      secondary: '— this proposal only',
    })
    const button = screen.getByRole('button', { name: 'Allow once — this proposal only' })
    const line = container.querySelector('.ui-button__secondary')

    expect(button).toBeTruthy()
    expect(line?.textContent).toBe('— this proposal only')
    expect(line?.previousElementSibling?.textContent).toBe('Allow once')
    expect(button.getAttribute('data-secondary')).toBe('true')
    const css = readFileSync('src/styles/components/button.css', 'utf8')
    expect(css).toContain(".ui-button[data-secondary='true']")
    expect(css).toContain('.ui-button__secondary')
    expect(css).toContain('font-size: var(--font-size-2xs)')
    expect(css).toMatch(/\.ui-button__secondary\s*\{[\s\S]*white-space: normal/)
  })
})

// Rule 1 is enforced by the type system, not by a lint rule, and this records why
// that needed checking. Removing `class` from ButtonProps looked sufficient and was
// not: `ButtonProps & JSX.IntrinsicElements['button']` handed it straight back, and
// since `class` had also left `knownKeys` it fell into `rest` and was spread onto the
// element. The escape hatch stayed fully open while every signal said it was closed.
//
// A runtime test cannot express "this does not compile", so what it can check is the
// consequence: nothing a caller passes reaches the element's class attribute.
describe('Button — the class escape hatch is closed', () => {
  it('emits only its own identity and variant, whatever the caller does', () => {
    render(() => (
      // @ts-expect-error — `class` is omitted from the props on purpose (§3.6)
      <Button class="sneaky" onClick={() => {}}>
        Save
      </Button>
    ))
    const el = screen.getByRole('button')
    expect(el.getAttribute('class')).toBe('ui-button')
    expect(el.classList.contains('sneaky')).toBe(false)
  })
})

// ── Ghost selected — the neutral register (nocx-dgsp) ──────────────────
// The selected ghost row is the "current choice in a list" state the two
// list consumers use (the settings rail's GroupedRail rows and the vertical
// Tabs rows). The DOM contract is `aria-selected`; the CSS keyed off it is
// asserted below against the SHIPPED stylesheet, because jsdom cannot
// resolve var() in computed styles — the deterministic in-jsdom assertion
// of the visual contract is the token relationship in the real file.

describe('Button — ghost selected', () => {
  it('emits aria-selected for the list-consumer contract', () => {
    const { unmount } = subject({ variant: 'ghost', selected: true })
    const btn = screen.getByText('Click me')
    expect(btn.getAttribute('aria-selected')).toBe('true')
    unmount()
    subject({ variant: 'ghost' })
    expect(screen.getByText('Click me').hasAttribute('aria-selected')).toBe(false)
  })

  it('keeps the selected plate distinct from the hover plate (the separation)', () => {
    const css = readFileSync('src/styles/components/button.css', 'utf8')
    const backgroundToken = (selector: string): string => {
      const block = css.match(
        new RegExp(selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*\\{([^}]*)\\}'),
      )
      expect(block, `rule not found: ${selector}`).not.toBeNull()
      const decl = block![1].match(/background:\s*var\((--[a-z0-9-]+)\)/)
      expect(decl, `no background var in ${selector}`).not.toBeNull()
      return decl![1]
    }

    const selected = backgroundToken(".ui-button[data-variant='ghost'][aria-selected='true']")
    const hover = backgroundToken(".ui-button[data-variant='ghost']:hover")

    // The whole difficulty: selected and hover sit adjacent in the rail, so
    // a shared plate token would make them indistinguishable. The selected
    // plate is the tab strip's token; hover keeps the ghost hover token.
    expect(selected).toBe('--color-tab-active')
    expect(hover).toBe('--color-surface-hover')
    expect(selected).not.toBe(hover)
  })

  it('keeps the accent in the marker channel, not the plate', () => {
    const css = readFileSync('src/styles/components/button.css', 'utf8')
    const selectedBlock = css.match(
      /\.ui-button\[data-variant='ghost'\]\[aria-selected='true'\]\s*\{([^}]*)\}/,
    )![1]
    // The plate holds no accent…
    expect(selectedBlock).toContain('background: var(--color-tab-active)')
    // …the accent is a 2px leading-edge marker on the same state.
    const markerBlock = css.match(
      /\.ui-button\[data-variant='ghost'\]\[aria-selected='true'\]::before\s*\{([^}]*)\}/,
    )![1]
    expect(markerBlock).toContain('width: 2px')
    expect(markerBlock).toContain('background: var(--color-accent)')
  })

  it('owns the row radius at the VARIANT level, not per consumer', () => {
    const css = readFileSync('src/styles/components/button.css', 'utf8')
    const ghost = css.match(/\.ui-button\[data-variant='ghost'\]\s*\{([^}]*)\}/)![1]
    expect(ghost).toContain('border-radius: var(--control-radius-md)')
    // The component file may not name a consumer: the next rail-like list
    // inherits the answer from the variant instead of copying a selector.
    expect(css).not.toMatch(/ui-grouped-nav|ui-settings-section-nav/)
  })
})

describe('Button truncate (nocx-9bpeq.7)', () => {
  it('yields to its row: data-truncate is set only when asked', () => {
    const { container } = render(() => (
      <>
        <Button onClick={vi.fn()} size="sm" truncate>
          long
        </Button>
        <Button onClick={vi.fn()} size="sm">
          short
        </Button>
      </>
    ))
    const [long, short] = [...container.querySelectorAll('button')]
    expect(long.dataset.truncate).toBe('true')
    expect(short.hasAttribute('data-truncate')).toBe(false)
  })

  it('the stylesheet ellipsises a small truncating button on one line', () => {
    const css = readFileSync(
      resolve(import.meta.dirname ?? '.', '..', 'styles/components/button.css'),
      'utf8',
    )
    const rule =
      /\.ui-button\[data-size='sm'\]\[data-truncate='true'\]\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''
    expect(rule).toMatch(/text-overflow:\s*ellipsis/)
    expect(rule).toMatch(/white-space:\s*nowrap/)
    expect(rule).toMatch(/overflow:\s*hidden/)
    expect(rule).toMatch(/display:\s*inline-block/)
  })
})

// Height following `data-size` used to work for `ghost` alone: `default`
// hard-coded `--control-height-sm` (24px, not any declared size), `primary`
// and `danger` hard-coded a bare `24px`, and `dashed` set no height at all —
// so `data-size` did nothing for any of the four, and the running command
// block's Stop button (`variant: 'default'`, no size) rendered at 24px
// instead of the mockup pass's 32px target (decision record 2026-09-15
// §1.7, nocx-9bpeq.21 round 2). Asserted against the SHIPPED stylesheet,
// like the ghost-selected tests above: jsdom cannot resolve var() in
// computed style, so the deterministic check is the token relationship in
// the real file.
describe('Button — height follows data-size, for every variant (nocx-9bpeq.21 round 2)', () => {
  const css = readFileSync(
    resolve(import.meta.dirname ?? '.', '..', 'styles/components/button.css'),
    'utf8',
  )

  it('gives every non-ghost, non-workspace variant the md height when unsized', () => {
    // One rule, four variants: the same specificity trick ghost already
    // used (`:not([data-size])`), so `[data-size='sm']` stays a SEPARATE,
    // mutually exclusive match rather than a specificity tie this rule
    // could lose or win depending on declaration order.
    const rule =
      /((?:\.ui-button\[data-variant='(?:default|primary|danger|dashed)'\]:not\(\[data-size\]\):not\(\[data-secondary\]\)),?\s*){4}\{([^}]*)\}/.exec(
        css,
      )
    expect(rule, 'combined default/primary/danger/dashed height rule not found').not.toBeNull()
    expect(rule![0]).toMatch(/height:\s*var\(--control-height-md\)/)
  })

  it('keeps the explicit sm height unchanged', () => {
    const rule = /\.ui-button\[data-size='sm'\]\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''
    expect(rule).toMatch(/height:\s*var\(--control-height-xs\)/)
  })

  it('never lets the md height override a two-line secondary button', () => {
    // agent-approval-prompt.tsx pairs `secondary` with `default`, `primary`
    // AND `danger`, all sizeless — the exact combination the md rule above
    // must not match, or the second line collapses under a fixed 32px.
    for (const variant of ['default', 'primary', 'danger', 'dashed']) {
      const re = new RegExp(
        `\\.ui-button\\[data-variant='${variant}'\\]:not\\(\\[data-size\\]\\):not\\(\\[data-secondary\\]\\)`,
      )
      expect(css, `${variant} height rule must exclude [data-secondary]`).toMatch(re)
    }
    const secondaryRule = /\.ui-button\[data-secondary='true'\]\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''
    expect(secondaryRule).toMatch(/height:\s*auto/)
    expect(secondaryRule).toMatch(/min-height:\s*var\(--control-height-sm\)/)
  })

  it('no longer hard-codes a height on the default/primary/danger variant rules themselves', () => {
    for (const variant of ['default', 'primary', 'danger']) {
      const block = new RegExp(`\\.ui-button\\[data-variant='${variant}'\\]\\s*\\{([^}]*)\\}`).exec(
        css,
      )?.[1]
      expect(block, `${variant} rule not found`).toBeDefined()
      expect(block).not.toMatch(/height\s*:/)
    }
  })
})
