// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from '@solidjs/testing-library'
import { Stack } from './stack'

function subject(overrides?: Partial<Parameters<typeof Stack>[0]>) {
  const props = { children: 'Content', ...overrides } as Parameters<typeof Stack>[0]
  return render(() => <Stack {...props}>{props.children}</Stack>)
}

describe('Stack', () => {
  it('renders a div with class ui-stack', () => {
    const { container } = subject()
    const el = container.firstElementChild!
    expect(el.classList.contains('ui-stack')).toBe(true)
    expect(el.tagName).toBe('DIV')
  })

  it('defaults data-gap to default', () => {
    const { container } = subject()
    expect(container.firstElementChild!.getAttribute('data-gap')).toBe('default')
  })

  it('accepts explicit gap values', () => {
    const { container } = subject({ gap: 'loose' })
    expect(container.firstElementChild!.getAttribute('data-gap')).toBe('loose')
  })

  it('renders children', () => {
    const { container } = subject()
    expect(container.textContent).toContain('Content')
  })

  it('renders multiple children', () => {
    const { container } = render(() => (
      <Stack>
        <span>First</span>
        <span>Second</span>
      </Stack>
    ))
    const el = container.firstElementChild!
    expect(el.children.length).toBe(2)
    expect((el.children[0] as HTMLElement).textContent).toBe('First')
    expect((el.children[1] as HTMLElement).textContent).toBe('Second')
  })

  it('refuses class prop at compile time', () => {
    // @ts-expect-error — class is omitted from the props on purpose (§3.6)
    ;<Stack class="sneaky">Content</Stack>
  })

  it('accepts an id prop', () => {
    const { container } = subject({ id: 'my-stack' })
    expect(container.firstElementChild!.id).toBe('my-stack')
  })

  describe('divided', () => {
    it('renders data-divided when prop is true', () => {
      const { container } = subject({ divided: true })
      expect(container.firstElementChild!.getAttribute('data-divided')).toBe('true')
    })

    it('omits data-divided when prop is false', () => {
      const { container } = subject({ divided: false })
      expect(container.firstElementChild!.hasAttribute('data-divided')).toBe(false)
    })

    it('omits data-divided when prop is undefined', () => {
      const { container } = subject()
      expect(container.firstElementChild!.hasAttribute('data-divided')).toBe(false)
    })
  })
})
