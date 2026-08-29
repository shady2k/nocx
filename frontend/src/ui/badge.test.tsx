// @vitest-environment jsdom
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { Badge, type BadgeProps } from './badge'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const CSS = resolve(dirname, '../styles/components/badge.css')
const THEMES = resolve(dirname, '../styles/themes')
const SOLID_TEXT_CONTRAST_THRESHOLD = 3

function token(themeText: string, name: string): string {
  const match = themeText.match(new RegExp(`--${name}\\s*:\\s*([^;]+);`))
  if (!match) throw new Error(`no --${name} in theme`)
  return match[1].trim()
}

function luminance(hex: string): number {
  const channels = hex
    .replace('#', '')
    .match(/../g)!
    .map((channel) => parseInt(channel, 16) / 255)
    .map((channel) =>
      channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4,
    )
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]
}

function contrast(a: string, b: string): number {
  const [first, second] = [luminance(a), luminance(b)]
  const [high, low] = first > second ? [first, second] : [second, first]
  return (high + 0.05) / (low + 0.05)
}

afterEach(() => cleanup())

function subject(overrides?: Partial<BadgeProps>) {
  const props: BadgeProps = {
    children: 'Customized',
    ...overrides,
  }
  return render(() => <Badge {...props} />)
}

describe('Badge', () => {
  it('renders text content', () => {
    subject()
    expect(screen.getByText('Customized')).toBeTruthy()
  })

  it('renders with ui-badge class identity', () => {
    subject()
    const el = screen.getByText('Customized')
    expect(el.classList.contains('ui-badge')).toBe(true)
  })

  it('defaults to neutral tone', () => {
    subject()
    const el = screen.getByText('Customized')
    expect(el.getAttribute('data-tone')).toBe('neutral')
  })

  it('applies info tone', () => {
    subject({ tone: 'info' })
    const el = screen.getByText('Customized')
    expect(el.getAttribute('data-tone')).toBe('info')
  })

  it('marks the solid variant and consumes the ring context with a token fallback', () => {
    subject({ tone: 'info', variant: 'solid' })
    const el = screen.getByText('Customized')
    expect(el.getAttribute('data-variant')).toBe('solid')

    const css = readFileSync(CSS, 'utf8')
    const rule = css.match(/\.ui-badge\[data-variant='solid'\]\s*\{([^}]*)\}/s)?.[1]
    expect(rule).toBeDefined()
    expect(rule).toMatch(/background:\s*var\(--color-accent\)/)
    expect(rule).toMatch(/color:\s*var\(--color-canvas\)/)
    expect(rule).toMatch(
      /box-shadow:\s*0 0 0 2px var\(--badge-ring-color,\s*var\(--color-chrome-rail\)\)/,
    )
  })

  it(`keeps solid badge text at least ${SOLID_TEXT_CONTRAST_THRESHOLD}:1 in every theme`, () => {
    const themeFiles = readdirSync(THEMES).filter((file) => file.endsWith('.css'))
    expect(themeFiles.length).toBeGreaterThanOrEqual(10)
    for (const file of themeFiles) {
      const theme = readFileSync(resolve(THEMES, file), 'utf8')
      const accent = token(theme, 'color-accent')
      const canvas = token(theme, 'color-canvas')
      expect(
        contrast(canvas, accent),
        `${file}: canvas ${canvas} on accent ${accent}`,
      ).toBeGreaterThanOrEqual(SOLID_TEXT_CONTRAST_THRESHOLD)
    }
  })

  it('applies warning tone', () => {
    subject({ tone: 'warning' })
    const el = screen.getByText('Customized')
    expect(el.getAttribute('data-tone')).toBe('warning')
  })

  it('applies danger tone', () => {
    subject({ tone: 'danger' })
    const el = screen.getByText('Customized')
    expect(el.getAttribute('data-tone')).toBe('danger')
  })

  it('is a span element', () => {
    subject()
    const el = screen.getByText('Customized')
    expect(el.tagName).toBe('SPAN')
  })

  it('opts into truncation with data-truncate', () => {
    subject({ truncate: true })
    expect(screen.getByText('Customized').getAttribute('data-truncate')).toBe('true')
  })

  it('does not truncate by default', () => {
    subject()
    expect(screen.getByText('Customized').getAttribute('data-truncate')).toBeNull()
  })
})
