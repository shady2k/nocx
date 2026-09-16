import { describe, expect, it } from 'vitest'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { checkCSSIntegrity } from './check-css-integrity.mjs'

/**
 * undefined-var, both directions (nocx-9bpeq.2).
 *
 * A fallback used to exempt a reference outright, so `var(--radius-sm, 4px)` —
 * a token that never existed — rendered its fallback for months with the gate
 * green. A fallback is still the right shape for a property set from script
 * (`style.setProperty('--sidebar-width', …)`), and a rule that reported those
 * would be turned off. So the rule is exact: a fallback reference is reported
 * when no stylesheet declares the name AND no source sets it.
 */

const here = fileURLToPath(new URL('.', import.meta.url))
const fixture = resolve(here, 'css-integrity-fixture')

const undefinedVarDetails = () =>
  checkCSSIntegrity({
    entry: resolve(fixture, 'entry.css'),
    stylesDir: resolve(fixture, 'styles'),
    uiDir: resolve(fixture, 'ui'),
  })
    .filter((v) => v.rule === 'undefined-var')
    .map((v) => v.detail)

describe('undefined-var', () => {
  it('reports a bare reference to a property nothing declares', () => {
    expect(undefinedVarDetails().some((d) => d.includes('--fixture-never-declared)'))).toBe(true)
  })

  it('reports a fallback reference to a property nothing declares and nothing sets', () => {
    expect(undefinedVarDetails().some((d) => d.includes('--fixture-also-never-declared'))).toBe(
      true,
    )
  })

  it('does not report a fallback reference to a property a source file sets', () => {
    expect(undefinedVarDetails().some((d) => d.includes('--fixture-runtime-width'))).toBe(false)
  })
})
