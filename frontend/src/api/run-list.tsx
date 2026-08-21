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
// WHICH PART is being read belongs to ONE run. A single flag for the list
// would mean opening the raw text of the run you are reading also opens it
// for the nineteen above it.
//
// The three parts are TABS rather than a stack. Headers used to sit above the
// body in one pane, each with its own scroll box, so a long body pushed the
// headers off screen and twenty-four headers pushed the body off — two things
// competing for one column when a person looks at one of them at a time.

import { For, Show, type JSX } from 'solid-js'
import { Badge } from '../ui/badge'
import { Caption } from '../ui/caption'
import { CodeBlock } from '../ui/code-block'
import { StatusCard } from '../ui/status-card'
import { Tabs } from '../ui/tabs'
import { EmptyState } from '../ui/empty-state'
import { StatusDot } from '../ui/status-dot'
import { createSecretChip, createSecretChipDamaged } from '../ui/secret-chip'
import { ResponseBody } from './response-body'
import type { ApiRun, ApiRunView } from './api-store'
import {
  type ApiCertificate,
  type ApiExchange,
  type ApiRawSegment,
  bodySummary,
  certificateText,
  connectionRawText,
  formatElapsed,
  formatSize,
  hasBodyText,
  isJSONResponse,
  rawSegments,
  responseHeaderText,
  statusTone,
} from './api-model'

export interface RunListProps {
  runs: readonly ApiRun[]
  onView: (id: number, view: ApiRunView) => void
  /** What a connection is CALLED, by the id a run reports. The run carries
   *  the id — that is the backend's fact — and the name belongs to whoever
   *  owns connections (AD-8), so the surface hands the translation in. */
  connectionName: (profileId: string) => string
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
        <For each={props.runs}>
          {(run) => <Run run={run} onView={props.onView} connectionName={props.connectionName} />}
        </For>
      </Show>
    </div>
  )
}

