import { describe, expect, it, afterEach } from 'vitest'
import { callOperationDeclaration } from '../generated/operations'
import { setActiveLocale } from '../i18n/locale-model'
import { en } from '../i18n/catalogues/en'
import { ru } from '../i18n/catalogues/ru'
import { waitingSentence } from './waiting-sentences'

afterEach(() => {
  setActiveLocale('en')
})

describe("the catalogue's waiting sentences", () => {
  it("has one English entry per method, equal to the catalogue's own words", () => {
    // ADR-8eej3e66 §2 gives every operation a `waiting` sentence, and the
    // Operations row shows it. The key set is derived from the catalogue, so a
    // sentence reworded in Go cannot leave a stale translation behind — this
    // is the check that makes a frontend-minted key honest.
    const methods = [
      'ack',
      'agent.ask',
      'files.list',
      'git.commit',
      'open',
      'settings.set',
      'vault.unseal',
    ]
    for (const method of methods) {
      const declaration = callOperationDeclaration(method)
      expect(declaration, method).toBeDefined()
      expect(en[`operations.waiting.${method}`], method).toBe(declaration!.waiting)
    }
  })

  it("renders the catalogue's sentence when no translation exists", () => {
    const declaration = callOperationDeclaration('files.list')!
    expect(waitingSentence('files.list', declaration.waiting)).toBe(declaration.waiting)
  })

  it('marks the sentence for translation rather than printing a key', () => {
    // Two halves, because the family is translated now (nocx-4ggge): under a
    // locale that HAS an entry the catalogue's own words are what is read, and
    // under one that has none the fallback is the English catalogue's value —
    // never the key and never blank. This test used to assert only the second
    // half, against `ru`, which was true only while `ru` had no entry.
    setActiveLocale('ru')
    const declaration = callOperationDeclaration('files.list')!
    expect(ru['operations.waiting.files.list'], 'files.list has no ru entry').toBeDefined()
    const translated = waitingSentence('files.list', declaration.waiting)
    expect(translated).toBe(ru['operations.waiting.files.list'])
    expect(translated).not.toBe(declaration.waiting)

    const unknown = waitingSentence('no.such.method', 'Doing something')
    expect(unknown).toBe('Doing something')
    expect(unknown).not.toContain('operations.waiting.')
  })
})
