// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { Field, type FieldProps } from './field'

afterEach(() => cleanup())

function subject(overrides?: Partial<FieldProps>) {
  const props: FieldProps = {
    for: 'host',
    label: 'Hostname',
    children: <input id="host" />,
    ...overrides,
  }
  return render(() => <Field {...props} />)
}

describe('Field', () => {
  it('renders the label', () => {
    subject()
    expect(screen.getByText('Hostname')).toBeTruthy()
  })

  it('wires label for attribute to control id', () => {
    subject({ for: 'port' })
    const label = screen.getByText('Hostname')
    expect(label.getAttribute('for')).toBe('port')
  })

  it('renders children', () => {
    subject()
    expect(screen.getByRole('textbox')).toBeTruthy()
  })

  it('renders description', () => {
    subject({ description: 'Enter a hostname or IP' })
    expect(screen.getByText('Enter a hostname or IP')).toBeTruthy()
  })

  it('renders error text', () => {
    subject({ error: 'Hostname is required' })
    expect(screen.getByText('Hostname is required')).toBeTruthy()
  })

  it('uses role=alert on error', () => {
    subject({ error: 'Invalid' })
    const error = screen.getByText('Invalid')
    expect(error.getAttribute('role')).toBe('alert')
  })

  it('shows required indicator', () => {
    subject({ required: true })
    expect(screen.getByText('*')).toBeTruthy()
  })

  // §3.6 kept `class` on the structural containers, bounded to layout. Measured, Field
  // had no consumer passing one — six call sites across connections and settings, none
  // of them — so the prop is gone rather than bounded. A prop needs two legitimate
  // consumers to exist; this had none, and the only class it ever carried in anger was
  // in this test.
  it('emits its identity and nothing else', () => {
    subject()
    const wrapper = screen.getByText('Hostname').closest('.ui-field')
    expect(wrapper?.getAttribute('class')).toBe('ui-field')
  })

  it('adds only the horizontal modifier in that orientation', () => {
    subject({ orientation: 'horizontal' })
    const wrapper = screen.getByText('Hostname').closest('.ui-field')
    expect(wrapper?.getAttribute('class')).toBe('ui-field ui-field-horizontal')
  })

  describe('data-label default', () => {
    it('vertical defaults to secondary', () => {
      subject()
      const wrapper = screen.getByText('Hostname').closest('.ui-field')
      expect(wrapper?.getAttribute('data-label')).toBe('secondary')
    })

    it('horizontal defaults to primary', () => {
      subject({ orientation: 'horizontal' })
      const wrapper = screen.getByText('Hostname').closest('.ui-field')
      expect(wrapper?.getAttribute('data-label')).toBe('primary')
    })

    it('horizontal respects explicit labelProminence override', () => {
      subject({ orientation: 'horizontal', labelProminence: 'secondary' })
      const wrapper = screen.getByText('Hostname').closest('.ui-field')
      expect(wrapper?.getAttribute('data-label')).toBe('secondary')
    })
  })
})
