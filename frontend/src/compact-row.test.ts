import { expect, it } from 'vitest'
import { sameRow } from './compact-row'
import type { Row } from './generated/session.frame'

it('compares style tuple length in both directions', () => {
  const row = (style: number[]): Row => ({ text: 'x', runs: [[style, 1]] }) as Row
  const exact = row([0, 0, 0, 0, 0])
  const extended = row([0, 0, 0, 0, 0, 1])
  const shortened = row([0, 0, 0, 0])
  expect(sameRow(shortened, exact)).toBe(false)
  expect(sameRow(exact, shortened)).toBe(false)
  expect(sameRow(exact, extended)).toBe(false)
  expect(sameRow(extended, exact)).toBe(false)
})
