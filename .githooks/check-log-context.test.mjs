import assert from 'node:assert/strict'
import { test } from 'node:test'
import { violationsInText } from './check-log-context.mjs'

test('version.Info is metadata, including nested production diagnostic calls', () => {
  const source = `package main
import (
  "encoding/json"
  "github.com/shady2k/nocx/internal/version"
)
func main() {
  json.NewEncoder(os.Stdout).Encode(version.Info())
}`
  assert.deepEqual(violationsInText('main.go', source), [])
})

test('recognizes the version package import alias rather than a receiver spelling', () => {
  const source = `package main
import buildversion "github.com/shady2k/nocx/internal/version"
func main() { info := buildversion.Info() }`
  assert.deepEqual(violationsInText('main.go', source), [])
  assert.equal(
    violationsInText('main.go', 'func main() { version.Info("lost context") }').length,
    1,
  )
})

test('does not exempt real logger calls beside version metadata', () => {
  const source = `package main
import "github.com/shady2k/nocx/internal/version"
func main() {
  info := version.Info()
  logger.Info("lost context")
  version.Warn("not a metadata function")
}`
  const violations = violationsInText('main.go', source)
  assert.equal(violations.length, 2)
  assert.match(violations[0].text, /logger.Info/)
  assert.match(violations[1].text, /version.Warn/)
})

test('a local logger shadowing the import still needs context', () => {
  const source = `package main
import "github.com/shady2k/nocx/internal/version"
func main() {
  version := newLogger()
  version.Info("lost context")
}`
  assert.equal(violationsInText('main.go', source).length, 1)
})
