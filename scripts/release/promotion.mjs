// Promotion verifies authenticated nightly bytes. It never compiles, packages or signs code.
import { createHash, createPrivateKey, createPublicKey, sign, verify } from 'node:crypto'
import {
  copyFileSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  writeFileSync,
} from 'node:fs'
import { join } from 'node:path'

const versionPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/
const shaPattern = /^[0-9a-f]{40}$/
const hashPattern = /^[0-9a-f]{64}$/
const publicPrefix = Buffer.from('302a300506032b6570032100', 'hex')
const privatePrefix = Buffer.from('302e020100300506032b657004220420', 'hex')
export const sha256 = (body) => createHash('sha256').update(body).digest('hex')
const requireThat = (condition, message) => {
  if (!condition) throw new Error(message)
}

function rawBase64(text, size, name) {
  const value = String(text).trim()
  const bytes = Buffer.from(value, 'base64')
  requireThat(
    bytes.length === size && bytes.toString('base64') === value,
    `${name}: invalid base64 or length`,
  )
  return bytes
}

// The nightly producer signs its workflow run id and attempt as the decimal
// strings GitHub hands it in GITHUB_RUN_ID / GITHUB_RUN_ATTEMPT
// (publish-nightly.mjs). The signed provenance is the contract, so this reads
// that form and nothing else: a number here would be a document the producer
// never wrote.
export function signedRunNumber(value) {
  const ok =
    typeof value === 'string' && /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value))
  return ok ? Number(value) : undefined
}

// The candidate's Go keyring is the source of truth. No second public-key list.
export function readKeyring(source) {
  const block = /var releaseKeys = \[\]string\{([\s\S]*?)\n\}/.exec(source)?.[1]
  requireThat(block !== undefined, 'candidate release keyring is absent')
  // One left-to-right pass, the way Go's scanner reads it: a string literal
  // is taken whole before a comment can start, because base64 carries "/" and
  // a key holding "//" is still one key. Stripping comments first cut such a
  // key in half and read the keyring as empty.
  const entries = []
  const rest = block.replace(/"([^"\n]+)"|\/\/[^\n]*/g, (_, literal) => {
    if (literal !== undefined) entries.push(literal)
    return ''
  })
  requireThat(entries.length > 0, 'candidate release keyring is empty')
  requireThat(rest.replace(/[\s,]/g, '') === '', 'unsupported candidate release keyring syntax')
  return entries.map((entry) =>
    createPublicKey({
      key: Buffer.concat([publicPrefix, rawBase64(entry, 32, 'release public key')]),
      format: 'der',
      type: 'spki',
    }),
  )
}

export function signingKey(seed) {
  return createPrivateKey({
    key: Buffer.concat([privatePrefix, rawBase64(seed, 32, 'RELEASE_SIGNING_KEY')]),
    format: 'der',
    type: 'pkcs8',
  })
}

export function verifySignature(body, signature, keys) {
  const raw = rawBase64(signature, 64, 'signature')
  requireThat(
    keys.length > 0 && keys.some((key) => verify(null, body, key, raw)),
    'signature is not valid under the candidate release keyring',
  )
}

export function authorize({ actor, triggeringActor, owner, ref, tried }) {
  requireThat(
    actor === owner && triggeringActor === owner,
    'only the repository owner may promote or rerun promotion',
  )
  requireThat(ref === 'refs/heads/main', 'promotion must run from main')
  requireThat(tried === true, 'owner must confirm they tried the named nightly')
}

export function payloadNames(version) {
  return [
    `nocx-${version}-darwin-universal.zip`,
    `nocx-${version}-darwin-universal.dmg`,
    `nocx-${version}-linux-amd64.AppImage`,
    ...['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64'].map(
      (platform) => `nocx-helper-${version}-${platform}`,
    ),
  ]
}

function describeEntries(entries) {
  requireThat(Array.isArray(entries) && entries.length > 0, 'nightly artifact inventory is empty')
  const byName = new Map()
  for (const entry of entries) {
    requireThat(
      /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(entry.name) && entry.name !== '..',
      'unsafe nightly artifact name',
    )
    requireThat(!byName.has(entry.name), `duplicate nightly artifact ${entry.name}`)
    requireThat(
      hashPattern.test(entry.sha256) && Number.isSafeInteger(entry.size) && entry.size > 0,
      `invalid digest or size: ${entry.name}`,
    )
    byName.set(entry.name, entry)
  }
  return byName
}

