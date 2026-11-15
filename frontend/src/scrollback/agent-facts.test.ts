// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { setActiveLocale } from '../i18n/locale-model'
import { appendAgentFacts } from './agent-facts'

afterEach(() => setActiveLocale('en'))

describe('agent usage facts', () => {
  it('localizes stated usage without estimating absent facts', () => {
    setActiveLocale('ru')
    const parent = document.createElement('div')
    appendAgentFacts(parent, undefined, { input: 104, output: 27, cacheRead: 40, cacheWrite: 8 })
    expect(parent.textContent).toBe('104 входных · 27 выходных · 40 из кеша')
    expect(parent.querySelector('.cmd-answer-provenance')).toBeNull()
  })

  it('leaves an older run without usage unadorned', () => {
    const parent = document.createElement('div')
    appendAgentFacts(parent)
    expect(parent.childElementCount).toBe(0)
  })
})
