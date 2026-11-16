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

test('error values do not hide a logger on the same line or inside a result literal', () => {
  const source = `package x
func f(cause error, lg Logger) {
  lg.Error("failed", "cause", cause.Error())
  result := []string{cause.Error()}
  result = append(result, cause.Error(
    /* no arguments */
  ))
}`
  assert.deepEqual(
    violationsInText('x.go', source).map((v) => v.text),
    ['lg.Error("failed", "cause", cause.Error())'],
  )
})

test('net/http.Error is a response function under its real import alias', () => {
  for (const alias of ['http', 'web']) {
    const source = `package x
import ${alias} "net/http"
func f(w ${alias}.ResponseWriter) {
  ${alias}.Error(w, "bad request", ${alias}.StatusBadRequest)
}`
    assert.deepEqual(violationsInText('x.go', source), [])
  }
})

test('a logger named http or shadowing the HTTP import still needs context', () => {
  const source = `package x
import web "net/http"
func f(w web.ResponseWriter, http Logger) {
  web.Error(w, "bad request", web.StatusBadRequest)
  http.Error("actual log")
}`
  assert.deepEqual(
    violationsInText('x.go', source).map((v) => v.text),
    ['http.Error("actual log")'],
  )
  assert.equal(
    violationsInText(
      'x.go',
      `import "net/http"
func f() { http := newLogger(); http.Error("actual log") }`,
    ).length,
    1,
  )
})
