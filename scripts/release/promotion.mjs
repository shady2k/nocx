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

import { judgeFailures, stepManifestDigest } from '../quarantine.mjs'

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

// Expand the candidate workflow once for both job names and gate manifests.
// This deliberately supports only CI's literal matrix axes, not arbitrary YAML.
function ciJobs(workflow) {
  requireThat(typeof workflow === 'string', 'CI workflow source is missing')
  const jobSection = /^jobs:\n([\s\S]*)/m.exec(workflow)?.[1]
  requireThat(jobSection, 'CI workflow declares no jobs')
  const jobs = [...jobSection.matchAll(/^ {2}([\w-]+):\n([\s\S]*?)(?=^ {2}[\w-]+:|$(?![\s\S]))/gm)]
  requireThat(jobs.length > 0, 'CI workflow declares no jobs')
  return jobs.flatMap(([, name, body]) => {
    if (!/^ {6}matrix:/m.test(body)) return [{ name, body, matrix: {} }]
    const block = /^ {6}matrix:\n((?: {8}[^\n]*\n|\n)*)/m.exec(body)?.[1]
    const rows = block
      ?.split('\n')
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith('#'))
    requireThat(rows?.length, `CI matrix ${name} is empty`)
    let combinations = [{}]
    for (const row of rows) {
      const match = /^(\w+): \[([\w, -]+)\]$/.exec(row)
      requireThat(match, `unsupported CI matrix syntax in ${name}: ${row}`)
      const values = match[2].split(',').map((s) => s.trim())
      requireThat(values.every(Boolean), `empty CI matrix value in ${name}`)
      requireThat(!Object.hasOwn(combinations[0], match[1]), `duplicate CI matrix axis in ${name}`)
      combinations = combinations.flatMap((combo) =>
        values.map((value) => ({ ...combo, [match[1]]: value })),
      )
    }
    return combinations.map((matrix) => ({
      name: `${name} (${Object.values(matrix).join(', ')})`,
      body,
      matrix,
    }))
  })
}

export function ciJobNames(workflow, { gated = false } = {}) {
  return ciJobs(workflow)
    .filter(({ body }) => !gated || body.includes('quarantine-gate.mjs'))
    .map(({ name }) => name)
}

// Read the gate step's own env declaration, independently of its log record.
// CI uses literals, matrix substitutions and one equality/and/or choice. Any
// other expression or declaration shape needs an explicit parser change.
export function ciStepManifests(workflow) {
  return Object.fromEntries(
    ciJobs(workflow).flatMap(({ name, body, matrix }) => {
      if (!body.includes('quarantine-gate.mjs')) return []
      const steps = body.split(/^ {6}- /m).slice(1)
      const gates = steps.filter((step) =>
        /^ {8}run: node scripts\/quarantine-gate\.mjs\s*$/m.test(step),
      )
      requireThat(gates.length === 1, `CI: unsupported quarantine gate declaration in ${name}`)
      const declarations = [...gates[0].matchAll(/^ {10}NOCX_EXPECTED_STEPS: ([^\n]+)$/gm)]
      requireThat(
        declarations.length === 1,
        `CI: missing or duplicate expected-step manifest in ${name}`,
      )
      let value = declarations[0][1].trim()
      if (value.startsWith("'") && value.endsWith("'")) value = value.slice(1, -1)
      else if (value.startsWith('"') && value.endsWith('"')) value = value.slice(1, -1)
      value = value.replace(/\$\{\{([\s\S]*?)\}\}/g, (_, expression) => {
        const variable = /^\s*matrix\.(\w+)\s*$/.exec(expression)
        const choice =
          /^\s*matrix\.(\w+)\s*==\s*'([\w-]+)'\s*&&\s*'([\w -]+)'\s*\|\|\s*'([\w -]+)'\s*$/.exec(
            expression,
          )
        const axis = variable?.[1] ?? choice?.[1]
        requireThat(
          axis && Object.hasOwn(matrix, axis),
          `CI: unsupported expected-step expression in ${name}`,
        )
        return variable ? matrix[axis] : matrix[axis] === choice[2] ? choice[3] : choice[4]
      })
      requireThat(
        /^[\w-]+(?: [\w-]+)*$/.test(value),
        `CI: unsupported expected-step manifest in ${name}`,
      )
      const expected = value.split(' ')
      requireThat(
        new Set(expected).size === expected.length,
        `CI: duplicate expected-step manifest in ${name}`,
      )
      return [[name, expected]]
    }),
  )
}

