// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { parseOsc7 } from './xterm'

describe('parseOsc7', () => {
  it('parses a local file:/// path (empty host)', () => {
    const result = parseOsc7('file:///Users/shady/projects')
    expect(result).toEqual({ host: '', path: '/Users/shady/projects' })
  })

  it('parses a file://host/path with hostname', () => {
    const result = parseOsc7('file://macbook.local/Users/shady')
    expect(result).toEqual({ host: 'macbook.local', path: '/Users/shady' })
  })

  it('parses file://localhost/path', () => {
    const result = parseOsc7('file://localhost/tmp')
    expect(result).toEqual({ host: 'localhost', path: '/tmp' })
  })

  it('percent-decodes the host', () => {
    const result = parseOsc7('file://my%20host.local/path')
    expect(result).toEqual({ host: 'my host.local', path: '/path' })
  })

  it('percent-decodes the path', () => {
    const result = parseOsc7('file://host/Users/shady/My%20Documents')
    expect(result).toEqual({ host: 'host', path: '/Users/shady/My Documents' })
  })

  it('percent-decodes both host and path', () => {
    const result = parseOsc7('file://my%20mac/Users/shady/project%20name')
    expect(result).toEqual({ host: 'my mac', path: '/Users/shady/project name' })
  })

  it('returns null for non-file:// payloads', () => {
    expect(parseOsc7('not-a-file-uri')).toBeNull()
    expect(parseOsc7('')).toBeNull()
    expect(parseOsc7('http://example.com/path')).toBeNull()
  })

  it('returns null for file:// with no path separator', () => {
    expect(parseOsc7('file://justhost')).toBeNull()
  })

  it('returns null for malformed percent-encoding', () => {
    // '%ZZ' is not valid percent-encoding
    expect(parseOsc7('file:///tmp/%ZZ')).toBeNull()
    // incomplete percent sequence
    expect(parseOsc7('file:///tmp/%')).toBeNull()
  })

  it('handles deeply nested paths', () => {
    const result = parseOsc7('file:///a/b/c/d/e/f/g')
    expect(result).toEqual({ host: '', path: '/a/b/c/d/e/f/g' })
  })

  it('handles root path', () => {
    const result = parseOsc7('file:///')
    expect(result).toEqual({ host: '', path: '/' })
  })
})
