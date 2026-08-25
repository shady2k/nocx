// The offers this module makes, at their edges.
//
// Every function here fills a field in while nothing has been typed into it,
// so the case that matters most is the one where there is NOTHING to offer:
// a surface that proposed a half-formed path would be worse than one that
// proposed nothing, because the person would have to notice and undo it.

import { describe, expect, it } from 'vitest'
import {
  classifyPastedSource,
  environmentPath,
  proposedDestination,
  proposedDestinationFromDocument,
  proposedDestinationFromURL,
  proposedRequestName,
  slugify,
} from './api-paths'

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

describe('classifyPastedSource — what a pasted string IS, asked in one place', () => {
  it('calls http and https a URL, whatever the case and the surrounding space', () => {
    expect(classifyPastedSource('  https://h/a.json \n')).toEqual({
      kind: 'url',
      url: 'https://h/a.json',
    })
    expect(classifyPastedSource('HTTP://h/a.json')).toEqual({ kind: 'url', url: 'HTTP://h/a.json' })
  })

  it('calls JSON a document', () => {
    expect(classifyPastedSource(' {"info":{}} ')).toEqual({
      kind: 'document',
      document: '{"info":{}}',
    })
    expect(classifyPastedSource('[]')).toEqual({ kind: 'document', document: '[]' })
  })

  it('calls anything else unusable — including a curl line this ask never offered', () => {
    // `unusable` is an answer and not an error: the ask says one sentence
    // and spends no round trip learning what the form already knew.
    expect(classifyPastedSource('curl https://h -X POST')).toEqual({ kind: 'unusable' })
    expect(classifyPastedSource('')).toEqual({ kind: 'unusable' })
    expect(classifyPastedSource('   ')).toEqual({ kind: 'unusable' })
  })
})

describe('proposedDestinationFromDocument — what a pasted export proposes', () => {
  it('offers the collection name, slugified', () => {
    expect(proposedDestinationFromDocument('/root', '{"info":{"name":"Acme API"}}')).toBe(
      '/root/acme-api',
    )
  })

  it('offers nothing rather than throwing, for everything it cannot read', () => {
    // It reads one field and validates nothing — the backend is the only
    // reader of hostile input — so every failure is the same empty offer.
    expect(proposedDestinationFromDocument('/root', 'not json')).toBe('')
    expect(proposedDestinationFromDocument('/root', '{"info":{}}')).toBe('')
    expect(proposedDestinationFromDocument('/root', '{"info":{"name":"***"}}')).toBe('')
    expect(proposedDestinationFromDocument('', '{"info":{"name":"Acme"}}')).toBe('')
  })
})

describe('proposedDestinationFromURL — what a pasted link proposes', () => {
  it('offers the last segment without its suffixes', () => {
    expect(proposedDestinationFromURL('/root', 'https://h/x/acme.postman_collection.json')).toBe(
      '/root/acme',
    )
    expect(
      proposedDestinationFromURL('/root', 'https://api.postman.com/collections/1234-abc'),
    ).toBe('/root/1234-abc')
  })

  it('offers nothing when there is no last segment to take', () => {
    expect(proposedDestinationFromURL('/root', 'https://h/')).toBe('')
    expect(proposedDestinationFromURL('/root', 'not a url')).toBe('')
  })
})

describe('proposedRequestName — what a request calls itself while nobody has', () => {
  it('is the method and the last path segment, which is what the crumbs read', () => {
    expect(proposedRequestName('POST', 'http://127.0.0.1:8080/v1/broker-access')).toBe(
      'POST broker-access',
    )
    expect(proposedRequestName('GET', 'https://api.example.test/users')).toBe('GET users')
  })

  it('reads an address written against an environment, which is most of them', () => {
    // `{{baseUrl}}/users` is not a URL any parser accepts and it is what a
    // Postman export holds, so the segments are taken syntactically.
    expect(proposedRequestName('GET', '{{baseUrl}}/users')).toBe('GET users')
    expect(proposedRequestName('DELETE', '{{baseUrl}}/users/{{id}}')).toBe('DELETE users')
  })

  it('ignores the query and the fragment — they are not what a request is called', () => {
    expect(proposedRequestName('GET', 'https://h/orders?page=2')).toBe('GET orders')
    expect(proposedRequestName('GET', 'https://h/orders#top')).toBe('GET orders')
    expect(proposedRequestName('GET', 'https://h/orders/?page=2')).toBe('GET orders')
  })

  it('offers NOTHING rather than something wrong', () => {
    // A host is not a path segment: `POST 127.0.0.1:8080` names the machine
    // and not the call.
    expect(proposedRequestName('POST', 'http://127.0.0.1:8080')).toBe('')
    expect(proposedRequestName('GET', 'https://api.example.test/')).toBe('')
    // Nothing typed yet, which is every request the moment it is made.
    expect(proposedRequestName('GET', '')).toBe('')
    expect(proposedRequestName('GET', '   ')).toBe('')
    // Every segment is a reference: there is no word in it that a person
    // would recognise as the name of this call.
    expect(proposedRequestName('GET', '{{baseUrl}}')).toBe('')
    expect(proposedRequestName('GET', '{{baseUrl}}/{{id}}')).toBe('')
  })

  it('a method nobody named leaves the segment standing alone', () => {
    expect(proposedRequestName('', 'https://h/orders')).toBe('orders')
  })
})
