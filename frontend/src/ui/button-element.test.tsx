// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { assertSameShape } from '../test-support/element-shape'
import { Button } from './button'
import { createButton, type CreateButtonOptions } from './button-element'

afterEach(() => cleanup())

const CASES: ReadonlyArray<Omit<CreateButtonOptions, 'onClick'>> = [
  { label: 'Enable command editor', variant: 'ghost', size: 'sm' },
  {
    label: 'openrouter',
    variant: 'ghost',
    size: 'sm',
    truncate: true,
    title: 'openrouter',
    ariaLabel: 'Answers with openrouter. Open Endpoints.',
  },
  { label: 'm-a', variant: 'ghost', size: 'sm', truncate: true, disabled: true },
  { label: 'deepseek/v4', variant: 'ghost', size: 'sm', truncate: true, mono: true },
  { label: 'Plain' },
  { label: 'Primary', variant: 'primary' },
]

describe('createButton is the Button, emitted without Solid (spec §6.2)', () => {
  for (const c of CASES) {
    it(`matches <Button> for ${JSON.stringify(c)}`, () => {
      const { container } = render(() => (
        <Button
          onClick={vi.fn()}
          variant={c.variant}
          size={c.size}
          truncate={c.truncate}
          mono={c.mono}
          title={c.title}
          ariaLabel={c.ariaLabel}
          disabled={c.disabled}
        >
          {c.label}
        </Button>
      ))
      const solid = container.querySelector('button')!
      const vanilla = createButton({ ...c, onClick: vi.fn() })
      assertSameShape(vanilla, solid)
    })
  }

  it('fails when a variance exists on one side only — the check is not vacuous', () => {
    const vanilla = createButton({
      label: 'x',
      variant: 'ghost',
      size: 'sm',
      truncate: true,
      onClick: vi.fn(),
    })
    vanilla.dataset.extra = 'drift'
    const { container } = render(() => (
      <Button onClick={vi.fn()} variant="ghost" size="sm" truncate>
        x
      </Button>
    ))
    expect(() => assertSameShape(vanilla, container.querySelector('button')!)).toThrow(
      /emitters disagree/,
    )
  })

  it('routes a click to onClick exactly once', () => {
    const onClick = vi.fn()
    createButton({ label: 'x', onClick }).click()
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
