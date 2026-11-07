import { test } from 'node:test'
import { execFileSync, spawnSync } from 'node:child_process'
import assert from 'node:assert/strict'
import {
  mkdtempSync,
  writeFileSync,
  readFileSync,
  rmSync,
  cpSync,
  mkdirSync,
  readdirSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { generateKeyPairSync, sign, createHash } from 'node:crypto'
import {
  decide,
  publish,
  prepare,
  attest,
  authorize,
  nextVersion,
  buildNumberOf,
  datedNightlyTags,
  isDatedNightly,
  nightlyTag,
  nightlyTitle,
  parseWhatChangesForYou,
  releaseNotes,
} from './publish-nightly.mjs'

const buildIdentity = { commit: 'a'.repeat(40), builtAt: '2026-10-27T03:17:00Z' }
// The tag's suffix is the signed build instant's UTC minute (nocx-vh2hr.1.17),
// so a fixture's tag is a function of its builtAt and never a free string.
const buildTag = 'v0.8.0-nightly.202610270317'

function writeSignedJSON(dir, name, value, privateKey) {
  const bytes = Buffer.from(JSON.stringify(value))
  writeFileSync(join(dir, name), bytes)
  writeFileSync(join(dir, `${name}.sig`), sign(null, bytes, privateKey).toString('base64'))
}

function sealProvenance({ dir, input, privateKey }) {
  writeSignedJSON(dir, 'nightly.json', attest(input), privateKey)
}

function changeManifest(candidate, change) {
  const manifest = JSON.parse(readFileSync(join(candidate.dir, 'manifest.json'), 'utf8'))
  change(manifest)
  writeSignedJSON(candidate.dir, 'manifest.json', manifest, candidate.privateKey)
  sealProvenance(candidate)
}

function changeReport(candidate, platform, change) {
  const path = join(candidate.dir, `verified-${platform}.json`)
  const report = JSON.parse(readFileSync(path, 'utf8'))
  change(report)
  writeFileSync(path, JSON.stringify(report))
  sealProvenance(candidate)
}

function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'nightly-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const { publicKey, privateKey } = generateKeyPairSync('ed25519')
  const files = ['nocx-0.8.0-darwin-universal.zip', 'nocx-0.8.0-linux-amd64.AppImage']
  const artifacts = files.map((name, i) => {
    const data = Buffer.from(`built artifact ${i}`)
    writeFileSync(join(dir, name), data)
    return {
      os: i ? 'linux' : 'darwin',
      arch: i ? 'amd64' : 'universal',
      format: i ? 'AppImage' : 'zip',
      url: `https://github.com/shady2k/nocx/releases/download/${buildTag}/${name}`,
      sha256: createHash('sha256').update(data).digest('hex'),
      size: data.length,
    }
  })
  writeSignedJSON(
    dir,
    'manifest.json',
    {
      version: '0.8.0',
      released: '2026-10-27T03:25:00Z',
      notesUrl: `https://github.com/shady2k/nocx/releases/tag/${buildTag}`,
      build: { ...buildIdentity },
      artifacts,
      helpers: [],
    },
    privateKey,
  )
  const input = {
    moved: true,
    sha: buildIdentity.commit,
    version: '0.8.0',
    tag: buildTag,
    previous: 'nightly-20261026-41',
    commits: [
      {
        sha: 'b'.repeat(40),
        subject: 'Merge pull request #41 from shady2k/update-url (nocx-x)',
        pull: {
          number: 41,
          title: 'Keep the update manifest URL immutable',
          body: '## What changes for you\r\n\r\n- The update check no longer loses the manifest URL.\r\n- The update notice names the build you are running.\r\n',
        },
      },
      {
        sha: 'c'.repeat(40),
        subject: 'Merge pull request #42 from shady2k/tech (nocx-y)',
        pull: { number: 42, title: 'Refactor the nightly publisher', body: 'No section here.' },
      },
    ],
    runId: '42',
    runAttempt: '1',
    dir,
    keyring: [publicKey.export({ format: 'der', type: 'spki' }).subarray(-32).toString('base64')],
  }
  for (const [i, platform] of ['darwin', 'linux'].entries()) {
    const about = {
      Version: input.version,
      Commit: input.sha,
      Date: buildIdentity.builtAt,
      Wails: 'v3.0.0-beta.9',
    }
    writeFileSync(
      join(dir, `verified-${platform}.json`),
      JSON.stringify({
        version: input.version,
        commit: input.sha,
        archive: { name: files[i], sha256: artifacts[i].sha256 },
        identity: { CFBundleIdentifier: 'dev.nocx' },
        declaredIdentity: { CFBundleIdentifier: 'dev.nocx' },
        executables: { nocx: { about }, 'nocx-server': { about } },
        updater: {
          signature: 'fixture-signature',
          sha256: 'fixture-hash',
          manifest: { version: '0.7.2' },
        },
      }),
    )
  }
  for (const name of [
    'nocx-0.8.0-darwin-universal.dmg',
    ...['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64'].map(
      (p) => `nocx-helper-0.8.0-${p}`,
    ),
  ])
    writeFileSync(join(dir, name), 'fixture payload')
  const candidate = { dir, input, privateKey }
  sealProvenance(candidate)
  return candidate
}

