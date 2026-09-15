// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { Button } from './button'
import { createButton, type CreateButtonOptions } from './button-element'

afterEach(() => cleanup())

/** Everything the kit's contract is made of, and nothing the engine adds. */
function shape(el: Element): Record<string, unknown> {
  const attrs = [...el.attributes]
    .filter((a) => a.name !== 'class')
    .map((a) => [a.name, a.value] as const)
    .sort(([x], [y]) => x.localeCompare(y))
  return {
    tag: el.tagName,
    classes: [...el.classList].sort(),
    attrs,
    text: el.textContent,
    children: [...el.children].map(shape),
  }
}

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
          title={c.title}
          ariaLabel={c.ariaLabel}
          disabled={c.disabled}
        >
          {c.label}
        </Button>
      ))
      const solid = container.querySelector('button')!
      const vanilla = createButton({ ...c, onClick: vi.fn() })
      expect(shape(vanilla)).toEqual(shape(solid))
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
    expect(shape(vanilla)).not.toEqual(shape(container.querySelector('button')!))
  })

  it('routes a click to onClick exactly once', () => {
    const onClick = vi.fn()
    createButton({ label: 'x', onClick }).click()
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
