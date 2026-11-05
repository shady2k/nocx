// Generates frontend/src/generated/operations.ts from contracts/operations.json.
//
// The renderer's deadlines, waiting sentences and row rules are a projection of
// the one Go declaration site (ADR-8eej3e66 section 1). They are generated and
// committed so a build never depends on the Go toolchain, and `--check` is what
// stops the committed copy drifting from the catalogue.

import { readFile, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { validate } from './operations-contract.mjs'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '../..')
const manifestPath = resolve(root, 'contracts/operations.json')
const schemaPath = resolve(root, 'contracts/operations.schema.json')
const target = resolve(here, '../src/generated/operations.ts')

const BANNER = `/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operations.json, itself generated from
 * internal/operation/catalog. Regenerate: cd frontend && npm run contracts
 *
 * A deadline or sentence written by hand in a surface is a second declaration
 * site, which is the defect the catalogue exists to remove.
 */`

function deadline(d) {
  return `{ millis: ${d.millis}, basis: ${JSON.stringify(d.basis)} }`
}

function bound(b) {
  return `{ millis: ${b.kind === 'deadline' ? b.millis : 'null'}, basis: ${JSON.stringify(b.basis)} }`
}

function entry(op) {
  const waiting = JSON.stringify(op.waiting)
  const preempts = op.preempts.length === 0 ? '[]' : `[${op.preempts.map((p) => JSON.stringify(p)).join(', ')}]`
  return `  ${JSON.stringify(op.id)}: {
    kind: ${JSON.stringify(op.kind)},
    owner: ${JSON.stringify(op.owner)},
    turn: ${deadline(op.turn)},
    max: ${deadline(op.max)},
    silence: ${bound(op.silence)},
    answer: ${bound(op.answer)},
    recovery: ${bound(op.recovery)},
    merge: ${JSON.stringify(op.merge)},
    preempts: ${preempts},
    waiting: ${waiting},
    row: ${JSON.stringify(op.row)},
    expired: ${JSON.stringify(op.expired)},
  },`
}

async function build() {
  const [schemaRaw, manifestRaw] = await Promise.all([
    readFile(schemaPath, 'utf8'),
    readFile(manifestPath, 'utf8'),
  ])
  const schema = JSON.parse(schemaRaw)
  const manifest = JSON.parse(manifestRaw)
  validate(schema, manifest)
  const ops = [...manifest.operations].sort((a, b) => a.id.localeCompare(b.id))
  return `${BANNER}

export interface OperationDeadline {
  readonly millis: number
  readonly basis: 'read' | 'measured'
}

export interface OperationBound {
  readonly millis: number | null
  readonly basis: 'read' | 'measured' | ''
}

export interface OperationDeclaration {
  readonly kind: 'method' | 'internal'
  readonly owner: 'request' | 'session' | 'binding' | 'server' | 'checkout'
  readonly turn: OperationDeadline
  readonly max: OperationDeadline
  readonly silence: OperationBound
  readonly answer: OperationBound
  readonly recovery: OperationBound
  readonly merge: 'none' | 'latest-value' | 'same-key'
  readonly preempts: readonly string[]
  readonly waiting: string
  readonly row: 'never' | 'past-wait' | 'always'
  readonly expired: 'not-started' | 'outcome-unknown' | 'retry-read'
}

export const OPERATIONS: Readonly<Record<string, OperationDeclaration>> = {
${ops.map(entry).join('\n')}
}

/** The declared lifetime of one operation, or undefined where it is not declared. */
export function operationDeclaration(id: string): OperationDeclaration | undefined {
  return OPERATIONS[id]
}
`
}

const generated = await build()
if (process.argv.includes('--check')) {
  const current = await readFile(target, 'utf8').catch(() => null)
  if (current !== generated) {
    console.error('stale: frontend/src/generated/operations.ts does not match contracts/operations.json')
    process.exit(1)
  }
} else {
  await writeFile(target, generated)
  console.log('contracts/operations.json → src/generated/operations.ts')
}