test('attest refuses a directory by name and still attests a clean artifact directory', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'nightly-attest-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const bytes = Buffer.from('built artifact')
  const name = 'nocx.AppImage'
  writeFileSync(join(dir, name), bytes)
  const input = { dir, sha: buildIdentity.commit, version: '0.8.0', tag: 'nightly-20261027-42' }
  const expected = {
    name,
    sha256: createHash('sha256').update(bytes).digest('hex'),
    size: bytes.length,
  }
  assert.deepEqual(attest(input).artifacts, [expected])
  const evidence = join(dir, 'appimage-verification')
  mkdirSync(evidence)
  assert.throws(() => attest(input), {
    message: `${evidence} is a directory, not an artifact`,
  })
  rmSync(evidence, { recursive: true })
  assert.deepEqual(attest(input).artifacts, [expected])
})

test('unchanged main publishes nothing, without artifacts or signing keys', async () => {
  const calls = []
  assert.deepEqual(await publish({ moved: false }, (...args) => calls.push(args)), {
    published: false,
  })
  assert.deepEqual(calls, [])
})
test('moved main publishes signed dated and moving prereleases, never a cask', async (t) => {
  const { input } = fixture(t)
  const calls = []
  await publish(input, (args) => {
    calls.push(args)
    return '[]'
  })
  assert.ok(calls.some((c) => c.includes(input.tag) && c.includes('--prerelease')))
  assert.ok(calls.some((c) => c.includes('nightly') && c.includes('--prerelease')))
  assert.ok(!JSON.stringify(calls).includes('homebrew'))
})
test('publication keeps build time distinct from the later manifest release time', (t) => {
  const candidate = fixture(t)
  const manifest = JSON.parse(readFileSync(join(candidate.dir, 'manifest.json'), 'utf8'))
  assert.notEqual(manifest.released, manifest.build.builtAt)
  assert.equal(decide(candidate.input).published, true)
})

const invalidBuildIdentities = [
  ['missing build', undefined],
  ['null build', null],
  ['array build', []],
  ['string build', 'not-an-object'],
  ['missing commit', { builtAt: buildIdentity.builtAt }],
  ['missing builtAt', { commit: buildIdentity.commit }],
  ['unknown field', { ...buildIdentity, extra: true }],
  ['non-string commit', { ...buildIdentity, commit: 42 }],
  ['short commit', { ...buildIdentity, commit: 'a'.repeat(39) }],
  ['non-hex commit', { ...buildIdentity, commit: 'g'.repeat(40) }],
  ['uppercase commit', { ...buildIdentity, commit: 'A'.repeat(40) }],
  ['non-string builtAt', { ...buildIdentity, builtAt: 42 }],
  ['empty builtAt', { ...buildIdentity, builtAt: '' }],
  ['date-only builtAt', { ...buildIdentity, builtAt: '2026-10-27' }],
  ['timezone-less builtAt', { ...buildIdentity, builtAt: '2026-10-27T03:17:00' }],
  ['invalid month', { ...buildIdentity, builtAt: '2026-13-27T03:17:00Z' }],
  ['nonexistent day', { ...buildIdentity, builtAt: '2026-02-30T03:17:00Z' }],
  ['invalid hour', { ...buildIdentity, builtAt: '2026-10-27T25:17:00Z' }],
]

for (const [label, build] of invalidBuildIdentities) {
  test(`signed manifest with ${label} refuses before any gh call`, async (t) => {
    const candidate = fixture(t)
    changeManifest(candidate, (manifest) => {
      if (build === undefined) delete manifest.build
      else manifest.build = build
    })
    const calls = []
    await assert.rejects(
      publish(candidate.input, (args) => {
        calls.push(args)
        return '[]'
      }),
      /build/i,
    )
    assert.deepEqual(calls, [])
  })
}

test('signed manifest build.commit must match the declared candidate before any gh call', async (t) => {
  const candidate = fixture(t)
  changeManifest(candidate, (manifest) => {
    manifest.build.commit = 'b'.repeat(40)
  })
  const calls = []
  await assert.rejects(
    publish(candidate.input, (args) => {
      calls.push(args)
      return '[]'
    }),
    /build.commit/,
  )
  assert.deepEqual(calls, [])
})

for (const platform of ['darwin', 'linux']) {
  for (const executable of ['nocx', 'nocx-server']) {
    for (const [field, value] of [
      ['Date', '2026-10-26T03:17:00Z'],
      ['Date', undefined],
      ['Commit', 'b'.repeat(40)],
    ]) {
      test(`${platform}.${executable} ${field} ${value === undefined ? 'missing' : 'mismatch'} refuses before any gh call`, async (t) => {
        const candidate = fixture(t)
        changeReport(candidate, platform, (report) => {
          if (value === undefined) delete report.executables[executable].about[field]
          else report.executables[executable].about[field] = value
        })
        const calls = []
        await assert.rejects(
          publish(candidate.input, (args) => {
            calls.push(args)
            return '[]'
          }),
          (error) => {
            assert.ok(error.message.includes(`${platform}.${executable}.About.${field}`))
            return true
          },
        )
        assert.deepEqual(calls, [])
      })
    }
  }
}

test('a later signed build time cannot replace the original packaged identity', async (t) => {
  const candidate = fixture(t)
  changeManifest(candidate, (manifest) => {
    manifest.build.builtAt = manifest.released
  })
  const calls = []
  await assert.rejects(
    publish(candidate.input, (args) => {
      calls.push(args)
      return '[]'
    }),
    /About.Date/,
  )
  assert.deepEqual(calls, [])
})

