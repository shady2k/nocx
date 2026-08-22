// The offers this module makes, at their edges.
//
// Every function here fills a field in while nothing has been typed into it,
// so the case that matters most is the one where there is NOTHING to offer:
// a surface that proposed a half-formed path would be worse than one that
// proposed nothing, because the person would have to notice and undo it.

import { describe, expect, it } from 'vitest'
import { proposedDestination, slugify, environmentPath } from './api-paths'

const ROOT = '/home/dev/.local/share/nocx/collections'

describe('proposedDestination — where an imported collection is proposed to land', () => {
  it('drops EVERY suffix, because a Postman export carries two', () => {
    // `acme.postman_collection` would name the folder after our import
    // machinery rather than after the collection inside it.
    expect(proposedDestination(ROOT, '/work/acme.postman_collection.json')).toBe(`${ROOT}/acme`)
  })

  it('takes the file name and never the folders around it', () => {
    expect(proposedDestination(ROOT, '/a/deep/path/orders.json')).toBe(`${ROOT}/orders`)
    expect(proposedDestination(ROOT, 'orders.json')).toBe(`${ROOT}/orders`)
  })

  it('a name with no suffix at all is the name', () => {
    expect(proposedDestination(ROOT, '/work/orders')).toBe(`${ROOT}/orders`)
  })

  it('offers NOTHING when there is nothing to offer', () => {
    // No default location on this build — the backend answered '' — so
    // there is no directory to propose anything inside.
    expect(proposedDestination('', '/work/acme.postman_collection.json')).toBe('')
    // No file chosen yet.
    expect(proposedDestination(ROOT, '')).toBe('')
    // A name that is all suffix: a hidden file whose whole name is its
    // extensions leaves no stem, and half a path is not an offer.
    expect(proposedDestination(ROOT, '/work/.postman_collection.json')).toBe('')
    expect(proposedDestination(ROOT, '/work/')).toBe('')
  })

  it('joins with one separator however the root was spelled', () => {
    expect(proposedDestination(`${ROOT}/`, '/work/acme.json')).toBe(`${ROOT}/acme`)
  })

  it('reads a Windows path as a path', () => {
    // The picker answers whatever the platform spells. Splitting on one
    // separator would make `C:\\work\\acme.json` its own stem — a proposal
    // with a drive letter in the middle of it.
    expect(proposedDestination(ROOT, 'C:\\work\\acme.postman_collection.json')).toBe(`${ROOT}/acme`)
  })
})

describe('slugify and environmentPath — the offers a name makes', () => {
  it('a name becomes a file-safe stem', () => {
    expect(slugify('Acme API')).toBe('acme-api')
    expect(slugify('  Orders — v2  ')).toBe('orders-v2')
  })

  it('a name that slugs to nothing is no offer', () => {
    expect(slugify('—')).toBe('')
    expect(environmentPath('—')).toBe('')
  })

  it('an environment goes under environments/', () => {
    expect(environmentPath('Local')).toBe('environments/local.json')
  })
})
