import { test } from 'node:test'
import { execFileSync, spawnSync } from 'node:child_process'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, readFileSync, rmSync, cpSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { generateKeyPairSync, sign, createHash } from 'node:crypto'
import { decide, publish, prepare, attest, authorize } from './publish-nightly.mjs'

const buildIdentity = { commit: 'a'.repeat(40), builtAt: '2026-10-27T03:17:00Z' }

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
      url: `https://github.com/shady2k/nocx/releases/download/nightly-20261027-42/${name}`,
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
      notesUrl: 'https://github.com/shady2k/nocx/releases/tag/nightly-20261027-42',
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
    tag: 'nightly-20261027-42',
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
test('prepare distinguishes moved, unchanged, and supporting-only commits', () => {
  const base = {
    sha: 'a'.repeat(40),
    previous: 'b'.repeat(40),
    version: '0.8.0',
    date: '20261027',
    runId: '42',
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
test('retention keeps seven dated copies and never removes stable releases', async (t) => {
  const { input } = fixture(t)
  const calls = []
  const releases = Array.from({ length: 9 }, (_, i) => ({
    tag_name: `nightly-202610${String(26 - i).padStart(2, '0')}-${i}`,
    prerelease: true,
    published_at: `2026-10-${String(26 - i).padStart(2, '0')}`,
  }))
  releases.push({ tag_name: 'v0.7.2', prerelease: false, published_at: '2026-10-26' })
  await publish(input, (args) => {
    calls.push(args)
    return args[0] === 'api' ? JSON.stringify([releases]) : ''
  })
  assert.equal(calls.filter((c) => c[1] === 'delete').length, 3)
  assert.ok(!calls.some((c) => c[1] === 'delete' && c.includes('v0.7.2')))
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
    input: 'nightly\nnightly-20261027-42\nv0.7.2\nv0.8.0-rc.1\nv0.6.1\nv0.10.0\n',
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
