// Generates frontend/src/generated/operations.ts from contracts/operations.json.
//
// This is the renderer's copy of each method's Operations metadata: the
// sentence shown while it waits and whether it may ever take a row. It has a
// user now — operations/call-operations.ts — and the catalogue remains the one
// declaration site (ADR-8eej3e66 §1). Generated and committed so a build never
// needs the Go toolchain.

import { readFile, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { format } from 'prettier'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '../..')
const manifestPath = resolve(root, 'contracts/operations.json')
const target = resolve(here, '../src/generated/operations.ts')

const BANNER = `/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operations.json, itself generated from
 * internal/operation/catalog. Regenerate: cd frontend && npm run contracts
 */`

async function build() {
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'))
  const methods = manifest.operations
    .filter((op) => op.kind === 'method')
    .sort((a, b) => a.id.localeCompare(b.id))
  const entries = methods.map(
    (op) =>
      `  ${JSON.stringify(op.id)}: { waiting: ${JSON.stringify(op.waiting)}, row: ${JSON.stringify(op.row)}, domain: ${JSON.stringify(op.dependencies[0] ?? '')}, turnMs: ${op.turn.millis}, maxMs: ${op.max.millis}, answerMs: ${op.answer.kind === 'deadline' ? op.answer.millis : 'null'} },`,
  )
  return `${BANNER}

export interface CallOperationDeclaration {
  /** The sentence shown while this call waits, in the person's terms. */
  readonly waiting: string
  /** When Operations may show it. */
  readonly row: 'never' | 'past-wait' | 'always'
  readonly domain: string
  readonly turnMs: number
  readonly maxMs: number
  readonly answerMs: number | null
}

const CALL_OPERATIONS: Readonly<Record<string, CallOperationDeclaration>> = {
${entries.join('\n')}
}

/** The declaration for a JSON-RPC method, or undefined where none exists. */
export function callOperationDeclaration(method: string): CallOperationDeclaration | undefined {
  return CALL_OPERATIONS[method]
}
`
}

const style = JSON.parse(await readFile(resolve(root, '.prettierrc.json'), 'utf8'))
const generated = await format(await build(), { ...style, parser: 'typescript' })
if (process.argv.includes('--check')) {
  const current = await readFile(target, 'utf8').catch(() => null)
  if (current !== generated) {
    console.error(
      'stale: frontend/src/generated/operations.ts does not match contracts/operations.json',
    )
    process.exit(1)
  }
} else {
  await writeFile(target, generated)
  console.log('contracts/operations.json → src/generated/operations.ts')
}