const quarantineMarker = 'NOCX_QUARANTINE_EVIDENCE_V1 '

// Read the final gate's record even when GitHub calls the job successful.
// Timestamp stripping matches the shared legacy log parser. An echoed shell
// command is not a record: the marker must start the log message itself.
export function readQuarantineEvidence(log, name, pull, binding) {
  const records = log
    .split(/\r?\n/)
    .map((line) => line.replace(/^\S+Z /, ''))
    .filter((line) => line.startsWith(quarantineMarker))
  requireThat(records.length === 1, `CI: ${name} needs exactly one quarantine evidence record`)
  const record = JSON.parse(records[0].slice(quarantineMarker.length))
  validateQuarantineEvidence(record, name, pull, binding)
  return record
}

function validateQuarantineEvidence(record, name, pull, binding) {
  requireThat(
    record?.schema === 1 &&
      record.job === name &&
      typeof record.runId === 'string' &&
      /^[1-9]\d*$/.test(record.runId) &&
      Number.isSafeInteger(record.attempt) &&
      record.attempt > 0 &&
      typeof record.manifestDigest === 'string' &&
      hashPattern.test(record.manifestDigest) &&
      (record.pull === pull || record.pull === String(pull)) &&
      Array.isArray(record.steps) &&
      record.steps.length > 0,
    `CI: ${name} has invalid quarantine evidence identity or steps`,
  )
  const names = new Set()
  for (const step of record.steps) {
    requireThat(
      typeof step?.name === 'string' &&
        step.name.trim() &&
        !names.has(step.name) &&
        Number.isInteger(step.status) &&
        step.status >= 0 &&
        step.status <= 255 &&
        typeof step.complete === 'boolean' &&
        Array.isArray(step.failures) &&
        step.failures.every((key) => typeof key === 'string' && key.trim()) &&
        (step.status !== 0 || (step.complete && step.failures.length === 0)),
      `CI: ${name} has invalid quarantine evidence step`,
    )
    names.add(step.name)
  }
  const { runId, attempt, expectedSteps } = binding ?? {}
  requireThat(
    record.runId === String(runId) &&
      record.attempt === attempt &&
      Array.isArray(expectedSteps) &&
      record.manifestDigest === stepManifestDigest(expectedSteps) &&
      JSON.stringify([...names].sort()) === JSON.stringify([...expectedSteps].sort()),
    `CI: ${name} quarantine evidence does not match run, attempt or workflow manifest`,
  )
}

// Direct product commits need clean suites, not merely API-green quarantines.
export function cleanCICover(entry) {
  return (
    entry.run?.conclusion === 'success' &&
    !entry.attempts &&
    !entry.jobs.some((job) => job.quarantineEvidence?.steps.some((step) => step.status !== 0))
  )
}

function verifyGatedJob(entry, label, job, manifests, attempt) {
  validateQuarantineEvidence(job.quarantineEvidence, job.name, entry.number, {
    runId: entry.run.id,
    attempt,
    expectedSteps: manifests[job.name],
  })
  for (const step of job.quarantineEvidence.steps) {
    if (step.status === 0) continue
    const verdict = judgeFailures({
      failures: step.failures,
      complete: step.complete,
      quarantine: entry.quarantine ?? {},
      jobKey: `job:${job.name}#${entry.number}`,
    })
    requireThat(verdict.accepted, `${label}: ${job.name} step ${step.name} ${verdict.reason}`)
  }
}

