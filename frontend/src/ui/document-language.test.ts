import { describe, expect, it } from 'vitest'
import { basenameOf, extensionOf, languageForPath } from './document-language'

describe('extensionOf', () => {
  it('returns the lowercased final extension', () => {
    expect(extensionOf('/srv/etc/nginx.conf')).toBe('.conf')
    expect(extensionOf('/srv/etc/NGINX.CONF')).toBe('.conf')
    expect(extensionOf('/home/dev/main.tsx')).toBe('.tsx')
  })

  it('treats a leading dot as a hidden file, not an extension', () => {
    expect(extensionOf('/home/dev/.bashrc')).toBe('')
    expect(extensionOf('.gitignore')).toBe('')
  })

  it('returns empty for an extensionless name', () => {
    expect(extensionOf('/etc/hostname')).toBe('')
    expect(extensionOf('Makefile')).toBe('')
  })
})

describe('basenameOf', () => {
  it('takes the last segment for either separator', () => {
    expect(basenameOf('/srv/etc/nginx.conf')).toBe('nginx.conf')
    expect(basenameOf('C:\\Users\\dev\\notes.md')).toBe('notes.md')
  })
})
describe('languageForPath', () => {
  // lang-* factories return a LanguageSupport object; the plain-text fallback
  // is the empty array. An object or a non-empty array means "has a language".
  const hasLanguage = (path: string): boolean => {
    const ext = languageForPath(path)
    return !Array.isArray(ext) || ext.length > 0
  }

  it('covers the formats that turn up in terminal work', () => {
    expect(hasLanguage('a.json')).toBe(true)
    expect(hasLanguage('a.yaml')).toBe(true)
    expect(hasLanguage('a.yml')).toBe(true)
    expect(hasLanguage('a.md')).toBe(true)
    expect(hasLanguage('a.markdown')).toBe(true)
    expect(hasLanguage('a.sh')).toBe(true)
    expect(hasLanguage('a.bash')).toBe(true)
    expect(hasLanguage('a.zsh')).toBe(true)
    expect(hasLanguage('a.go')).toBe(true)
    expect(hasLanguage('a.ts')).toBe(true)
    expect(hasLanguage('a.tsx')).toBe(true)
    expect(hasLanguage('a.js')).toBe(true)
    expect(hasLanguage('a.jsx')).toBe(true)
    expect(hasLanguage('a.mjs')).toBe(true)
    expect(hasLanguage('a.cjs')).toBe(true)
    expect(hasLanguage('a.py')).toBe(true)
  })

  it('falls back to plain text (no extension) for everything else', () => {
    // The fallback is the correct answer, not a gap: config files, logs and
    // unknown suffixes render exactly as they are, unhighlighted.
    expect(languageForPath('/etc/hostname')).toEqual([])
    expect(languageForPath('a.conf')).toEqual([])
    expect(languageForPath('a.txt')).toEqual([])
    expect(languageForPath('a.log')).toEqual([])
    expect(languageForPath('a.sql')).toEqual([])
    expect(languageForPath('Makefile')).toEqual([])
    expect(languageForPath('/home/dev/.bashrc')).toEqual([])
  })

  it('does not extend by directory segment', () => {
    // A directory named "config.json" must not light up as JSON.
    expect(languageForPath('/srv/config.json/notes')).toEqual([])
  })
})
