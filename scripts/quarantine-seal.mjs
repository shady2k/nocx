#!/usr/bin/env node
// A completion seal is written only after tee and the status write succeeded.
// Hashing detects later truncation; missing evidence never counts as a pass.
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, renameSync } from 'node:fs'
import { join } from 'node:path'
const [dir, name] = process.argv.slice(2)
const status = Number(readFileSync(join(dir, `${name}.status`), 'utf8').trim())
const body = readFileSync(join(dir, `${name}.log`))
if (!body.length) throw new Error('empty test log')
let report
if (process.env.NOCX_QUARANTINE_REPORT) {
  // Missing or malformed structured runner output is retained as incomplete,
  // never replaced by printed failure-looking text.
  try {
    report = JSON.parse(readFileSync(process.env.NOCX_QUARANTINE_REPORT, 'utf8'))
  } catch {
    report = null
  }
}
let wrapper
if (process.env.NOCX_QUARANTINE_WRAPPER_STATUS) {
  let commandStatus = null
  try {
    const raw = readFileSync(process.env.NOCX_QUARANTINE_WRAPPER_STATUS, 'utf8').trim()
    if (/^(0|[1-9]\d*)$/.test(raw)) commandStatus = Number(raw)
  } catch {
    /* Missing positive wrapper completion refuses named holds. */
  }
  wrapper = { required: true, status: commandStatus }
}
const target = join(dir, `${name}.complete`)
writeFileSync(
  `${target}.tmp`,
  JSON.stringify({
    status,
    bytes: body.length,
    sha256: createHash('sha256').update(body).digest('hex'),
    ...(report !== undefined ? { report } : {}),
    ...(wrapper ? { wrapper } : {}),
  }),
)
renameSync(`${target}.tmp`, target)