test('missing or corrupt signature refuses before any publication', async (t) => {
  const { input, dir } = fixture(t)
  const calls = []
  writeFileSync(join(dir, 'manifest.json.sig'), 'broken')
  await assert.rejects(
    publish(input, (args) => calls.push(args)),
    /signature/,
  )
  assert.deepEqual(calls, [])
})
test('changed artifact refuses naming its filename before publication', (t) => {
  const { input, dir } = fixture(t)
  writeFileSync(join(dir, 'nocx-0.8.0-darwin-universal.zip'), 'corrupt')
  assert.throws(() => decide(input), /nocx-0.8.0-darwin-universal.zip/)
})
test('wrong stamped version refuses', (t) => {
  const { input } = fixture(t)
  assert.throws(() => decide({ ...input, version: '0.9.0' }), /version/)
})
// THE VERSION COMES FROM THE TAGS (ADR-79b6cy61). After v0.8.0 every
// nightly is 0.8.1, with no VERSION bump waiting between a release and the
// next nightly; the tag that promotion creates IS the next input.
test('nextVersion is the next patch after the highest stable tag, nightly tags excluded', () => {
  assert.equal(nextVersion(['v0.7.2', 'v0.8.0', 'nightly-20261027-42', 'nightly']), '0.8.1')
  // Order of the tag list is not a policy: the highest release wins.
  assert.equal(nextVersion(['v0.8.0', 'v0.7.2', 'v0.6.1']), '0.8.1')
  // After promotion the SAME rule gives the version after it.
  assert.equal(nextVersion(['v0.8.1', 'v0.8.0']), '0.8.2')
  // A nightly-only history names no version at all.
  assert.throws(() => nextVersion(['nightly', 'nightly-20261027-42']), /no stable release tag/)
  assert.throws(() => nextVersion([]), /no stable release tag/)
  // Missing or unreadable tags fail closed rather than picking a lower release.
  assert.throws(() => nextVersion(undefined), /tags must be the tag names/)
  assert.throws(() => nextVersion(['v0.8', 'v0.8.0']), /unreadable release tag v0\.8/)
})

// THE TAG AND THE TITLE NAME THE BUILD (nocx-vh2hr.1.17). The suffix is the
// UTC minute of the signed build instant — the same number internal/version
// stamps into the binary, macOS CFBundleVersion, and the update notice — so a
// reader can tell two nightlies of one version apart from the list alone.
test('a dated tag and its title carry the signed build number', () => {
  assert.equal(buildNumberOf('2026-11-02T11:01:00Z'), '202611021101')
  assert.equal(nightlyTag('0.8.1', '202611021101'), 'v0.8.1-nightly.202611021101')
  assert.equal(nightlyTitle('0.8.1', '202611021101'), 'nocx 0.8.1 nightly · build 202611021101')
  // Only a canonical UTC-second instant has a number; a guessed one does not.
  for (const instant of [
    'unknown',
    '',
    '2026-11-02',
    '2026-11-02T11:01:00',
    '2026-11-02T11:01:00.500Z',
  ])
    assert.equal(buildNumberOf(instant), '', instant)
  assert.throws(() => nightlyTag('0.8', '202611021101'), /MAJOR\.MINOR\.PATCH/)
  assert.throws(() => nightlyTag('0.8.1', '2026-11-02'), /build number/)
})

test('the tag must number the signed build time, not another build of the version', (t) => {
  const candidate = fixture(t)
  // The URLs and the tag travel together — the real builder takes both from one
  // variable — so the case that matters is a signed manifest for the 03:17Z
  // build offered under a tag that numbers 03:25Z.
  changeManifest(candidate, (manifest) => {
    const wrong = 'v0.8.0-nightly.202610270325'
    manifest.notesUrl = `https://github.com/shady2k/nocx/releases/tag/${wrong}`
    for (const artifact of manifest.artifacts)
      artifact.url = artifact.url.replace(candidate.input.tag, wrong)
  })
  assert.throws(
    () => decide({ ...candidate.input, tag: 'v0.8.0-nightly.202610270325' }),
    /does not number this build: the signed build time gives v0\.8\.0-nightly\.202610270317/,
  )
})

test('a publish titles the dated copy and the alias by the build, never by a run id', async (t) => {
  const { input } = fixture(t)
  const calls = []
  await publish(input, (args) => {
    calls.push(args)
    if (args[0] === 'api') return JSON.stringify([[]])
    return ''
  })
  const dated = calls.find((c) => c[0] === 'release' && c[1] === 'create' && c[2] === input.tag)
  assert.equal(dated[dated.indexOf('--title') + 1], 'nocx 0.8.0 nightly · build 202610270317')
  assert.ok(dated.includes('--prerelease') && dated.includes('--latest=false'))
  const alias = calls.find((c) => c[0] === 'release' && c[1] === 'create' && c[2] === 'nightly')
  assert.equal(alias[alias.indexOf('--title') + 1], 'Nightly')
  assert.ok(alias.includes('--latest=false'))
})

