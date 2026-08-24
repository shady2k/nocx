import { describe, expect, it } from 'vitest'
import { scanCss, kitIdentitySet } from './check-error-vocabulary.mjs'

/**
 * The error-vocabulary checker's own tests (nocx-8sudy).
 *
 * Commit 7ce9b934 removed eight surfaces' private error elements; this rule
 * is the gate that keeps a tenth from appearing. Both halves are required:
 * a class must NAME an error/refusal concept AND paint a danger token.
 * Kit identities are derived from src/ui/ and may declare their own error
 * states.
 */

/** The production identity set, derived from src/ui/ (the prefix is not
 *  the test): kit identities pass because the kit owns them, not because a
 *  fixture listed them. */
const KIT = kitIdentitySet()

describe('must trip', () => {
  it('flags a surface class named for a refusal that paints danger', () => {
    const hits = scanCss('connections.css', '.conn-refusal-message { color: var(--color-danger); }')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('.conn-refusal-message')
  })

  it('flags a surface class named for an error that paints danger', () => {
    const hits = scanCss('git.css', '.git-push-error { color: var(--color-error); }')
    expect(hits.length).toBe(1)
  })

  it('flags a hyphen-separated spelling of the refusal vocabulary', () => {
    const hits = scanCss('api.css', '.api-move-refused { color: var(--color-danger); }')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('.api-move-refused')
  })

  it('flags the token even with a fallback or inside color-mix (formatting is not an escape)', () => {
    expect(
      scanCss('x.css', '.cm-forwards-error { color: var( --color-error , transparent ); }').length,
    ).toBe(1)
    expect(
      scanCss(
        'backup.css',
        '.backup-restore-error { background: color-mix(in srgb, var(--color-danger), transparent 85%); }',
      ).length,
    ).toBe(1)
  })

  it('flags one error-named class in a selector list when the block paints danger', () => {
    const hits = scanCss('x.css', '.plain, .env-error { color: var(--color-danger); }')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('.env-error')
  })

  it('reports unparseable CSS rather than skipping it — fail closed', () => {
    const hits = scanCss('broken.css', 'a { color: red; } junk )')
    expect(hits.length).toBe(1)
    expect(hits[0].selector).toBe('<unparseable>')
  })
})

describe('must NOT trip', () => {
  it('lets kit identities alone — ui-field-error, ui-toast, ui-status-card', () => {
    expect(
      scanCss('components/field.css', '.ui-field-error { color: var(--color-error); }', KIT),
    ).toEqual([])
    expect(
      scanCss(
        'components/toast.css',
        ".ui-toast[data-level='danger'] { --toast-accent: var(--color-danger); }",
        KIT,
      ),
    ).toEqual([])
    expect(
      scanCss(
        'components/status-card.css',
        ".ui-status-card[data-tone='danger'] { border-left-color: var(--color-danger); }",
        KIT,
      ),
    ).toEqual([])
  })

  it('lets danger paint with a non-error name — a classification, not a refusal', () => {
    expect(
      scanCss('surfaces/connections.css', '.cm-impact-dangerous { color: var(--color-danger); }'),
    ).toEqual([])
    expect(
      scanCss(
        'components/overview.css',
        ".overview__card[data-state='failed'] { border-color: var(--color-danger); }",
      ),
    ).toEqual([])
  })

  it('lets an error-named class with no danger paint — the name alone is not the vocabulary', () => {
    expect(scanCss('surfaces/files.css', '.files-watch-error { display: flex; }')).toEqual([])
    expect(
      scanCss('surfaces/git.css', '.git-commit-output { max-height: 12rem; overflow: auto; }'),
    ).toEqual([])
  })
})