function verifyBody(body, entry) {
  requireThat(
    entry && sha256(body) === entry.sha256 && body.length === entry.size,
    `digest or size changed: ${entry?.name ?? 'missing inventory entry'}`,
  )
}

function versionParts(version) {
  return version.split('.').map(Number)
}
export function isNewerVersion(version, previous) {
  requireThat(
    versionPattern.test(version) && versionPattern.test(previous),
    'stable version must be MAJOR.MINOR.PATCH',
  )
  const before = versionParts(previous)
  for (const [i, part] of versionParts(version).entries()) {
    if (part !== before[i]) return part > before[i]
  }
  return false
}

export function verifyCandidate(input) {
  const {
    repo,
    tag,
    sha,
    keys,
    nightlyBody,
    nightlySignature,
    manifestBody,
    manifestSignature,
    release,
    run,
  } = input
  requireThat(repo === 'shady2k/nocx', 'unexpected promotion repository')
  // Both dated spellings are promotable: the old `nightly-<date>-<run>` copies
  // are published until the pruner retires them, and the new
  // `v<version>-nightly.<build>` is what publication creates now
  // (nocx-vh2hr.1.17). A stable `vX.Y.Z` tag and the moving alias are not.
  requireThat(
    /^(?:nightly-\d{8}-\d+|v\d+\.\d+\.\d+-nightly\.\d{12})$/.test(tag),
    'name an immutable dated nightly, not the moving nightly alias',
  )
  requireThat(shaPattern.test(sha), 'nightly tag must resolve to a full commit SHA')
  requireThat(
    release?.tag_name === tag && release.draft === false && release.prerelease === true,
    'candidate is not a published nightly prerelease',
  )
  verifySignature(nightlyBody, nightlySignature, keys)
  verifySignature(manifestBody, manifestSignature, keys)
  const nightly = JSON.parse(nightlyBody)
  const manifest = JSON.parse(manifestBody)
  requireThat(
    nightly.schema === 1 && nightly.sha === sha && nightly.tag === tag,
    'nightly provenance/tag commit mismatch',
  )
  requireThat(
    versionPattern.test(nightly.version) && manifest.version === nightly.version,
    'nightly version does not match its manifest',
  )
  const version = nightly.version
  requireThat(
    isNewerVersion(version, input.previousVersion),
    `v${version} is not newer than stable v${input.previousVersion}`,
  )
  requireThat(
    signedRunNumber(nightly.runId) !== undefined &&
      signedRunNumber(nightly.runAttempt) !== undefined &&
      run?.id === signedRunNumber(nightly.runId) &&
      run.run_attempt === signedRunNumber(nightly.runAttempt) &&
      run.head_sha === sha &&
      run.head_branch === 'main' &&
      ['schedule', 'workflow_dispatch'].includes(run.event) &&
      run.path === '.github/workflows/nightly.yml' &&
      run.status === 'completed' &&
      run.conclusion === 'success',
    'nightly has no matching successful verified workflow run',
  )
  const status = input.readme.split(/\r?\n/).filter((line) => /^\*\*Status:\*\*/.test(line))
  const statusVersion =
    status.length === 1 && /^\*\*Status:\*\* `v([^`]+)`(?:[, .]|$)/.exec(status[0])?.[1]
  requireThat(
    statusVersion === version && status[0].includes(`](docs/release-notes/v${version}.md)`),
    `README status must name v${version} and link docs/release-notes/v${version}.md on the promoted commit`,
  )
  requireThat(input.notes?.trim(), `promoted commit has no docs/release-notes/v${version}.md`)
  const inventory = describeEntries(nightly.artifacts)
  for (const name of [...payloadNames(version), 'manifest.json', 'manifest.json.sig']) {
    requireThat(inventory.has(name), `nightly inventory missing ${name}`)
  }
  verifyBody(manifestBody, inventory.get('manifest.json'))
  verifyBody(Buffer.from(manifestSignature), inventory.get('manifest.json.sig'))
  requireThat(
    manifest.artifacts?.length === 2 && manifest.helpers?.length === 4,
    'nightly manifest must cover both platforms and four helpers',
  )
  const artifactTuples = manifest.artifacts.map((a) => `${a.os}/${a.arch}/${a.format}`).sort()
  requireThat(
    JSON.stringify(artifactTuples) ===
      JSON.stringify(['darwin/universal/zip', 'linux/amd64/AppImage']),
    'nightly manifest platform tuples are invalid',
  )
  const helperTuples = manifest.helpers.map((a) => `${a.os}/${a.arch}`).sort()
  requireThat(
    JSON.stringify(helperTuples) ===
      JSON.stringify(['darwin/amd64', 'darwin/arm64', 'linux/amd64', 'linux/arm64']) &&
      manifest.helpers.every((a) => a.format === undefined),
    'nightly manifest helper tuples are invalid',
  )
  const prefix = `https://github.com/${repo}/releases/download/${tag}/`
  const mapped = [...manifest.artifacts, ...manifest.helpers]
  const seen = new Set()
  for (const entry of mapped) {
    requireThat(
      typeof entry.url === 'string' && entry.url.startsWith(prefix),
      'nightly manifest URL is outside its dated release',
    )
    const name = entry.url.slice(prefix.length)
    requireThat(
      payloadNames(version).includes(name) && !name.endsWith('.dmg') && !seen.has(name),
      `invalid or duplicate manifest URL: ${entry.url}`,
    )
    seen.add(name)
    const pinned = inventory.get(name)
    requireThat(
      pinned?.sha256 === entry.sha256 && pinned.size === entry.size,
      `manifest/inventory digest disagreement: ${name}`,
    )
    const expected =
      entry.format === 'zip'
        ? `nocx-${version}-darwin-universal.zip`
        : entry.format === 'AppImage'
          ? `nocx-${version}-linux-amd64.AppImage`
          : `nocx-helper-${version}-${entry.os}-${entry.arch}`
    requireThat(name === expected, `manifest platform disagrees with URL: ${name}`)
  }
  return {
    repo,
    tag,
    sha,
    version,
    nightly,
    manifest,
    inventory: [...inventory.values()],
    notes: input.notes,
  }
}

