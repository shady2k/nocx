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
const STABLE_TAG = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/
/**
 * A dated nightly tag is `v<version>-nightly.<build>` (nocx-vh2hr.1.17) or the
 * older `nightly-<date>-<run>`. Both are dated copies, neither is a release, and
 * a rule that reads tags has to recognise both: the old ones are still published
 * and are what the first run under the new scheme prunes.
 */
const DATED_NIGHTLY =
  /^(?:nightly-\d{8}-\d+|v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)-nightly\.\d{12})$/
/** The newest three dated nightlies: this build, the one the update proof
 * updates FROM, and one spare (nocx-vh2hr.1.17, the owner's decision). */
const DATED_NIGHTLIES_KEPT = 3

/** True for a dated nightly tag in either spelling. Never true for `nightly`. */
export function isDatedNightly(name) {
  return typeof name === 'string' && DATED_NIGHTLY.test(name)
}

/**
 * The number one build instant carries: its UTC date and minute, `YYYYMMDDHHMM`
 * (nocx-vh2hr.1.5). internal/version.BuildNumberOf owns this rule for the
 * binary, the About panel and the update notice, and the tag must carry the same
 * number those show, so this is that derivation and not a second one. An
 * instant that is not canonical UTC-second RFC 3339 answers "" — the number is
 * absent rather than invented, exactly as the Go function answers.
 */
export function buildNumberOf(builtAt) {
  if (typeof builtAt !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(builtAt))
    return ''
  const instant = new Date(builtAt)
  if (
    !Number.isFinite(instant.getTime()) ||
    instant.toISOString() !== `${builtAt.slice(0, -1)}.000Z`
  )
    return ''
  const pad = (value) => String(value).padStart(2, '0')
  return (
    String(instant.getUTCFullYear()) +
    pad(instant.getUTCMonth() + 1) +
    pad(instant.getUTCDate()) +
    pad(instant.getUTCHours()) +
    pad(instant.getUTCMinutes())
  )
}

/** The immutable dated tag for one build. */
export function nightlyTag(version, build) {
  if (!/^\d+\.\d+\.\d+$/.test(String(version)))
    fail(`nightly version ${version} is not MAJOR.MINOR.PATCH`)
  if (!/^\d{12}$/.test(String(build)))
    fail(`nightly build number ${build} is not the UTC build minute YYYYMMDDHHMM`)
  return `v${version}-nightly.${build}`
}

/** The release title a person reads in the releases list. */
export function nightlyTitle(version, build) {
  return `nocx ${version} nightly · build ${build}`
}

/**
 * Release notes for one dated build: the first-parent commits since the
 * previous nightly, newest first, each linked to its commit. `commits` is
 * collected by the workflow (`git log --first-parent --format=%H%x09%s`), so
 * this function owns the wording and the links and nothing about git.
 */
export function releaseNotes({ version, build, sha, previous, commits = [] }) {
  const lines = [`\`nocx ${version}\` nightly, build ${build}, from main at ${sha}.`, '']
  if (!previous) {
    lines.push('This is the first dated nightly on the channel: there is no build to compare with.')
    return lines.join('\n')
  }
  lines.push(`Changes since ${previous}:`, '')
  for (const commit of commits)
    lines.push(
      `- ${commit.subject} ([\`${commit.sha.slice(0, 7)}\`](https://github.com/shady2k/nocx/commit/${commit.sha}))`,
    )
  return lines.join('\n')
}

/**
 * The dated nightlies in `releases`, newest first by publication. Drafts and
 * everything that is not a dated nightly — stable releases, the moving
 * `nightly` alias — are excluded here, which is why no caller prunes by hand.
 */
export function datedNightlyTags(releases) {
  if (!Array.isArray(releases)) fail('releases must be the release list gh api printed')
  return releases
    .filter(
      (release) =>
        release && isDatedNightly(release.tag_name) && release.prerelease && !release.draft,
    )
    .sort((a, b) => String(b.published_at).localeCompare(String(a.published_at)))
}

/**
 * The version a nightly embeds: the next patch after the highest STABLE tag
 * on the revision's ancestry (ADR-79b6cy61, partially superseding
 * ADR-0097's VERSION source).
 *
 * `tags` is every tag this revision can reach, as `git tag --list --merged`
 * prints them, and this function is the ONE place that picks among them.
 * `git describe` is not used: its "nearest tag" is a different policy, and it
 * walks toward a commit rather than answering "which release is the newest".
 *
 * The nightly's own tags are excluded by name — both spellings, moving and
 * dated — and every other tag must be a stable `vX.Y.Z` — a release tag the
 * version is read from cannot be one this function guesses about, so an
 * unreadable one fails the nightly closed rather than silently picking the next
 * lower release. A tag like `v0.9.0-nightly.202611021101` is therefore skipped
 * by the dated rule and never mistaken for a release: it sorts above the stable
 * tags of the same minor in a plain version sort, and counting it would name a
 * version that no stable release will ever carry.
 */