// A required job that is not green in the newest attempt still passes when
// every test it failed, in any attempt, is quarantined — or when its only
// failures named no test and the same job passed in another attempt of the
// same run (a retry that classified an infrastructure flake). Anything else,
// a cancelled job included, stays red.
function quarantinedJob(entry, label, name) {
  requireThat(
    Array.isArray(entry.attempts) && entry.quarantine && typeof entry.quarantine === 'object',
    `${label}: ${name} was not green`,
  )
  const history = entry.attempts.flatMap((attempt) =>
    attempt.jobs.filter((job) => job.name === name),
  )
  const passed = history.some((job) => job.status === 'completed' && job.conclusion === 'success')
  const failed = history.filter(
    (job) => !(job.status === 'completed' && job.conclusion === 'success'),
  )
  for (const job of failed) {
    requireThat(job.conclusion === 'failure', `${label}: ${name} was not green`)
    // The verdict is the quarantine's own (scripts/quarantine.mjs), shared with
    // a pull request's own CI so the two cannot answer differently.
    const verdict = judgeFailures({
      failures: job.failures ?? [],
      complete: job.complete === true,
      quarantine: entry.quarantine,
      jobKey: `job:${name}#${entry.number}`,
      passedInAnotherAttempt: passed,
    })
    requireThat(verdict.accepted, `${label}: ${name} ${verdict.reason}`)
  }
  return failed.flatMap((job) => job.failures ?? [])
}

// GitHub names a pull_request run's PR in run.pull_requests only while the
// head branch exists; deleting the branch after the merge empties it (PR #529's
// run 40283573725, 2026-10-29). The run is then the PR's by the branch and
// repository it ran on, on that PR's exact head commit.
export function runBelongsTo(run, pr, repo) {
  if (run.pull_requests?.some((p) => p.number === pr.number)) return true
  return (
    Array.isArray(run.pull_requests) &&
    run.pull_requests.length === 0 &&
    typeof pr.head?.ref === 'string' &&
    run.head_branch === pr.head.ref &&
    run.head_repository?.full_name === repo &&
    pr.head.repo?.full_name === repo
  )
}

