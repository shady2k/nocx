import { For, Show, createEffect, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import type { JSX } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { json } from '@codemirror/lang-json'
import { syntaxTree } from '@codemirror/language'
import { classHighlighter, highlightTree } from '@lezer/highlight'
import { Button } from './button'
import { CodeBlock } from './code-block'
import { Dialog } from './dialog'
import { SearchField } from './search-field'
import { Stack } from './stack'

const HIGHLIGHTING_CEILING = 256 * 1024

function exceedsHighlightingCeiling(text: string): boolean {
  return new TextEncoder().encode(text).byteLength > HIGHLIGHTING_CEILING
}

interface DumpDrive {
  text: string
  truncated: boolean
}

interface DumpData {
  request: DumpDrive[]
  response: DumpDrive[]
}

export interface DumpPanelProps {
  dump: DumpData
  copy: (text: string) => Promise<void>
  onClose: () => void
}

type HighlightMode = 'json' | 'sse' | 'plain'

function appendHighlightedJSON(parent: DocumentFragment, text: string): void {
  const state = EditorState.create({ doc: text, extensions: json() })
  const tree = syntaxTree(state)
  let cursor = 0
  highlightTree(tree, classHighlighter, (from, to, classes) => {
    if (from > cursor) parent.append(text.slice(cursor, from))
    const token = document.createElement('span')
    token.className = classes
    token.textContent = text.slice(from, to)
    parent.append(token)
    cursor = to
  })
  if (cursor < text.length) parent.append(text.slice(cursor))
}

function appendHighlightedSSE(parent: DocumentFragment, text: string): void {
  const lines = text.split('\n')
  lines.forEach((line, index) => {
    if (line.startsWith('data:')) {
      const prefix = document.createElement('span')
      prefix.className = 'tok-meta'
      const prefixText = line.slice(0, line.indexOf(':') + 1)
      prefix.textContent = prefixText
      parent.append(prefix)
      const payload = line.slice(prefixText.length).replace(/^\s?/, '')
      if (payload === '[DONE]') {
        const done = document.createElement('span')
        done.className = 'tok-atom'
        done.textContent = '[DONE]'
        parent.append(done)
      } else {
        try {
          appendHighlightedJSON(parent, payload)
        } catch {
          parent.append(payload)
        }
      }
    } else {
      parent.append(line)
    }
    if (index < lines.length - 1) parent.append('\n')
  })
}

function highlightedFragment(text: string, mode: HighlightMode): DocumentFragment {
  const fragment = document.createDocumentFragment()
  if (mode === 'plain' || exceedsHighlightingCeiling(text)) {
    fragment.append(text)
    return fragment
  }
  if (mode === 'sse') {
    appendHighlightedSSE(fragment, text)
  } else {
    try {
      appendHighlightedJSON(fragment, text)
    } catch {
      fragment.append(text)
    }
  }
  return fragment
}

function HighlightedText(props: { text: string; mode: HighlightMode }): JSX.Element {
  let host: HTMLSpanElement | undefined
  createEffect(() => {
    if (host) host.replaceChildren(highlightedFragment(props.text, props.mode))
  })
  return <span class="ui-dump-panel__highlighted" ref={(el) => (host = el)} />
}

function formatRequest(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

interface ToolCall {
  id: string
  name: string
  args: string
}

function assembleResponse(text: string): string {
  const content: string[] = []
  const tools = new Map<number, ToolCall>()
  for (const line of text.split('\n')) {
    if (!line.startsWith('data:')) continue
    const payload = line.slice(line.indexOf(':') + 1).trim()
    if (!payload || payload === '[DONE]') continue
    let frame: {
      choices?: Array<{
        delta?: {
          content?: unknown
          tool_calls?: Array<{
            index?: number
            id?: string
            function?: { name?: string; arguments?: string }
          }>
        }
      }>
    }
    try {
      frame = JSON.parse(payload) as typeof frame
    } catch {
      continue
    }
    for (const choice of frame.choices ?? []) {
      if (typeof choice.delta?.content === 'string') content.push(choice.delta.content)
      for (const [position, call] of (choice.delta?.tool_calls ?? []).entries()) {
        const index = call.index ?? position
        const current = tools.get(index) ?? { id: '', name: '', args: '' }
        if (typeof call.id === 'string') current.id += call.id
        if (typeof call.function?.name === 'string') current.name += call.function.name
        if (typeof call.function?.arguments === 'string') current.args += call.function.arguments
        tools.set(index, current)
      }
    }
  }
  const answer = content.join('')
  const toolText = [...tools.values()]
    .map((tool) => `Tool call: ${tool.name || tool.id || 'unknown'}\n${tool.args}`)
    .join('\n\n')
  const assembled = [answer, toolText].filter(Boolean).join(answer && toolText ? '\n\n' : '')
  return assembled || 'No assembled answer.'
}

function isMatch(needle: string, ...values: string[]): boolean {
  if (!needle) return true
  return values.some((value) => value.toLocaleLowerCase().includes(needle))
}

function DriveList(props: {
  label: string
  drives: DumpDrive[]
  copy: (text: string) => Promise<void>
  response: boolean
  searchQuery: () => string
}): JSX.Element {
  const matching = () =>
    props.drives.filter((drive) => {
      const shown = props.response ? assembleResponse(drive.text) : formatRequest(drive.text)
      return isMatch(props.searchQuery().trim().toLocaleLowerCase(), shown, drive.text)
    })
  return (
    <section class="ui-dump-panel__drive">
      <h3>{props.label}</h3>
      <Show when={props.drives.length > 0} fallback={<p>No recorded drive.</p>}>
        <Show when={matching().length > 0} fallback={<p>No dump content matches this search.</p>}>
          <Stack>
            <For each={matching()}>
              {(drive, index) => {
                const shown = props.response
                  ? assembleResponse(drive.text)
                  : formatRequest(drive.text)
                const large =
                  exceedsHighlightingCeiling(shown) || exceedsHighlightingCeiling(drive.text)
                return (
                  <div class="ui-dump-panel__entry">
                    <h4>Drive {index() + 1}</h4>
                    <div class={props.response ? 'ui-dump-panel__assembled' : undefined}>
                      <CodeBlock
                        ariaLabel={`${props.label} drive ${index() + 1}`}
                        variant="dump"
                        wrap={false}
                        copyText={shown}
                        copy={props.copy}
                      >
                        <HighlightedText text={shown} mode={props.response ? 'plain' : 'json'} />
                      </CodeBlock>
                    </div>
                    <Show when={large}>
                      <p>Highlighting is disabled for large dumps (over 256 KiB).</p>
                    </Show>
                    <Show when={props.response}>
                      <details class="ui-dump-panel__raw">
                        <summary>Raw chunks</summary>
                        <CodeBlock
                          ariaLabel={`${props.label} drive ${index() + 1} raw chunks`}
                          variant="dump"
                          wrap={false}
                        >
                          <HighlightedText text={drive.text} mode="sse" />
                        </CodeBlock>
                      </details>
                    </Show>
                    <Show when={drive.truncated}>
                      <p>Truncated at the 1 MiB capture limit.</p>
                    </Show>
                  </div>
                )
              }}
            </For>
          </Stack>
        </Show>
      </Show>
    </section>
  )
}

export function DumpPanel(props: DumpPanelProps) {
  const [fullPage, setFullPage] = createSignal(false)
  const [searchQuery, setSearchQuery] = createSignal('')
  return (
    <Dialog open title="Model dump" size="full" onClose={props.onClose}>
      <div class="ui-dump-panel" data-reader={fullPage() ? 'full-page' : undefined}>
        <Stack>
          <div class="ui-dump-panel__toolbar">
            <p class="ui-dump-panel__intro">
              Recorded provider bytes. Requests are formatted JSON; responses show the assembled
              answer above raw chunks.
            </p>
            <Button variant="ghost" ariaLabel="Open full page" onClick={() => setFullPage(true)}>
              Open full page
            </Button>
          </div>
          <Show when={fullPage()}>
            <SearchField
              value={searchQuery()}
              onInput={setSearchQuery}
              ariaLabel="Search dump"
              placeholder="Search dump"
            />
          </Show>
          <DriveList
            label="Request"
            drives={props.dump.request}
            copy={props.copy}
            response={false}
            searchQuery={searchQuery}
          />
          <DriveList
            label="Response"
            drives={props.dump.response}
            copy={props.copy}
            response
            searchQuery={searchQuery}
          />
        </Stack>
      </div>
    </Dialog>
  )
}

export function mountDumpPanel(
  host: HTMLElement,
  props: Omit<DumpPanelProps, 'onClose'>,
): () => void {
  const lifecycle: { dispose: (() => void) | undefined } = { dispose: undefined }
  const close = () => {
    lifecycle.dispose?.()
    host.remove()
  }
  lifecycle.dispose = render(
    () => <DumpPanel dump={props.dump} copy={props.copy} onClose={close} />,
    host,
  )
  return () => {
    lifecycle.dispose?.()
    host.remove()
  }
}
