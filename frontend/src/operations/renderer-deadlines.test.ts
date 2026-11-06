import { expect, it } from 'vitest'
import { callOperationDeclaration } from '../generated/operations'
import { rendererDeadlineMs, DEADLINE_MARGIN_MS } from './renderer-deadlines'

it('arms nothing while no domain is armed', () => {
  expect(rendererDeadlineMs('open')).toBeNull()
  expect(rendererDeadlineMs('git.commit')).toBeNull()
})

it('projects turn plus max plus the margin once a domain is armed', () => {
  // open's declaration carries a config dependency and both deadlines. The
  // numbers come from the declaration, not from a copy of it: a person's turn
  // is the catalogue's (thirty seconds since the stage-12 review), and this
  // projection must follow the table rather than drift beside it.
  const open = callOperationDeclaration('open')!
  expect(rendererDeadlineMs('open', new Set(['config']))).toBe(
    open.turnMs + open.maxMs + DEADLINE_MARGIN_MS,
  )
})

it('leaves an unmigrated domain unarmed even when another is armed', () => {
  expect(rendererDeadlineMs('git.commit', new Set(['config']))).toBeNull()
})
