// node --test scripts/workflow-wiring.test.mjs
//
// A workflow names files that must exist in the tree it runs from, and GitHub
// resolves none of them: a step that imports a moved module fails at run time,
// on its schedule, long after the rename was reviewed. Measured 2026-11-01
// (nocx-fg79x.6): the tracker mechanism's recipe moved from
// .githooks/backlog-gate/br-tracker.mjs to .githooks/backlog-gate/trackers/br.mjs
// and every reference moved with it except the one in
// .github/workflows/tracker-gate.yml. The scheduled gate then stopped at its
// first step with ERR_MODULE_NOT_FOUND, and the two steps after it -- the gate
// itself and the push that records the checked head -- had never run in CI
// (nocx-pknm1).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const WORKFLOWS = join(ROOT, '.github/workflows')

// A path-shaped module reference: it carries a directory component, so a bare
// `check.mjs` named beside a `$gate/` variable is not one, and neither is a
// `$gate/commit-links.mjs` -- that is a shell expansion and the variable is
// resolved at run time. The leading lookbehind drops it and any word-adjacent
// suffix of it.
const MODULE = /(?<![$\w])[\w.-]+(?:\/[\w.-]+)+\.mjs\b/g

const referenced = (text) => [...text.matchAll(MODULE)].map((match) => match[0])

test('every module a workflow names exists in the tree', () => {
  const missing = []
  for (const name of readdirSync(WORKFLOWS).filter((file) => file.endsWith('.yml'))) {
    const text = readFileSync(join(WORKFLOWS, name), 'utf8')
    for (const reference of referenced(text)) {
      const path = reference.replace(/^\.\//, '')
      if (!existsSync(join(ROOT, path))) missing.push(`${name}: ${reference}`)
    }
  }
  assert.deepEqual(missing, [], `workflows name modules that do not exist:\n${missing.join('\n')}`)
})

// THE REF NAME HAS ONE OWNER, and it is the recipe the mechanism reads
// (tracker-sync.mjs refuses a recipe whose ref is a branch or a tag). The
// workflow derives the ref by importing that recipe instead of carrying a
// second copy of the name, so this test asks the module the workflow names.
test('the tracker gate takes the store ref from the recipe, not a copy of the name', async () => {
  const workflow = readFileSync(join(WORKFLOWS, 'tracker-gate.yml'), 'utf8')
  const imported = workflow.match(/import\(\s*['"]([^'"]+)['"]\s*\)/)
  assert.ok(imported, 'the workflow imports no module; the ref has no owner left')
  const recipe = await import(pathToFileURL(join(ROOT, imported[1].replace(/^\.\//, ''))).href)
  assert.match(recipe.default?.ref ?? '', /^refs\//)
  assert.doesNotMatch(recipe.default.ref, /^refs\/(heads|tags)\//)
  // A literal here would be a second owner, and the two would agree until one
  // of them moved.
  assert.doesNotMatch(workflow, /refs\/tracker\/state/)
})
