import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

// Load the actual helper without starting Playwright; these readers use no browser.
const source = readFileSync(new URL('./ask-gesture.ts', import.meta.url), 'utf8')
const compiled = ts
  .transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ESNext },
  })
  .outputText.replace(/import .*? from .*?;\n/g, '')
const { terminalContext, attachedItemID, systemPrompt, toolResults } = await import(
  `data:text/javascript,${encodeURIComponent(compiled)}`
)

test('attachments come from the current user turn, not stale system or earlier turns', () => {
  const body = JSON.stringify({
    messages: [
      { role: 'system', content: '\n- id: stale; state: exited' },
      { role: 'user', content: '\n- id: earlier; state: exited' },
      { role: 'user', content: [{ type: 'text', text: '\n- id: current; state: running' }] },
    ],
  })
  assert.equal(attachedItemID(body), 'current')
  assert.equal(terminalContext(body), '\n- id: current; state: running')
  assert.equal(systemPrompt(body), '\n- id: stale; state: exited')
})

test('a turn without an attachment cannot reuse an earlier attachment', () => {
  const body = JSON.stringify({
    messages: [
      { role: 'user', content: '\n- id: earlier; state: exited' },
      { role: 'user', content: 'new question' },
    ],
  })
  assert.equal(terminalContext(body), 'new question')
  assert.throws(() => attachedItemID(body), /no attached terminal item id/)
})

test('tool results read text arrays and strings from only the current user turn', () => {
  const body = JSON.stringify({
    messages: [
      { role: 'user', content: 'earlier question' },
      { role: 'tool', content: 'stale result' },
      { role: 'user', content: 'current question' },
      { role: 'assistant', content: 'not a tool result' },
      {
        role: 'tool',
        content: [
          { type: 'text', text: 'screen row one' },
          { type: 'image', data: 'not text' },
          { type: 'text', text: 'screen row two' },
        ],
      },
      { role: 'tool', content: 'another result' },
    ],
  })
  assert.deepEqual(toolResults(body), ['screen row one\nscreen row two', 'another result'])
})
