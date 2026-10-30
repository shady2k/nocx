#!/usr/bin/env node
// Validate every byte before any remote mutation. The workflow supplies inputs;
// this module owns the moved/no-op decision and publication ordering.
import { readFileSync, readdirSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { createHash, createPublicKey, verify } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'
import { touchesProduct } from './product-change.mjs'

const fail = (message) => {
  throw new Error(message)
}
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex')
export function releaseKeys(path) {
  const text = readFileSync(path, 'utf8')
  const block = text.match(/var releaseKeys = \[\]string\{([\s\S]*?)\n\}/)?.[1]
  const keys = [...(block ?? '').matchAll(/"([A-Za-z0-9+/]{43}=)"/g)].map((m) => m[1])
  if (!keys.length) fail('release keyring is empty')
  return keys
}
export function verifyFile(dir, name, keys) {
  const body = readFileSync(join(dir, name))
  const encoded = readFileSync(join(dir, `${name}.sig`), 'utf8').trim()
  if (!/^[A-Za-z0-9+/]{86}==$/.test(encoded)) fail(`${name} signature is malformed`)
  const signature = Buffer.from(encoded, 'base64')
  const valid = keys.some((key) =>
    verify(
      null,
      body,
      createPublicKey({
        key: Buffer.concat([
          Buffer.from('302a300506032b6570032100', 'hex'),
          Buffer.from(key, 'base64'),
        ]),
        format: 'der',
        type: 'spki',
      }),
      signature,
    ),
  )
  if (!valid) fail(`${name} signature does not verify against the declared keyring`)
  return JSON.parse(body)
}
export function authorize({ ref, dryRun, linuxOnly }) {
  if (!dryRun && ref !== 'refs/heads/main') fail('publication requires main')
  if (!dryRun && linuxOnly) fail('publication requires both platforms')
  return { publish: !dryRun }
}
export function prepare({ sha, previous, files, version, date, runId }) {
  if (!/^[a-f0-9]{40}$/.test(sha)) fail('sha must be a full main revision')
  if (!/^\d+\.\d+\.\d+$/.test(version)) fail('VERSION must be the next stable version')
  if (!/^\d{8}$/.test(date) || !/^\d+$/.test(runId)) fail('invalid dated tag inputs')
  if (previous && previous !== sha && files.length === 0)
    fail('empty diff between different main revisions')
  return {
    moved: previous !== sha && (!previous || touchesProduct(files)),
    sha,
    version,
    tag: `nightly-${date}-${runId}`,
  }
}
export function attest(input) {
  const { dir, sha, version, tag, runId, runAttempt } = input
  const artifacts = readdirSync(dir, { withFileTypes: true })
    .map((entry) => {
      const path = join(dir, entry.name)
      if (entry.isDirectory()) fail(`${path} is a directory, not an artifact`)
      if (!entry.isFile()) fail(`${path} is not a regular file artifact`)
      return entry.name
    })
    .filter((name) => !['nightly.json', 'nightly.json.sig'].includes(name))
    .sort()
    .map((name) => {
      const bytes = readFileSync(join(dir, name))
      return { name, sha256: hash(bytes), size: bytes.length }
    })
  return { schema: 1, sha, version, tag, runId, runAttempt, artifacts }
}
// This is deliberately an adapter for the build identity's object/string
// schema, not a general JSON Schema validator. The contract owns its key set
// and patterns; publication adds only equality with the packaged descriptors.
// Refuse new constraints until this adapter knows how to enforce them.
function validateBuildIdentity(value, schema, path = 'manifest.build') {
  const keywords = {
    object: ['type', 'properties', 'required', 'additionalProperties'],
    string: ['type', 'pattern', 'format'],
  }[schema?.type]
  if (!keywords || Object.keys(schema).some((key) => !keywords.includes(key)))
    fail(`${path} schema has unsupported identity constraints`)
  if (schema.type === 'object') {
    const properties = schema.properties
    if (
      !properties ||
      schema.additionalProperties !== false ||
      !Array.isArray(schema.required) ||
      Object.keys(properties).some((key) => !schema.required.includes(key)) ||
      schema.required.some((key) => !Object.hasOwn(properties, key))
    )
      fail(`${path} schema must require every identity property and forbid extras`)
    if (value === null || typeof value !== 'object' || Array.isArray(value))
      fail(`${path} must be an object`)
    for (const key of schema.required) {
      if (!Object.hasOwn(value, key)) fail(`${path}.${key} is required`)
    }
    for (const key of Object.keys(value)) {
      if (!Object.hasOwn(properties, key)) fail(`${path}.${key} is not allowed`)
    }
    for (const [key, property] of Object.entries(properties))
      validateBuildIdentity(value[key], property, `${path}.${key}`)
    return
  }
  if (schema.pattern !== undefined && typeof schema.pattern !== 'string')
    fail(`${path} schema has an unsupported pattern`)
  if (schema.format !== undefined && schema.format !== 'date-time')
    fail(`${path} schema has an unsupported format`)
  if (typeof value !== 'string') fail(`${path} must be a string`)
  if (schema.pattern !== undefined && !new RegExp(schema.pattern).test(value))
    fail(`${path} does not match the identity contract`)
  if (schema.format === 'date-time') {
    // Date.parse normalizes nonexistent days. Round-tripping rejects those as
    // well as fractional seconds, offsets and missing zones: this identity is
    // the exact UTC-second value stamped into internal/version.Date.
    const parsed = new Date(value)
    if (!Number.isFinite(parsed.getTime()) || parsed.toISOString() !== `${value.slice(0, -1)}.000Z`)
      fail(`${path} must be a valid canonical UTC timestamp`)
  }
}

export function decide(input) {
  if (input.moved === false) return { published: false }
  if (input.moved !== true) fail('moved must be explicitly true or false')
  const { dir, version, sha, tag, keyring } = input
  if (!/^[a-f0-9]{40}$/.test(sha) || !/^nightly-\d{8}-\d+$/.test(tag))
    fail('invalid nightly identity')
  const manifest = verifyFile(dir, 'manifest.json', keyring)
  const contract = JSON.parse(
    readFileSync(new URL('../contracts/update/manifest.schema.json', import.meta.url), 'utf8'),
  )
  validateBuildIdentity(manifest.build, contract.properties?.build)
  if (manifest.build.commit !== sha) fail('manifest.build.commit differs from declared revision')
  if (manifest.version !== version) fail('manifest.version differs from declared version')
  for (const platform of ['darwin', 'linux']) {
    if (manifest.artifacts.filter((a) => a.os === platform).length !== 1)
      fail(`manifest.artifacts missing/duplicate ${platform}`)
  }
  for (const entry of [...manifest.artifacts, ...(manifest.helpers ?? [])]) {
    const url = new URL(entry.url)
    const name = basename(url.pathname)
    if (entry.url !== `https://github.com/shady2k/nocx/releases/download/${tag}/${name}`)
      fail(`${name} URL is not the immutable nightly URL`)
    const body = readFileSync(join(dir, name))
    if (body.length !== entry.size || hash(body) !== entry.sha256)
      fail(`${name} hash/size differs from manifest`)
  }
  // Reports are generated by executing the PACKAGED executables, not source
  // assertions. The signed provenance binds each report to these archive bytes.
  for (const platform of ['darwin', 'linux']) {
    const report = JSON.parse(readFileSync(join(dir, `verified-${platform}.json`), 'utf8'))
    const artifact = manifest.artifacts.find((a) => a.os === platform)
    if (
      report.archive?.name !== basename(new URL(artifact.url).pathname) ||
      report.archive?.sha256 !== artifact.sha256
    )
      fail(`verified-${platform}.archive differs from manifest`)
    for (const [field, want] of Object.entries({ version, commit: sha })) {
      if (report[field] !== want) fail(`verified-${platform}.${field} differs from declared value`)
    }
    for (const executable of ['nocx', 'nocx-server']) {
      const about = report.executables?.[executable]?.about
      if (about?.Version !== version)
        fail(`${platform}.${executable}.About.Version differs from declared value`)
      if (about?.Commit !== sha)
        fail(`${platform}.${executable}.About.Commit differs from declared value`)
      if (about?.Date !== manifest.build.builtAt)
        fail(`${platform}.${executable}.About.Date differs from manifest.build.builtAt`)
      if (!about?.Wails || /unknown|my company|my product/i.test(about.Wails))
        fail(`${platform}.${executable}.About.Wails is a placeholder`)
    }
    if (
      platform === 'darwin' &&
      report.identity?.CFBundleIdentifier !== report.declaredIdentity?.CFBundleIdentifier
    )
      fail('darwin.CFBundleIdentifier differs from declared value')
    if (!report.updater?.signature || !report.updater?.sha256 || !report.updater?.manifest?.version)
      fail(`${platform}.updater has no live verified manifest evidence`)
  }
  for (const name of [
    `nocx-${version}-darwin-universal.dmg`,
    ...['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64'].map(
      (p) => `nocx-helper-${version}-${p}`,
    ),
  ]) {
    if (!readFileSync(join(dir, name)).length) fail(`${name} is empty`)
  }
  const provenance = verifyFile(dir, 'nightly.json', keyring)
  for (const [field, want] of Object.entries({
    sha,
    version,
    tag,
    runId: input.runId,
    runAttempt: input.runAttempt,
  })) {
    if (provenance[field] !== want) fail(`nightly.${field} differs from declared value`)
  }
  if (JSON.stringify(provenance) !== JSON.stringify(attest(input)))
    fail('nightly.artifacts differ from signed provenance')
  return {
    published: true,
    files: readdirSync(dir)
      .sort()
      .map((name) => join(dir, name)),
  }
}
async function pointAlias(run, sha) {
  const refs = JSON.parse(
    await run(['api', 'repos/{owner}/{repo}/git/matching-refs/tags/nightly']),
  ).flat()
  if (refs.some((ref) => ref.ref === 'refs/tags/nightly'))
    await run([
      'api',
      '--method',
      'PATCH',
      'repos/{owner}/{repo}/git/refs/tags/nightly',
      '-f',
      `sha=${sha}`,
      '-F',
      'force=true',
    ])
  else
    await run([
      'api',
      '--method',
      'POST',
      'repos/{owner}/{repo}/git/refs',
      '-f',
      'ref=refs/tags/nightly',
      '-f',
      `sha=${sha}`,
    ])
}
export async function publish(
  input,
  run = (args) =>
    execFileSync('gh', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] }),
) {
  const plan = decide(input)
  if (!plan.published) return plan
  const notes = `Built from main ${input.sha}. Signed update manifest; macOS first launch needs the quarantine step. No notarization.`
  // Dated copy first. If it fails, the existing moving nightly stays untouched.
  await run([
    'release',
    'create',
    input.tag,
    '--target',
    input.sha,
    '--prerelease',
    '--latest=false',
    '--title',
    input.tag,
    '--notes',
    notes,
    ...plan.files,
  ])
  // GitHub cannot atomically replace a release's assets. Preserve the old
  // alias locally and restore it on any failed replacement; immutable copies
  // remain available even if GitHub also refuses the recovery request.
  const releases = JSON.parse(
    await run(['api', '--paginate', '--slurp', 'repos/{owner}/{repo}/releases?per_page=100']),
  ).flat()
  const prior = releases.find((r) => r.tag_name === 'nightly' && !r.draft)
  const backup = mkdtempSync(join(tmpdir(), 'nocx-nightly-'))
  try {
    let oldFiles = []
    let oldProvenance
    if (prior) {
      await run(['release', 'download', 'nightly', '--dir', backup])
      oldProvenance = verifyFile(backup, 'nightly.json', input.keyring)
      oldFiles = readdirSync(backup).map((name) => join(backup, name))
    }
    try {
      if (prior) await run(['release', 'delete', 'nightly', '--yes'])
      await pointAlias(run, input.sha)
      await run([
        'release',
        'create',
        'nightly',
        '--target',
        input.sha,
        '--prerelease',
        '--latest=false',
        '--title',
        'Nightly',
        '--notes',
        notes,
        ...plan.files,
      ])
    } catch (cause) {
      // The failed create can leave a draft/tag. Inspect rather than hiding an
      // API failure behind a catch-all "not found".
      const partial = JSON.parse(
        await run(['api', '--paginate', '--slurp', 'repos/{owner}/{repo}/releases?per_page=100']),
      ).flat()
      if (partial.some((r) => r.tag_name === 'nightly'))
        await run(['release', 'delete', 'nightly', '--yes'])
      if (prior) await pointAlias(run, oldProvenance.sha)
      if (prior)
        await run([
          'release',
          'create',
          'nightly',
          '--target',
          oldProvenance.sha,
          '--prerelease',
          '--latest=false',
          '--title',
          prior.name || 'Nightly',
          '--notes',
          prior.body || '',
          ...oldFiles,
        ])
      throw new Error(
        `nightly alias publication failed; ${prior ? 'previous signed nightly restored' : 'no previous nightly'}: ${cause.message}`,
        { cause },
      )
    }
  } finally {
    rmSync(backup, { recursive: true, force: true })
  }
  const dated = releases
    .filter((r) => /^nightly-\d{8}-\d+$/.test(r.tag_name) && r.prerelease && !r.draft)
    .sort((a, b) => b.published_at.localeCompare(a.published_at))
  // The new dated copy is not in the previously fetched list in test fixtures;
  // exclude it explicitly, retaining six previous copies plus the new one.
  for (const old of dated.filter((r) => r.tag_name !== input.tag).slice(6)) {
    await run(['release', 'delete', old.tag_name, '--yes', '--cleanup-tag'])
  }
  return { published: true }
}
async function main() {
  const [mode, path] = process.argv.slice(2)
  const input = JSON.parse(readFileSync(path, 'utf8'))
  if (mode === 'prepare') console.log(JSON.stringify(prepare(input)))
  else if (mode === 'attest')
    writeFileSync(join(input.dir, 'nightly.json'), JSON.stringify(attest(input), null, 2) + '\n')
  else if (mode === 'publish') {
    if (input.moved) input.keyring = releaseKeys(input.keyringPath)
    console.log(JSON.stringify(await publish(input)))
  } else fail('usage: publish-nightly.mjs prepare|attest|publish input.json')
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  main().catch((error) => {
    console.error(`publish-nightly: ${error.message}`)
    process.exitCode = 1
  })
