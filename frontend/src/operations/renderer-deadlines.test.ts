import { expect, it } from 'vitest'
import { callOperationDeclaration, catalogueDomains } from '../generated/operations'
import { rendererDeadlineMs, armedDomains, DEADLINE_MARGIN_MS } from './renderer-deadlines'

it('arms every domain the catalogue declares', () => {
  // ADR-8eej3e66 stage 8: every domain has migrated, so every domain is armed.
  // The set is derived from the catalogue, and this compares it with the
  // catalogue's own list so a domain nobody armed cannot exist.
  expect([...armedDomains()].sort()).toEqual(catalogueDomains())
})

it('projects turn plus max plus the margin for an armed domain', () => {
  // The numbers come from the declaration, not from a copy of it: a person's
  // turn is the catalogue's (thirty seconds since the stage-12 review), and
  // this projection must follow the table rather than drift beside it.
  const open = callOperationDeclaration('open')!
  expect(rendererDeadlineMs('open')).toBe(open.turnMs + open.maxMs + DEADLINE_MARGIN_MS)
})

it('gives no timer to a method outside the catalogue', () => {
  expect(rendererDeadlineMs('not.a.method')).toBeNull()
})
