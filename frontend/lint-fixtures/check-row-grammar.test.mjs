import { describe, expect, it } from 'vitest'
import { scanCss } from './check-row-grammar.mjs'

/**
 * The row-grammar checker's own tests (nocx-pp3y.3, acceptance 4).
 *
 * The kit now owns "describe a record in a row" through RecordRow's typed
 * slots (title, one kind badge, meta text, status). A surface that declares
 * its own name/meta grammar — a `*-item-name` / `*-item-meta` family — is
 * building the second dialect this bead exists to make impossible. The rule:
 * a CSS class segment naming a composite-owned concept (`name`, `meta`)
 * under an `item`/`row` row prefix is a violation, wherever in styles/ it
 * is declared.
 *
 * The one pre-existing family (the Git panel's dense `git-log-row__meta`,
 * whose row predates the composite and whose meta line carries multiple
 * ref badges — not the composite's single-meta shape) is grandfathered in
 * the baseline; the checker proves no NEW family appears.
 */

describe('must trip', () => {
  it('flags a new *-item-name family in a surface stylesheet', () => {
    const hits = scanCss('connections.css', '.foo-item-name { font-weight: 600; }')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('.foo-item-name')
  })

  it('flags *-item-meta — the other half of the dialect', () => {
    const hits = scanCss('endpoints.css', '.foo-item-meta { display: flex; }')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('.foo-item-meta')
  })

  it('flags a -row- prefixed family too (git-log-row__meta is the shape)', () => {
    const hits = scanCss('surfaces/git.css', '.bar-row__meta { display: flex; }')
    expect(hits.length).toBe(1)
  })

  it('flags the family even when a child part follows the concept', () => {
    const hits = scanCss('x.css', '.foo-item-name__inner { color: red; }')
    expect(hits.length).toBe(1)
  })

  it('flags the pre-existing git commit row family too — the BASELINE is what keeps the tree green', () => {
    // scanCss reports every family; the real-tree run filters the one
    // grandfathered entry (git-log-row__meta) through row-grammar-baseline.json.
    // A unit test cannot see the baseline, and a checker that hid it here
    // would also hide the next dialect — the separation is the point.
    const hits = scanCss('surfaces/git.css', '.git-log-row__meta { display: flex; }')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('.git-log-row__meta')
  })
})

describe('must NOT trip', () => {
  it('lets the kit and the composite alone', () => {
    expect(
      scanCss('components/record-row.css', '.ui-record-row__title { font-weight: 500; }'),
    ).toEqual([])
    expect(scanCss('components/collection-view.css', '.ui-collection-row { padding: 0; }')).toEqual(
      [],
    )
    expect(scanCss('components/status-dot.css', '.ui-status-dot { width: 8px; }')).toEqual([])
  })

  it('lets unrelated surface classes alone — including the record row that uses the composite', () => {
    expect(scanCss('surfaces/secrets.css', '.sr-row-info { display: flex; }')).toEqual([])
    expect(
      scanCss('surfaces/connections.css', '.cm-tip { color: var(--color-text-muted); }'),
    ).toEqual([])
  })
})
