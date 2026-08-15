// The submit gate has ONE owner — the kit. These are structural guards for
// the acceptance "no surface calls revealAll() itself, and connections.tsx no
// longer defines a private gate": a surface that re-implements the refusal
// locally would not be caught by a user-seam test, because the defect is that
// the surface owns the logic at all.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const src = dirname(fileURLToPath(import.meta.url))

describe('the submit gate lives in the kit, not in the surfaces', () => {
  for (const file of ['connections.tsx', 'secrets.tsx']) {
    const source = readFileSync(join(src, file), 'utf8')
    it(`${file} does not call revealAll() itself`, () => {
      expect(source).not.toMatch(/\.revealAll\(\)/)
    })
    it(`${file} refuses submits through createSubmitGate`, () => {
      expect(source).toMatch(/createSubmitGate/)
    })
  }

  it('connections.tsx no longer defines a private gate', () => {
    const source = readFileSync(join(src, 'connections.tsx'), 'utf8')
    expect(source).not.toMatch(/function gate\(/)
  })
})
