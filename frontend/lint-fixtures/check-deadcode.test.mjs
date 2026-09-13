import { describe, expect, it } from 'vitest'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { analyzeModule, rootsFileName } from '../../.githooks/check-deadcode.mjs'

/**
 * The ratchet's cgo rule, held at both ends (nocx-ygxjv.16).
 *
 * RTA cannot see a C-to-Go call, so deadcode reports every `//export`ed
 * callback dead — and with it everything only the callback reaches, which is
 * the half that matters (ghostty's copyBorrowed and lookup are called by the
 * six nocxGo* callbacks and by nothing else). The rule is structural: the
 * callbacks are derived from the source, rendered into an overlay root file,
 * and the analyser is left to do the reaching.
 *
 * Two failures are possible and this file has to catch both:
 *
 *   - Saving too little, which is the finding: the callback is still reported,
 *     or its helper is.
 *   - Saving too much, which would be worse: a genuinely dead function inside a
 *     cgo package, or a `//export` comment in a file cgo does not read the
 *     directive from, leaving the gate green over dead code.
 *
 * The analyser here is a model (deadcode-model.mjs) because ci-frontend has no
 * Go toolchain; the real one runs the same pipeline in ci-mac and ci-linux.
 * The fixture is a module of its own so that `go list ./...` in this repository
 * never compiles it.
 */
const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), 'deadcode-cgo-roots-fixture')
const MODEL = join(dirname(fileURLToPath(import.meta.url)), 'deadcode-model.mjs')

const analysis = analyzeModule({ GOOS: 'linux', GOARCH: 'amd64' }, '', {
  root: FIXTURE,
  deadcodeCmd: MODEL,
})

const reported = analysis.violations.map((v) => `${v.file}:${v.func}`)

describe('cgo //export callbacks are roots', () => {
  it('does not report an //export’ed callback', () => {
    expect(reported).not.toContain('callback.go:fixtureGoBell')
    expect(reported).not.toContain('callback.go:fixtureGoTitle')
  })

  it('does not report what only the callback reaches — the half a suppression would leave behind', () => {
    expect(reported).not.toContain('callback.go:fixtureCopy')
    expect(reported).not.toContain('callback.go:fixtureRemember')
  })

  it('STILL reports a genuinely dead function inside the cgo package', () => {
    expect(reported).toContain('callback.go:fixtureDeadHelper')
  })

  it('STILL reports an //export comment in a file that does not import "C"', () => {
    expect(reported).toContain('plain.go:fixtureNotACallback')
  })

  it('reports exactly those two, so nothing else in the module was swallowed', () => {
    expect(reported).toEqual(['callback.go:fixtureDeadHelper', 'plain.go:fixtureNotACallback'])
  })
})

describe('the rendered root a callback gets', () => {
  it('names every file that declares a callback', () => {
    expect(analysis.roots.map((c) => c.rel)).toEqual(['callback.go', 'gated_darwin.go'])
  })

  it('keeps a platform-gated file’s suffix, so the root is not compiled on every platform', () => {
    // Go infers a build constraint from the file NAME, so a root rendered as
    // `zz_deadcode_cgo_roots.go` would be compiled on linux and would make a
    // darwin-only callback live in an analysis that never sees its file.
    expect(rootsFileName('/pkg/gated_darwin.go')).toBe('zz_deadcode_cgo_roots_gated_darwin.go')
    expect(rootsFileName('/pkg/gated_linux_amd64.go')).toBe(
      'zz_deadcode_cgo_roots_gated_linux_amd64.go',
    )
  })
})