export function nextVersion(tags) {
  if (!Array.isArray(tags)) fail('tags must be the tag names reachable from the nightly revision')
  let best = null
  for (const name of tags) {
    if (typeof name !== 'string' || name === '') continue
    if (isDatedNightly(name) || name === 'nightly') continue
    const match = STABLE_TAG.exec(name)
    if (!match)
      fail(
        `unreadable release tag ${name}: the nightly version comes from stable tags, so an unknown tag cannot be skipped`,
      )
    const parts = match.slice(1, 4).map(Number)
    if (
      !best ||
      parts[0] > best[0] ||
      (parts[0] === best[0] && (parts[1] > best[1] || (parts[1] === best[1] && parts[2] > best[2])))
    )
      best = parts
  }
  if (!best)
    fail(
      'no stable release tag is reachable from this revision; a nightly cannot name the next version',
    )
  return `${best[0]}.${best[1]}.${best[2] + 1}`
}

export function prepare({ sha, previous, files, tags }) {
  if (!/^[a-f0-9]{40}$/.test(sha)) fail('sha must be a full main revision')
  const version = nextVersion(tags)
  if (previous && previous !== sha && files.length === 0)
    fail('empty diff between different main revisions')
  // No tag here: the tag numbers the build's own signed instant
  // (buildNumberOf), and that instant is chosen by the build job, so naming it
  // before the build would be a second, guessed answer to the same question.
  return {
    moved: previous !== sha && (!previous || touchesProduct(files)),
    sha,
    version,
    previous: previous ?? null,
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
  if (!/^[a-f0-9]{40}$/.test(sha) || !isDatedNightly(tag)) fail('invalid nightly identity')
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
  // THE TAG NUMBERS THE BUILD (nocx-vh2hr.1.17): its suffix is the number About
  // and the update notice show, which is the UTC minute of the SIGNED build
  // instant. The title names that number too, so a tag, a title and a build
  // cannot disagree about which build a person is looking at.
  const build = buildNumberOf(manifest.build.builtAt)
  const expected = nightlyTag(version, build)
  if (tag !== expected)
    fail(`nightly tag ${tag} does not number this build: the signed build time gives ${expected}`)
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
    tag,
    version,
    build,
    title: nightlyTitle(version, build),
    notes: releaseNotes({
      version,
      build,
      sha,
      previous: input.previous ?? null,
      commits: input.commits ?? [],
    }),
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
  const notes = plan.notes
  // Dated copy first. If it fails, the existing moving nightly stays untouched.
  await run([
    'release',
    'create',
    plan.tag,
    '--target',
    input.sha,
    '--prerelease',
    '--latest=false',
    '--title',
    plan.title,
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
  // PRUNE TO THE NEWEST THREE DATED NIGHTLIES (nocx-vh2hr.1.17): this build,
  // the one the update proof updates FROM, and one spare. The list was fetched
  // before this build existed, so the new tag leads it explicitly; the filter
  // admits both tag spellings and nothing else, which is what keeps the stable
  // releases and the moving 'Nightly' alias out of it by construction. The
  // proof's previous build is therefore always the second entry and is never a
  // candidate for deletion.
  const ordered = [...new Set([plan.tag, ...datedNightlyTags(releases).map((r) => r.tag_name)])]
  for (const old of ordered.slice(DATED_NIGHTLIES_KEPT)) {
    await run(['release', 'delete', old, '--yes', '--cleanup-tag'])
  }
  return { published: true, tag: plan.tag, title: plan.title }
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
  } else if (mode === 'dated-tags') {
    // The workflow's one reader of "which published nightlies exist": newest
    // first, `<tag>\t<published_at>` per line, both tag spellings, drafts out.
    for (const release of datedNightlyTags(JSON.parse(readFileSync(path, 'utf8')).flat()))
      console.log(`${release.tag_name}\t${release.published_at}`)
  } else fail('usage: publish-nightly.mjs prepare|attest|publish|dated-tags input.json')
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  main().catch((error) => {
    console.error(`publish-nightly: ${error.message}`)
    process.exitCode = 1
  })