export function verifyFiles(directory, entries) {
  for (const entry of entries) {
    const path = join(directory, entry.name)
    requireThat(
      lstatSync(path).isFile() && !lstatSync(path).isSymbolicLink(),
      `not a regular artifact: ${entry.name}`,
    )
    verifyBody(readFileSync(path), entry)
  }
}

export function prepareStable(candidate, source, output, privateKey, keys) {
  verifyFiles(source, candidate.inventory)
  mkdirSync(output) // Existing staging output is never silently clobbered.
  const { repo, tag, sha, version, manifest } = candidate
  const assets = []
  for (const name of payloadNames(version)) {
    const pinned = candidate.inventory.find((a) => a.name === name)
    copyFileSync(join(source, name), join(output, name))
    assets.push(pinned)
  }
  const stable = structuredClone(manifest)
  const from = `https://github.com/${repo}/releases/download/${tag}/`
  const to = `https://github.com/${repo}/releases/download/v${version}/`
  for (const entry of [...stable.artifacts, ...stable.helpers])
    entry.url = to + entry.url.slice(from.length)
  stable.notesUrl = `https://github.com/${repo}/releases/tag/v${version}`
  const body = Buffer.from(JSON.stringify(stable, null, 2) + '\n')
  const signature = sign(null, body, privateKey).toString('base64') + '\n'
  verifySignature(body, signature, keys) // The secret must match the candidate's compiled keyring.
  for (const [name, bytes] of [
    ['manifest.json', body],
    ['manifest.json.sig', Buffer.from(signature)],
  ]) {
    writeFileSync(join(output, name), bytes)
    assets.push({ name, sha256: sha256(bytes), size: bytes.length })
  }
  verifyFiles(output, assets)
  requireThat(readdirSync(output).length === assets.length, 'unexpected staged stable asset')
  const macSHA256 = assets.find((a) => a.name === `nocx-${version}-darwin-universal.zip`).sha256
  return { schema: 1, tag: `v${version}`, nightly: tag, sha, version, assets, macSHA256 }
}

export function updateCask(text, version, hash) {
  requireThat(
    versionPattern.test(version) && hashPattern.test(hash),
    'invalid cask version or sha256',
  )
  const versions = text.match(/^\s*version\s+["'][^"']+["']/gm) ?? []
  const hashes = text.match(/^\s*sha256\s+["'][0-9a-f]{64}["']/gm) ?? []
  requireThat(
    versions.length === 1 && hashes.length === 1,
    'expected exactly one stable cask version and sha256',
  )
  return text
    .replace(/^(\s*version\s+)["'][^"']+["']/m, `$1"${version}"`)
    .replace(/^(\s*sha256\s+)["'][0-9a-f]{64}["']/m, `$1"${hash}"`)
}
