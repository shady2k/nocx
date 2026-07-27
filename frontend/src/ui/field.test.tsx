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

  it('sets class on wrapper', () => {
    subject({ class: 'cm-field' })
    const wrapper = screen.getByText('Hostname').closest('.ui-field')
    expect(wrapper?.getAttribute('class')).toBe('ui-field cm-field')
  })
})