export function verifyCI(evidence) {
  requireThat(
    Array.isArray(evidence) && evidence.length > 0,
    'CI evidence is empty: no merged PR heads were verified',
  )
  for (const entry of evidence) {
    if (entry.direct) {
      // Judged by the collector against the PRs after it; here only its record
      // is checked: a product commit must name the green PR that covers it.
      requireThat(shaPattern.test(entry.commit), 'CI: invalid direct commit')
      requireThat(
        !entry.product ||
          evidence.some(
            (other) =>
              !other.direct &&
              other.number === entry.coveredBy &&
              other.product &&
              cleanCICover(other),
          ),
        `CI: direct commit ${entry.commit} touches product and no later green PR covers it`,
      )
      continue
    }
    const { number, head, run, jobs, requiredJobs } = entry
    const label = `CI for PR #${number} head ${head}`
    requireThat(Number.isInteger(number) && shaPattern.test(head), `${label}: invalid PR identity`)
    requireThat(typeof entry.product === 'boolean', `${label}: missing change classification`)
    requireThat(
      run?.event === 'pull_request' &&
        run.head_sha === head &&
        run.path === '.github/workflows/ci.yml' &&
        run.status === 'completed' &&
        runBelongsTo(
          run,
          { number, head: { ref: entry.headRef, repo: { full_name: entry.headRepo } } },
          entry.headRepo,
        ),
      `${label}: no completed CI workflow run on its PR-head SHA`,
    )
    requireThat(
      Array.isArray(requiredJobs) && requiredJobs.length >= 2,
      `${label}: missing workflow job contract`,
    )
    requireThat(Array.isArray(jobs), `${label}: no jobs`)
    requireThat(
      entry.gatedJobs === undefined ||
        (Array.isArray(entry.gatedJobs) &&
          entry.gatedJobs.length > 0 &&
          new Set(entry.gatedJobs).size === entry.gatedJobs.length &&
          entry.gatedJobs.every((name) => requiredJobs.includes(name))),
      `${label}: invalid gated job contract`,
    )
    const hasGateEvidence = [...jobs, ...(entry.attempts ?? []).flatMap((a) => a.jobs)].some(
      (job) => job.quarantineEvidence !== undefined,
    )
    const manifests =
      entry.workflow !== undefined || entry.gatedJobs || hasGateEvidence
        ? ciStepManifests(entry.workflow)
        : {}
    const gatedJobs = Object.keys(manifests)
    if (entry.workflow !== undefined) {
      requireThat(
        JSON.stringify([...requiredJobs].sort()) ===
          JSON.stringify(ciJobNames(entry.workflow).sort()),
        `${label}: required job contract does not match workflow`,
      )
    }
    if (entry.gatedJobs !== undefined) {
      requireThat(
        JSON.stringify([...entry.gatedJobs].sort()) === JSON.stringify([...gatedJobs].sort()),
        `${label}: gated job contract does not match workflow`,
      )
    }
    if (gatedJobs.length || hasGateEvidence) {
      requireThat(
        Number.isSafeInteger(run.id) &&
          run.id > 0 &&
          Number.isSafeInteger(run.run_attempt) &&
          run.run_attempt > 0,
        `${label}: invalid quarantine evidence run identity`,
      )
      if (run.run_attempt > 1 || entry.attempts !== undefined) {
        requireThat(
          Array.isArray(entry.attempts) &&
            entry.attempts.length === run.run_attempt &&
            entry.attempts.every((item, index) => item.attempt === index + 1),
          `${label}: incomplete quarantine evidence history`,
        )
      }
    }
    // Earlier attempts can also be API-green because their gate accepted a
    // failure. Those records still owe today's quarantine verdict.
    for (const attempt of entry.attempts ?? []) {
      requireThat(
        Number.isSafeInteger(attempt.attempt) &&
          attempt.attempt > 0 &&
          attempt.attempt <= run.run_attempt,
        `${label}: invalid quarantine evidence history attempt`,
      )
      for (const job of attempt.jobs) {
        if (
          (gatedJobs.includes(job.name) || job.quarantineEvidence !== undefined) &&
          job.conclusion === 'success'
        ) {
          verifyGatedJob(entry, label, job, manifests, attempt.attempt)
        }
      }
    }
    const remaining = new Set(jobs)
    let quarantined = 0
    for (const name of requiredJobs) {
      let matches = jobs.filter((job) => job.name === name)
      // jobs.if is evaluated before matrix expansion. GitHub reports one
      // unexpanded skipped job when an entire supporting-only matrix is off.
      if (!entry.product && matches.length === 0 && name.includes(' (')) {
        matches = jobs.filter(
          (job) => job.name === name.split(' (')[0] && job.conclusion === 'skipped',
        )
      }
      requireThat(matches.length === 1, `${label}: missing or duplicate job ${name}`)
      const job = matches[0]
      const supportingSkip =
        !entry.product && !['changes', 'ci-backlog'].includes(name) && job.conclusion === 'skipped'
      if (
        (gatedJobs.includes(name) || job.quarantineEvidence !== undefined) &&
        job.conclusion === 'success'
      ) {
        verifyGatedJob(entry, label, job, manifests, run.run_attempt)
      }
      if (!(job.status === 'completed' && (job.conclusion === 'success' || supportingSkip))) {
        quarantinedJob(entry, label, job.name)
        quarantined++
      }
      remaining.delete(job)
    }
    requireThat(remaining.size === 0, `${label}: extra jobs`)
    requireThat(
      run.conclusion === 'success' || quarantined > 0,
      `${label}: no successful CI workflow run on its PR-head SHA`,
    )
  }
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
  requireThat(
    /^nightly-\d{8}-\d+$/.test(tag),
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
  verifyCI(input.ci)
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
