// Generates the renderer's wire types from contracts/*.schema.json.
//
// The schema is the single declaration of every JSON-RPC result shape; this
// script is how the renderer gets its half. The output is committed so a build
// never depends on a generator being installed, and `--check` is what stops the
// committed copy drifting from the schema it came from.
//
// Why generated rather than hand-written: `vault.status` shipped without
// `defaultProvider` while the renderer's hand-written interface declared it and
// read it on every render. A hand-written type can want a field the wire does
// not carry. A generated one cannot.

import { readdir, readFile, writeFile, mkdir } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { compileFromFile } from 'json-schema-to-typescript'

const here = dirname(fileURLToPath(import.meta.url))
const contractsDir = resolve(here, '../../contracts')
const outDir = resolve(here, '../src/generated')

const BANNER = `/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/%SCHEMA%
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */`

function outputName(schemaFile) {
  return schemaFile.replace(/\.schema\.json$/, '.ts')
}

async function main() {
  const check = process.argv.includes('--check')
  const entries = (await readdir(contractsDir)).filter((f) => f.endsWith('.schema.json')).sort()

  if (entries.length === 0) {
    console.error('no *.schema.json under contracts/')
    process.exit(1)
  }

  await mkdir(outDir, { recursive: true })
  let stale = false

  for (const schemaFile of entries) {
    const generated = await compileFromFile(join(contractsDir, schemaFile), {
      bannerComment: BANNER.replace('%SCHEMA%', schemaFile),
      additionalProperties: false,
      style: { semi: false, singleQuote: true, printWidth: 100 },
    })
    const target = join(outDir, outputName(schemaFile))

    if (check) {
      const current = await readFile(target, 'utf8').catch(() => null)
      if (current !== generated) {
        console.error(`stale: ${outputName(schemaFile)} does not match ${schemaFile}`)
        stale = true
      }
      continue
    }

    await writeFile(target, generated)
    console.log(`${schemaFile} → src/generated/${outputName(schemaFile)}`)
  }

  if (stale) {
    console.error('\nRun `npm run contracts` and commit the result.')
    process.exit(1)
  }
}

await main()