function Run(props: {
  run: ApiRun
  onView: (id: number, view: ApiRunView) => void
  connectionName: (profileId: string) => string
}) {
  const run = () => props.run
  const response = () => props.run.response

  return (
    <div class="api-run" data-run-id={run().id}>
      {/* WHAT WAS SENT, which the response pane owes a person as much as what
          came back: this is a LIST, so a card that only said "404" would be a
          number with no question attached to it. */}
      {/* TWO LINES, and the reason is what each is for. The first is the
          exchange itself — verb and address — and an address is the longest
          thing on this surface; the second is what it went out under. On one
          line the badges took the width and the URL was elided to
          `https://localhost:…`, which is the one part of a run that must
          stay readable. */}
      <div class="api-run__head">
        <span class="api-run__verb">
          <Badge tone="neutral">{run().method}</Badge>
        </span>
        <span class="api-run__url" title={run().url}>
          {run().url}
        </span>
      </div>
      <Show
        when={
          run().environment !== '' || run().route.kind === 'connection' || run().route.insecureTls
        }
      >
        <div class="api-run__under">
          {/* Which environment answered — in the BACKEND's words, off the
              send result. Absent when the send named none, which is a
              request that went out exactly as its file has it; a badge
              reading "None" would be a label on the ordinary case. */}
          <Show when={run().environment !== ''}>
            <Badge tone="neutral">{run().environment}</Badge>
          </Show>
          {/* WHERE IT LEFT FROM. Absent on a direct send, which needs no
              label — a badge reading "this machine" on every run would be
              noise. Present when it went through a connection, because that
              is the one fact a person routing through a bastion needs on the
              answer: not which environment was chosen, but which host
              actually dialled. */}
          <Show when={run().route.kind === 'connection'}>
            <Badge tone="info">{`via ${props.connectionName(run().route.profileId)}`}</Badge>
          </Show>
          {/* AND WHETHER IT CHECKED WHO ANSWERED. A warning tone, on the run
              rather than only in the file that allowed it: an environment
              with verification off is a setting somebody turned on for one
              host and will forget, and the run is where they will be looking
              when it matters. */}
          <Show when={run().route.insecureTls}>
            <Badge tone="warning">unverified TLS</Badge>
          </Show>
        </div>
      </Show>

      {/* A SEND THAT NEVER LANDED IS STILL A RUN. It used to be one red line
          under the head — the same weight as a caption, in a column where
          everything else has a box — so the one state a person most needs to
          read was the one that looked like an aside. It is the kit's danger
          card now, which is what "this did not work, and here is what was
          said" looks like everywhere else in the product. */}
      <Show when={run().error}>
        {(reason) => (
          <StatusCard tone="danger" title="The request did not go out" description={reason()} />
        )}
      </Show>

      <Show when={response()}>
        {(res) => (
          <Tabs
            orientation="horizontal"
            ariaLabel="This exchange"
            active={run().view}
            onChange={(v) => props.onView(run().id, v as ApiRunView)}
            // THE NUMBERS RIDE THE TAB ROW, which is where the owner's
            // reference puts them and where they cost no line of their own:
            // what came back, how long it took and how much of it there was
            // are one glance, beside the choice of what to look at.
            actions={
              <span class="api-run__stats">
                <StatusDot
                  tone={statusTone(res().status)}
                  accessibleName={`HTTP status ${res().status}`}
                >
                  <span>{String(res().status)}</span>
                </StatusDot>
                <span class="api-run__elapsed">{formatElapsed(res().timings.totalMs)}</span>
                <span class="api-run__size">{formatSize(res().size)}</span>
              </span>
            }
            items={[
              {
                id: 'body',
                label: 'Body',
                content: () => (
                  <Show
                    when={hasBodyText(res())}
                    fallback={<Caption>{bodySummary(res())}</Caption>}
                  >
                    <ResponseBody
                      ariaLabel="Response body"
                      text={res().text}
                      language={isJSONResponse(res()) ? 'json' : 'text'}
                    />
                    {/* The summary stays for the three states that are not
                        "here it is": truncated, lossy, and the size itself
                        (§12.3). Four sentences, still four. */}
                    <Caption>{bodySummary(res())}</Caption>
                  </Show>
                ),
              },
              {
                id: 'headers',
                label: `Headers ${res().headers.length}`,
                content: () => (
                  <ResponseBody
                    ariaLabel="Response headers"
                    text={responseHeaderText(res())}
                    language="text"
                  />
                ),
              },
              {
                id: 'raw',
                label: 'Raw',
                content: () => (
                  <RawExchange
                    exchange={res().raw}
                    connection={connectionRawText(res())}
                    certificates={res().certificates}
                  />
                ),
              },
            ]}
          />
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
function RawExchange(props: {
  exchange: ApiExchange
  connection: string
  certificates: readonly ApiCertificate[]
}) {
  return (
    <div class="api-run__raw">
      <Caption>── request ──</Caption>
      <CodeBlock ariaLabel="Raw request">
        <For each={rawSegments(props.exchange.request)}>{(seg) => rawSegment(seg)}</For>
      </CodeBlock>
      <Caption>── connection ──</Caption>
      <CodeBlock ariaLabel="Connection">{props.connection}</CodeBlock>
      {/* WHAT WAS ON THE OTHER END. Present for any TLS exchange, not only
          the unverified ones — but it is the unverified ones it exists for:
          with the check off, "which certificate did I just trust" is the
          only question left, and it had no answer anywhere in the product.
          Leaf first, the whole presented chain, as text — because a
          fingerprint is a thing people COPY out of a pane and into a
          terminal. */}
      <Show when={props.certificates.length > 0}>
        <Caption>── certificate chain ──</Caption>
        <For each={props.certificates}>
          {(cert, i) => (
            <CodeBlock ariaLabel={`Certificate ${i() + 1}`}>{certificateText(cert, i())}</CodeBlock>
          )}
        </For>
      </Show>
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
