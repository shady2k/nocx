// The run list — the workbench's own, deliberately NOT the terminal's blocks
// (design §9.3). Two consequences, and the second is the gain: it makes no
// restore promise, so ADR-0019 is untouched; and the body is not constrained
// to SGR rows, so a JSON payload, a header table and the raw text can each be
// what they are instead of three kinds of coloured text.
//
// What the body says is four separate sentences and they stay four (§12.3):
// a binary body is named and never base64 — the wire sends EMPTY text for one,
// so "no text" and "binary" must not both render as an empty pane; a truncated
// body says it is a prefix; a body that was not valid text says that; and an
// empty body says it is empty. Collapsing any two of them loses one.
//
// The pretty/raw choice belongs to ONE run. A single flag for the list would
// mean opening the raw text of the run you are reading also opens it for the
// nineteen above it.

import { For, Show, type JSX } from 'solid-js'
import { Badge } from '../ui/badge'
import { CodeBlock } from '../ui/code-block'
import { Caption } from '../ui/caption'
import { EmptyState } from '../ui/empty-state'
import { SegmentedControl } from '../ui/segmented-control'
import { StatusDot } from '../ui/status-dot'
import { createSecretChip, createSecretChipDamaged } from '../ui/secret-chip'
import type { ApiRun, ApiRunView } from './api-store'
import {
  type ApiExchange,
  type ApiRawSegment,
  bodySummary,
  connectionRawText,
  formatElapsed,
  formatSize,
  hasBodyText,
  rawSegments,
  responseHeaderText,
  statusTone,
} from './api-model'

const VIEWS = [
  { value: 'pretty', label: 'Pretty' },
  { value: 'raw', label: 'Raw' },
]

export interface RunListProps {
  runs: readonly ApiRun[]
  onView: (id: number, view: ApiRunView) => void
}

export function RunList(props: RunListProps) {
  return (
    <div class="api-runs">
      <Show
        when={props.runs.length > 0}
        fallback={
          <EmptyState
            title="No runs yet"
            description="Press Send and the exchange appears here, newest first."
          />
        }
      >
        <For each={props.runs}>{(run) => <Run run={run} onView={props.onView} />}</For>
      </Show>
    </div>
  )
}

function Run(props: { run: ApiRun; onView: (id: number, view: ApiRunView) => void }) {
  const run = () => props.run
  const response = () => props.run.response

  return (
    <div class="api-run" data-run-id={run().id}>
      <div class="api-run__head">
        <span class="api-run__verb">
          <Badge tone="neutral">{run().method}</Badge>
        </span>
        <span class="api-run__url" title={run().url}>
          {run().url}
        </span>
        <Show
          when={response()}
          fallback={
            <span class="api-run__status">
              <StatusDot tone="error" accessibleName="the exchange failed">
                <span>failed</span>
              </StatusDot>
            </span>
          }
        >
          {(res) => (
            <>
              <span class="api-run__status">
                <StatusDot
                  tone={statusTone(res().status)}
                  accessibleName={`HTTP status ${res().status}`}
                >
                  <span>{String(res().status)}</span>
                </StatusDot>
              </span>
              <span class="api-run__elapsed">{formatElapsed(res().timings.totalMs)}</span>
              <span class="api-run__size">{formatSize(res().size)}</span>
            </>
          )}
        </Show>
        <span class="api-run__view">
          <SegmentedControl
            ariaLabel="How to read this run"
            options={VIEWS}
            value={run().view}
            onChange={(v) => props.onView(run().id, v as ApiRunView)}
          />
        </span>
      </div>

      <Show when={run().error}>{(reason) => <p class="api-run__failure">{reason()}</p>}</Show>

      <Show when={response()}>
        {(res) => (
          <Show
            when={run().view === 'raw'}
            fallback={
              <div class="api-run__body">
                <Caption>{bodySummary(res())}</Caption>
                <Show when={res().headers.length > 0}>
                  <CodeBlock ariaLabel="Response headers">{responseHeaderText(res())}</CodeBlock>
                </Show>
                <Show when={hasBodyText(res())}>
                  <CodeBlock ariaLabel="Response body">{res().text}</CodeBlock>
                </Show>
              </div>
            }
          >
            <RawExchange exchange={res().raw} connection={connectionRawText(res())} />
          </Show>
        )}
      </Show>
    </div>
  )
}

/**
 * The full text of both sides, and where it went — §11's first-class
 * requirement rather than a debugging affordance.
 *
 * Both sides come off the WIRE. The renderer composes neither: the sender is
 * the only party that knows what it put on the socket and which spans it
 * placed there (§11.2), so a request text composed here from the form would be
 * a second answer to "what was sent" — and the two would agree until the day
 * they did not, which is the failure AGENTS.md's rule about two derivations of
 * one fact is written against.
 */
function RawExchange(props: { exchange: ApiExchange; connection: string }) {
  return (
    <div class="api-run__raw">
      <Caption>── request ──</Caption>
      <CodeBlock ariaLabel="Raw request">
        <For each={rawSegments(props.exchange.request)}>{(seg) => rawSegment(seg)}</For>
      </CodeBlock>
      <Caption>── connection ──</Caption>
      <CodeBlock ariaLabel="Connection">{props.connection}</CodeBlock>
      <Caption>── response ──</Caption>
      <CodeBlock ariaLabel="Raw response">
        <For each={rawSegments(props.exchange.response)}>{(seg) => rawSegment(seg)}</For>
      </CodeBlock>
    </div>
  )
}

/**
 * One run of the raw text (design §11.1's three states, never two).
 *
 * The chips are the KIT's — `ui/secret-chip.ts` renders exactly this, and
 * ADR-0021 says the chip is the rendering while the reference is what is
 * stored and sent. Nothing here draws a span of its own with its own colours.
 *
 * A plain function rather than a `<Show>` because the discriminant is what
 * decides, and a Show would lose the narrowing that keeps `name`, `damage` and
 * `text` mutually exclusive — which is the property the chips depend on: there
 * is no branch in which a value could be reached, because no shape carries one.
 */
function rawSegment(seg: ApiRawSegment): JSX.Element {
  switch (seg.kind) {
    case 'secret':
      return createSecretChip(seg.name)
    case 'secret-damaged':
      return createSecretChipDamaged(seg.name, seg.damage)
    case 'text':
      return <span>{seg.text}</span>
  }
}
