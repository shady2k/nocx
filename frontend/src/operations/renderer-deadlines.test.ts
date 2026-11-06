import { expect, it } from 'vitest'
import { rendererDeadlineMs, DEADLINE_MARGIN_MS } from './renderer-deadlines'

it('arms nothing while no domain is armed', () => {
  expect(rendererDeadlineMs('open')).toBeNull()
  expect(rendererDeadlineMs('git.commit')).toBeNull()
})

it('projects turn plus max plus the margin once a domain is armed', () => {
  // open's declaration carries a config dependency and both deadlines.
  expect(rendererDeadlineMs('open', new Set(['config']))).toBe(1000 + 300000 + DEADLINE_MARGIN_MS)
})

it('leaves an unmigrated domain unarmed even when another is armed', () => {
  expect(rendererDeadlineMs('git.commit', new Set(['config']))).toBeNull()
})
