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

test('error values are not logger calls, including nested and multiline calls', () => {
  const source = `func main() {
  report(cause.Error())
  report(other.Error(
    /* no message argument */
  ))
  report(third.Error( // still no message argument
  ))
}`
  assert.deepEqual(violationsInText('errors.go', source), [])
})

test('HTTP response errors use their actual unshadowed import alias', () => {
  const source = `package main
import nethttp "net/http"
func main() {
  nethttp.Error(w, cause.Error(), 500)
}`
  assert.deepEqual(violationsInText('http.go', source), [])
  const shadowed = `package main
import nethttp "net/http"
func main() {
  nethttp := newLogger()
  nethttp.Error("lost context")
}`
  assert.deepEqual(
    violationsInText('shadowed.go', shadowed).map((v) => v.text),
    ['nethttp.Error("lost context")'],
  )
})

test('real error logging remains reported beside error values and without HTTP imports', () => {
  const source = `func main() {
  logger.Error(cause.Error())
  http.Error("lost context")
}`
  assert.deepEqual(
    violationsInText('logging.go', source).map((v) => v.text),
    ['logger.Error(cause.Error())', 'http.Error("lost context")'],
  )
})