test('notes list what changed for a user, each bullet linked to its pull request', async (t) => {
  const { input } = fixture(t)
  const calls = []
  await publish(input, (args) => {
    calls.push(args)
    if (args[0] === 'api') return JSON.stringify([[]])
    return ''
  })
  const dated = calls.find((c) => c[0] === 'release' && c[1] === 'create' && c[2] === input.tag)
  const notes = dated[dated.indexOf('--notes') + 1]
  assert.match(notes, /^`nocx 0\.8\.0` nightly, build 202610270317, from main at a{40}\./)
  assert.match(notes, /Changes since nightly-20261026-41:/)
  assert.match(notes, /## What changed/)
  assert.match(
    notes,
    /- The update check no longer loses the manifest URL\. \(\[#41\]\(https:\/\/github\.com\/shady2k\/nocx\/pull\/41\)\)/,
  )
  assert.match(
    notes,
    /- The update notice names the build you are running\. \(\[#41\]\(https:\/\/github\.com\/shady2k\/nocx\/pull\/41\)\)/,
  )
  assert.match(notes, /<summary>Technical changes<\/summary>/)
  assert.match(
    notes,
    /- Refactor the nightly publisher \(\[#42\]\(https:\/\/github\.com\/shady2k\/nocx\/pull\/42\)\)/,
  )
  // A merge subject says which branch landed, not what changed, and never
  // reaches the notes when the pull request is known.
  assert.doesNotMatch(notes, /Merge pull request/)
  // The moving alias serves the same build and says the same thing.
  const alias = calls.find((c) => c[0] === 'release' && c[1] === 'create' && c[2] === 'nightly')
  assert.equal(alias[alias.indexOf('--notes') + 1], notes)
})

test('release notes name the build, the comparison point and what changed in plain words', () => {
  const notes = releaseNotes({
    version: '0.8.1',
    build: '202611021101',
    sha: 'a'.repeat(40),
    previous: 'v0.8.1-nightly.202611011830',
    commits: [
      {
        sha: 'b'.repeat(40),
        subject: 'Merge pull request #7 from shady2k/pty-fix',
        pull: {
          number: 7,
          title: 'Keep the terminal open',
          body: '## What changes for you\n\n- The terminal stays open when a program exits.\n',
        },
      },
      { sha: 'd'.repeat(40), subject: 'fix(pty): keep the slave open', pull: null },
    ],
  })
  assert.equal(
    notes,
    [
      '`nocx 0.8.1` nightly, build 202611021101, from main at ' + 'a'.repeat(40) + '.',
      '',
      'Changes since v0.8.1-nightly.202611011830:',
      '',
      '## What changed',
      '',
      '- The terminal stays open when a program exits. ([#7](https://github.com/shady2k/nocx/pull/7))',
      '',
      '<details>',
      '<summary>Technical changes</summary>',
      '',
      '- fix(pty): keep the slave open ([`ddddddd`](https://github.com/shady2k/nocx/commit/' +
        'd'.repeat(40) +
        '))',
      '',
      '</details>',
    ].join('\n'),
  )
  assert.doesNotMatch(notes, /Merge pull request/)
})

test('the first nightly reads as today: no comparison, no empty sections', () => {
  assert.equal(
    releaseNotes({ version: '0.8.1', build: '202611021101', sha: 'a'.repeat(40), previous: null }),
    '`nocx 0.8.1` nightly, build 202611021101, from main at ' +
      'a'.repeat(40) +
      '.\n\nThis is the first dated nightly on the channel: there is no build to compare with.',
  )
})

test('the what-changes heading is exact, tolerates CRLF and stops at the next heading', () => {
  assert.deepEqual(
    parseWhatChangesForYou(
      'Intro\r\n\r\n## What changes for you\r\n\r\n- One user change.\r\n- Two user changes.\r\n\r\n## Reviewer notes\r\n- Not a user change.\r\n',
    ),
    ['One user change.', 'Two user changes.'],
  )
  // Heading matched exactly: another level, another suffix and a different
  // bullet marker are not the section.
  assert.deepEqual(parseWhatChangesForYou('### What changes for you\n\n- One\n'), [])
  assert.deepEqual(parseWhatChangesForYou('## What changes for you?\n\n- One\n'), [])
  assert.deepEqual(parseWhatChangesForYou('## What changes for you\n\n* One\n- Two\n'), ['Two'])
  assert.deepEqual(parseWhatChangesForYou('## What changes for you\n\n## Details\n- One\n'), [])
})

test('a pull request without the section, a direct commit and an empty section are technical changes', () => {
  const notes = releaseNotes({
    version: '0.8.1',
    build: '202611021101',
    sha: 'a'.repeat(40),
    previous: 'v0.8.1-nightly.202611011830',
    commits: [
      {
        sha: 'b'.repeat(40),
        subject: 'Merge pull request #8 from shady2k/tidy',
        pull: {
          number: 8,
          title: 'Tidy the workflow',
          body: '## What changes for you\r\n\r\n## Notes\r\n- Internal only.\r\n',
        },
      },
      {
        sha: 'c'.repeat(40),
        subject: 'Merge pull request #9 from shady2k/deps',
        pull: { number: 9, title: 'Drop an unused dependency', body: '' },
      },
      { sha: 'd'.repeat(40), subject: 'chore: refresh the lockfile', pull: null },
    ],
  })
  assert.match(notes, /This build has only technical changes\./)
  assert.doesNotMatch(notes, /## What changed/)
  assert.match(notes, /- Tidy the workflow \(\[#8\]/)
  assert.match(notes, /- Drop an unused dependency \(\[#9\]/)
  assert.match(notes, /- chore: refresh the lockfile \(\[`ddddddd`\]/)
  assert.doesNotMatch(notes, /Merge pull request/)
})

test('a pull request reached by several commits is listed once', () => {
  const pull = {
    number: 10,
    title: 'One change',
    body: '## What changes for you\n\n- One user change.\n',
  }
  const notes = releaseNotes({
    version: '0.8.1',
    build: '202611021101',
    sha: 'a'.repeat(40),
    previous: 'v0.8.1-nightly.202611011830',
    commits: [
      { sha: 'b'.repeat(40), subject: 'Merge pull request #10 from a', pull },
      { sha: 'c'.repeat(40), subject: 'Merge pull request #10 from a again', pull },
    ],
  })
  assert.equal((notes.match(/One user change\./g) || []).length, 1)
  assert.equal((notes.match(/pull\/10/g) || []).length, 1)
})

test('notes for the first dated nightly say there is nothing to compare with', async (t) => {
  const { input } = fixture(t)
  const calls = []
  await publish({ ...input, previous: null, commits: [] }, (args) => {
    calls.push(args)
    if (args[0] === 'api') return JSON.stringify([[]])
    return ''
  })
  const dated = calls.find((c) => c[0] === 'release' && c[1] === 'create' && c[2] === input.tag)
  assert.match(dated[dated.indexOf('--notes') + 1], /first dated nightly/)
})

test('dated releases are read newest first in both spellings, drafts and stables excluded', () => {
  const releases = [
    {
      tag_name: 'nightly-20261025-40',
      prerelease: true,
      draft: false,
      published_at: '2026-10-25T03:20:00Z',
    },
    {
      tag_name: 'v0.8.1-nightly.202610270317',
      prerelease: true,
      draft: false,
      published_at: '2026-10-27T03:30:00Z',
    },
    {
      tag_name: 'nightly-20261026-41',
      prerelease: true,
      draft: true,
      published_at: '2026-10-26T03:20:00Z',
    },
    { tag_name: 'nightly', prerelease: true, draft: false, published_at: '2026-10-27T03:30:00Z' },
    { tag_name: 'v0.8.0', prerelease: false, draft: false, published_at: '2026-10-21T03:30:00Z' },
    {
      tag_name: 'not-a-nightly',
      prerelease: true,
      draft: false,
      published_at: '2026-10-24T03:30:00Z',
    },
  ]
  assert.deepEqual(
    datedNightlyTags(releases).map((r) => r.tag_name),
    ['v0.8.1-nightly.202610270317', 'nightly-20261025-40'],
  )
  assert.ok(isDatedNightly('v0.8.1-nightly.202610270317'))
  assert.ok(isDatedNightly('nightly-20261026-41'))
  assert.ok(!isDatedNightly('nightly'))
  assert.ok(!isDatedNightly('v0.8.0'))
})

test('the version still comes from stable tags when new nightly tags are reachable', () => {
  // A nightly tag sorts above the stable tags of its minor in a plain version
  // sort; counting it would name a version no stable release will carry.
  assert.equal(nextVersion(['v0.8.0', 'v0.8.1-nightly.202610270317']), '0.8.1')
  assert.equal(nextVersion(['v0.8.0', 'v0.9.0-nightly.202610270317', 'nightly']), '0.8.1')
  assert.equal(nextVersion(['v0.8.1', 'v0.8.0', 'v0.9.0-nightly.202610270317']), '0.8.2')
  assert.throws(() => nextVersion(['v0.9.0-nightly.202610270317']), /no stable release tag/)
  // A near-miss is still unreadable: the dated rule is exact.
  assert.throws(() => nextVersion(['v0.9.0-nightly.2026']), /unreadable release tag/)
})

test('prepare distinguishes moved, unchanged, and supporting-only commits', () => {
  const base = {
    sha: 'a'.repeat(40),
    previous: 'b'.repeat(40),
    tags: ['v0.8.0', 'nightly-20261027-42', 'v0.8.1-nightly.202610270317'],
  }
  assert.equal(prepare({ ...base, files: ['main.go'] }).moved, true)
  assert.equal(prepare({ ...base, previous: base.sha, files: [] }).moved, false)
  assert.equal(prepare({ ...base, files: ['docs/a.md', '.beads/issues.jsonl'] }).moved, false)
  assert.equal(prepare({ ...base, previous: null, files: [] }).moved, true)
  assert.throws(() => prepare({ ...base, files: [] }), /empty diff/)
})
test('publication failure does not move nightly tag', async (t) => {
  const { input } = fixture(t)
  const calls = []
  await assert.rejects(
    publish(input, (args) => {
      calls.push(args)
      throw Error('upload failed')
    }),
    /upload failed/,
  )
  assert.equal(calls.length, 1)
})

// Static wiring checks supplement, never replace, the executed decisions above.
test('prepare derives the version from the tags instead of reading VERSION', () => {
  const plan = prepare({
    sha: 'a'.repeat(40),
    previous: null,
    files: [],
    tags: ['v0.8.0'],
  })
  assert.equal(plan.version, '0.8.1')
  // A version handed in beside the tags is not a second source: the tags are.
  const after = prepare({
    sha: 'a'.repeat(40),
    previous: null,
    files: [],
    tags: ['v0.8.1', 'v0.8.0'],
    version: '0.0.0',
  })
  assert.equal(after.version, '0.8.2')
})

test('the nightly workflow reads the reachable tags and never the VERSION file', () => {
  const workflow = readFileSync(
    new URL('../.github/workflows/nightly.yml', import.meta.url),
    'utf8',
  )
  assert.match(workflow, /git\(\['tag', '--list', '--merged', sha\]\)/)
  assert.match(workflow, /prepare\(\{[^}]*tags/s)
  assert.match(workflow, /fetch-tags: true/)
  assert.doesNotMatch(workflow, /readFileSync\('VERSION'/)
  // Nightly and promotion must not run at the same time: both are in one
  // concurrency group that queues rather than cancels.
  assert.match(workflow, /concurrency:\s*\n\s*group: nocx-release/)
  assert.match(workflow, /cancel-in-progress: false/)
  const release = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  assert.match(release, /concurrency:\s*\n\s*group: nocx-release/)
  assert.match(release, /cancel-in-progress: false/)
  assert.doesNotMatch(release, /promotion-github\.mjs bump/)
})

// THE WORKFLOW DOES NOT INVENT A SECOND ANSWER (nocx-vh2hr.1.17). The tag comes
// from the build that stamps the instant, the proofs read the module's own
// predicate, and nothing selects nightlies by a tag prefix written by hand.
test('the tag is made where the build instant is stamped, and the proofs read it from there', () => {
  const nightly = readFileSync(new URL('../.github/workflows/nightly.yml', import.meta.url), 'utf8')
  const build = readFileSync(
    new URL('../.github/workflows/release-build.yml', import.meta.url),
    'utf8',
  )
  // One instant, and the tag derived from it beside the number it numbers.
  assert.match(build, /build_number="\$\(date -u -d "\$build_date" \+%Y%m%d%H%M\)"/)
  assert.match(build, /tag="v\$\{VERSION\}-nightly\.\$\{build_number\}"/)
  assert.match(
    build,
    /^ {4}outputs:\n {6}tag:\n {8}description:[^\n]*\n {8}value: \$\{\{ jobs\.validate\.outputs\.tag \}\}$/m,
  )
  assert.doesNotMatch(build, /inputs\.release-tag/)
  // The nightly plan decides moved/version and never a tag.
  assert.doesNotMatch(nightly, /needs\.prepare\.outputs\.tag/)
  assert.match(nightly, /RELEASE_TAG: \$\{\{ needs\.build\.outputs\.tag \}\}/)
  assert.match(nightly, /PUBLISHED_TAG: \$\{\{ needs\.build\.outputs\.tag \}\}/)
  // Both proofs select the pair with the publication module, in both spellings.
  assert.equal((nightly.match(/node scripts\/publish-nightly\.mjs dated-tags/g) || []).length, 2)
  assert.doesNotMatch(nightly, /startswith\("nightly-"\)/)
  // The notes are the pull requests of the first-parent commits since the
  // previous nightly. A failed lookup is not caught, so the plan step fails
  // rather than publishing notes that silently lost a change.
  assert.match(nightly, /git\(\['log', '--first-parent', '--format=%H%x09%s'/s)
  assert.match(nightly, /commits=\$\{JSON\.stringify\(commits\)\}/)
  // A null previous is an empty output, never the string "null" a step would
  // read back as a tag.
  assert.match(nightly, /\$\{key\}=\$\{value \?\? ''\}/)
})

test('the plan resolves every commit to its pull request and fails when a lookup fails', () => {
  const nightly = readFileSync(new URL('../.github/workflows/nightly.yml', import.meta.url), 'utf8')
  // The plan step asks GitHub for the pull request of every first-parent
  // commit, and carries its title and body to publication.
  assert.match(
    nightly,
    /gh\(\['api', `repos\/\{owner\}\/\{repo\}\/commits\/\$\{commit\}\/pulls`\]\)/,
  )
  assert.match(
    nightly,
    /pull: pull \? \{ number: pull\.number, title: pull\.title, body: pull\.body \?\? '' \} : null/,
  )
  // execFileSync throws on a failed lookup and nothing catches it, so the
  // plan step fails instead of publishing notes with a change missing.
  const start = nightly.indexOf('- id: plan')
  const end = nightly.indexOf('\n          JS\n', start)
  assert.ok(start >= 0 && end > start, 'the plan step script must be present')
  assert.doesNotMatch(nightly.slice(start, end), /catch/)
  // The job's token must be allowed to read pull requests, or every lookup
  // fails.
  const prepare = nightly.slice(nightly.indexOf('  prepare:'), nightly.indexOf('  build:'))
  assert.match(prepare, /pull-requests: read/)
})

test('the pull request template asks for the user-facing section', () => {
  const template = readFileSync(
    new URL('../.github/pull_request_template.md', import.meta.url),
    'utf8',
  )
  assert.match(template, /^## What changes for you$/m)
})

test('nightly workflow calls the shared build without CI or Homebrew and gates unchanged main', () => {
  const workflow = readFileSync(
    new URL('../.github/workflows/nightly.yml', import.meta.url),
    'utf8',
  )
  assert.match(workflow, /cron:/)
  assert.match(workflow, /if: needs.prepare.outputs.moved == 'true'/)
  assert.match(workflow, /uses: \.\/\.github\/workflows\/release-build.yml/)
  assert.doesNotMatch(workflow, /uses:.*ci.yml|HOMEBREW_TAP_TOKEN|Casks\//i)
  const build = readFileSync(
    new URL('../.github/workflows/release-build.yml', import.meta.url),
    'utf8',
  )
  assert.doesNotMatch(build, /uses:.*ci.yml/)
  assert.match(build, /scripts\/appimage\/verify-appimage.sh/)
  assert.match(build, /go run \.\/cmd\/manifest-sign -verify/)
})

test('deliberately broken About.Wails refuses naming field before any gh call', async (t) => {
  const { input, dir } = fixture(t)
  const calls = []
  const file = join(dir, 'verified-darwin.json')
  const report = JSON.parse(readFileSync(file, 'utf8'))
  report.executables.nocx.about.Wails = 'unknown'
  writeFileSync(file, JSON.stringify(report))
  await assert.rejects(
    publish(input, (args) => calls.push(args)),
    /About.Wails/,
  )
  assert.deepEqual(calls, [])
})
// THE NEWEST THREE DATED NIGHTLIES SURVIVE (nocx-vh2hr.1.17): this build, the
// previous one the update proof updates FROM, and one spare. The old
// nightly-<date>-<run> tags are in this list because they are what the first
// run under the new scheme has to retire.
test('retention keeps the three newest dated copies in both tag spellings', async (t) => {
  const { input } = fixture(t)
  const calls = []
  const releases = [
    { tag_name: 'nightly-20261026-41', prerelease: true, published_at: '2026-10-26T03:20:00Z' },
    { tag_name: 'nightly-20261025-40', prerelease: true, published_at: '2026-10-25T03:20:00Z' },
    { tag_name: 'nightly-20261024-39', prerelease: true, published_at: '2026-10-24T03:20:00Z' },
    { tag_name: 'nightly-20261023-38', prerelease: true, published_at: '2026-10-23T03:20:00Z' },
    {
      tag_name: 'v0.8.0-nightly.202610221830',
      prerelease: true,
      published_at: '2026-10-22T18:30:00Z',
    },
    { tag_name: 'nightly', prerelease: true, published_at: '2026-10-27T03:30:00Z' },
    { tag_name: 'v0.7.2', prerelease: false, published_at: '2026-10-20T03:30:00Z' },
    { tag_name: 'v0.8.0', prerelease: false, published_at: '2026-10-21T03:30:00Z' },
  ]
  await publish(input, (args) => {
    calls.push(args)
    if (args[0] === 'api') return JSON.stringify([releases])
    // The moving alias is replaced by the same publish; its backup is real.
    if (args[1] === 'download')
      cpSync(input.dir, args[args.indexOf('--dir') + 1], { recursive: true })
    return ''
  })
  const deleted = calls.filter((c) => c[1] === 'delete' && c[2] !== 'nightly').map((c) => c[2])
  // Kept: this build, 20261026-41 (the proof's previous) and 20261025-40.
  assert.deepEqual(deleted.sort(), [
    'nightly-20261023-38',
    'nightly-20261024-39',
    'v0.8.0-nightly.202610221830',
  ])
  // The proof's previous build is the second newest and is never deleted.
  assert.ok(!deleted.includes('nightly-20261026-41'))
})

test('a nightly that is the only dated copy deletes nothing', async (t) => {
  const { input } = fixture(t)
  const calls = []
  const releases = [
    { tag_name: 'v0.8.0', prerelease: false, published_at: '2026-10-21T03:30:00Z' },
    { tag_name: 'nightly', prerelease: true, published_at: '2026-10-27T03:30:00Z' },
  ]
  await publish(input, (args) => {
    calls.push(args)
    if (args[0] === 'api') return JSON.stringify([releases])
    if (args[1] === 'download')
      cpSync(input.dir, args[args.indexOf('--dir') + 1], { recursive: true })
    return ''
  })
  assert.deepEqual(
    calls.filter((c) => c[1] === 'delete' && c[2] !== 'nightly'),
    [],
  )
})

test('undeclared nightly.json-prefixed file is refused', (t) => {
  const { input, dir } = fixture(t)
  writeFileSync(join(dir, 'nightly.json.exe'), 'undeclared')
  assert.throws(() => decide(input), /nightly.artifacts/)
})
test('failed alias replacement restores the previous signed nightly', async (t) => {
  const { input, dir } = fixture(t)
  const calls = []
  const prior = { tag_name: 'nightly', name: 'Old Nightly', body: 'previous', draft: false }
  let creates = 0
  await assert.rejects(
    publish(input, (args) => {
      calls.push(args)
      if (args[0] === 'api') return JSON.stringify([[prior]])
      if (args[1] === 'download') {
        cpSync(dir, args[args.indexOf('--dir') + 1], { recursive: true })
        return ''
      }
      if (args[1] === 'create' && args[2] === 'nightly' && ++creates === 1)
        throw Error('GitHub upload failed')
      return ''
    }),
    /previous signed nightly restored/,
  )
  assert.equal(creates, 2)
  assert.equal(calls.at(-1)[2], 'nightly')
  assert.ok(calls.at(-1).includes('Old Nightly'))
})

test('landing page stamps stable tags only, not the newer nightly aliases', () => {
  const result = execFileSync('bash', ['scripts/latest-stable-tag.sh'], {
    input:
      'nightly\nnightly-20261027-42\nv0.8.1-nightly.202611021101\nv0.7.2\nv0.8.0-rc.1\nv0.6.1\nv0.10.0\n',
    encoding: 'utf8',
  })
  assert.equal(result.trim(), 'v0.10.0')
})
test('lost landing-page release and CTA markers refuse publication', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'nocx-site-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  cpSync('scripts', join(dir, 'scripts'), { recursive: true })
  cpSync('site', join(dir, 'site'), { recursive: true })
  const run = () => spawnSync('bash', [join(dir, 'scripts/check-site.sh')], { encoding: 'utf8' })
  assert.equal(run().status, 0)
  const page = join(dir, 'site/index.html')
  writeFileSync(
    page,
    readFileSync(page, 'utf8')
      .replaceAll('data-released', 'lost-released')
      .replaceAll('<span data-version>', '<span lost-version>'),
  )
  const result = run()
  assert.equal(result.status, 1)
  assert.match(result.stdout, /no <span data-released>/)
  assert.match(result.stdout, /no <span data-version>/)
})

test('partial alias delete failure still restores the old tag and signed release', async (t) => {
  const { input, dir } = fixture(t)
  const calls = []
  let removed = false
  const prior = { tag_name: 'nightly', name: 'Previous', draft: false }
  await assert.rejects(
    publish(input, (args) => {
      calls.push(args)
      if (args[0] === 'api' && args[1] === '--paginate')
        return JSON.stringify([[...(removed ? [] : [prior])]])
      if (args[0] === 'api') return '[]'
      if (args[1] === 'download') {
        cpSync(dir, args.at(-1), { recursive: true })
        return ''
      }
      if (args[1] === 'delete') {
        removed = true
        throw Error('response lost after deletion')
      }
      return ''
    }),
    /previous signed nightly restored/,
  )
  assert.ok(
    calls.some(
      (args) => args.includes('ref=refs/tags/nightly') && args.includes(`sha=${input.sha}`),
    ),
  )
  assert.ok(calls.at(-1).includes('Previous'))
})

test('new workflows admit branch dry runs and Linux-first iteration, never branch publication', () => {
  for (const name of ['nightly', 'release-build']) {
    const yaml = readFileSync(new URL(`../.github/workflows/${name}.yml`, import.meta.url), 'utf8')
    assert.match(yaml, /workflow_dispatch:/)
    assert.match(yaml, /dry-run:/)
    assert.match(yaml, /linux-only:/)
  }
  const yaml = readFileSync(new URL('../.github/workflows/nightly.yml', import.meta.url), 'utf8')
  assert.match(yaml, /authorize\(/)
})

test('branch publication is refused while branch Linux dry run is allowed', () => {
  assert.throws(
    () => authorize({ ref: 'refs/heads/feature', dryRun: false, linuxOnly: false }),
    /requires main/,
  )
  assert.throws(
    () => authorize({ ref: 'refs/heads/main', dryRun: false, linuxOnly: true }),
    /requires both platforms/,
  )
  assert.deepEqual(authorize({ ref: 'refs/heads/feature', dryRun: true, linuxOnly: true }), {
    publish: false,
  })
  assert.deepEqual(authorize({ ref: 'refs/heads/main', dryRun: false, linuxOnly: false }), {
    publish: true,
  })
})

for (const location of ['runner temp', 'sibling temp', 'caller override']) {
  test(`AppImage verification evidence uses ${location}, outside dist`, (t) => {
    const root = mkdtempSync(join(tmpdir(), 'appimage-evidence-'))
    t.after(() => rmSync(root, { recursive: true, force: true }))
    const dist = join(root, 'dist')
    const bin = join(root, 'bin')
    const runner = join(root, 'runner')
    for (const path of [dist, bin, runner]) mkdirSync(path)
    const appimage = join(dist, 'nocx.AppImage')
    writeFileSync(appimage, 'fixture artifact')
    // Stop at the container boundary. The real wrapper still owns path
    // selection, directory creation, mount arguments and its job-log output.
    writeFileSync(join(bin, 'docker'), '#!/bin/sh\nprintf "%s\n" "$@"\n', { mode: 0o755 })
    const env = { ...process.env, PATH: `${bin}:${process.env.PATH}` }
    delete env.RUNNER_TEMP
    delete env.NOCX_APPIMAGE_VERIFY_EVIDENCE
    if (location === 'runner temp') env.RUNNER_TEMP = runner
    if (location === 'caller override') {
      env.RUNNER_TEMP = runner
      env.NOCX_APPIMAGE_VERIFY_EVIDENCE = join(root, 'chosen')
    }
    const result = spawnSync('bash', ['scripts/appimage/verify-appimage.sh', appimage, 'none'], {
      encoding: 'utf8',
      env,
    })
    assert.equal(result.status, 0, result.stdout + result.stderr)
    assert.deepEqual(readdirSync(dist), ['nocx.AppImage'])
    const evidence = result.stdout.match(/^AppImage verification evidence: (.+)$/m)?.[1]
    assert.ok(evidence, 'the job log must name the evidence directory')
    assert.ok(result.stdout.includes(`${evidence}:/evidence`))
    if (location === 'runner temp') assert.equal(dirname(evidence), runner)
    if (location === 'sibling temp') assert.equal(dirname(evidence), root)
    if (location === 'caller override') assert.equal(evidence, env.NOCX_APPIMAGE_VERIFY_EVIDENCE)
  })
}
