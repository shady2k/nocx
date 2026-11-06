import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { validate } from './operations-contract.mjs'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '../..')
const schema = JSON.parse(await readFile(resolve(root, 'contracts/operations.schema.json'), 'utf8'))
const manifest = JSON.parse(await readFile(resolve(root, 'contracts/operations.json'), 'utf8'))

function clone() {
  return JSON.parse(JSON.stringify(manifest))
}

test('the generated catalogue satisfies its schema', () => {
  validate(schema, manifest)
})

test('a zero max is refused by the schema, not only by the Go constructor', () => {
  const planted = clone()
  planted.operations[0].max.millis = 0
  assert.throws(() => validate(schema, planted), /below 1|expected/)
})

test('an unlimited bound kind is refused', () => {
  const planted = clone()
  planted.operations[0].silence = { kind: 'unlimited', millis: 0, basis: '', note: '' }
  assert.throws(() => validate(schema, planted), /not one of/)
})

test('a deadline bound with zero millis is refused', () => {
  const planted = clone()
  planted.operations[0].answer = { kind: 'deadline', millis: 0, basis: 'read', note: 'planted' }
  assert.throws(() => validate(schema, planted), /below 1/)
})

test('a missing required field is refused', () => {
  const planted = clone()
  delete planted.operations[0].expired
  assert.throws(() => validate(schema, planted), /missing required expired/)
})

test('an extra field is refused in the operation, its phase and its resources', () => {
  for (const mutate of [
    (op) => {
      op.extra = true
    },
    (op) => {
      op.phases[0].extra = true
    },
    (op) => {
      op.resources.push({ kind: 'session', name: '', mode: 'key', extra: true })
    },
  ]) {
    const planted = clone()
    mutate(planted.operations[0])
    assert.throws(() => validate(schema, planted), /additional property/)
  }
})

test('an empty waiting sentence is refused', () => {
  const planted = clone()
  planted.operations[0].waiting = ''
  assert.throws(() => validate(schema, planted), /shorter than 1/)
})
