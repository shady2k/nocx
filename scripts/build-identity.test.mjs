import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const workflow = readFileSync(
  new URL('../.github/workflows/release-build.yml', import.meta.url),
  'utf8',
)

function job(name) {
  const match = workflow.match(
    new RegExp(`^  ${name}:\\n([\\s\\S]*?)(?=^  [a-z][a-z-]*:|$(?![\\s\\S]))`, 'm'),
  )
  assert.ok(match, `${name} job must exist`)
  return match[1]
}

function buildDateSource(name) {
  const match = job(name).match(
    /^ {6}BUILD_DATE: \$\{\{ needs\.validate\.outputs\.([a-zA-Z0-9_-]+) \}\}$/m,
  )
  assert.ok(match, `${name} must consume BUILD_DATE from validate, not mint its own time`)
  return match[1]
}

// Reports execute the packaged binaries; these checks guard the workflow wiring
// that makes their one shared build identity possible before signing.
test('both platform builds and signing consume one timestamp produced by validate', () => {
  const source = buildDateSource('build-macos')
  assert.equal(buildDateSource('build-linux'), source)
  assert.equal(buildDateSource('sign'), source)
  const validate = job('validate')
  const escaped = source.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  assert.match(
    validate,
    new RegExp(`^      ${escaped}: \\$\\{\\{ steps\\.v\\.outputs\\.${escaped} \\}\\}$`, 'm'),
  )
  assert.match(validate, new RegExp(`echo "${escaped}=.*" >> "\\$GITHUB_OUTPUT"`))
  assert.equal((validate.match(/date -u \+%Y-%m-%dT%H:%M:%SZ/g) ?? []).length, 1)
  for (const name of ['build-macos', 'build-linux']) {
    const build = job(name)
    assert.doesNotMatch(build, /BUILD_DATE=|date -u \+%Y-%m-%dT%H:%M:%SZ/)
    assert.match(build, /internal\/version\.Commit=\$\{BUILD_SHA\}/)
    assert.match(build, /internal\/version\.Date=\$\{BUILD_DATE\}/)
  }
})

test('signed manifest uses the shared binary commit and time, not publication time', () => {
  const sign = job('sign')
  const commit = sign.match(/--arg ([a-zA-Z_][a-zA-Z0-9_]*) "\$BUILD_SHA"/)
  const builtAt = sign.match(/--arg ([a-zA-Z_][a-zA-Z0-9_]*) "\$BUILD_DATE"/)
  assert.ok(commit, 'manifest generation must pass BUILD_SHA to jq')
  assert.ok(builtAt, 'manifest generation must pass shared BUILD_DATE to jq')
  assert.match(
    sign,
    new RegExp(`build: \\{\\s*commit: \\$${commit[1]},\\s*builtAt: \\$${builtAt[1]}\\s*\\}`),
  )
  assert.match(sign, /go run \.\/cmd\/manifest-sign -in dist\/manifest\.json/)
})
