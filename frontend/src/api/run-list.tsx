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
//
// BODY AND RAW ARE TWO DIFFERENT QUESTIONS and neither may be answered with
// the other's answer. Raw is "what came off the socket", byte for byte, and it
// is composed by the SENDER (§11.2) — nothing here touches it. Body is "what
// does this answer SAY", and a minified JSON payload does not say it: a
// 154-byte reply was read by dragging a scrollbar sideways, and a large one
// could not be read at all. So the Body tab lays JSON out for reading, and
// everything the run RECORDED — the size, and the raw view — stays the bytes
// off the socket (nocx-dhojo).

import { For, Show, createEffect, createMemo, createSignal, onCleanup, type JSX } from 'solid-js'
import { Badge } from '../ui/badge'
import { Caption } from '../ui/caption'
import { CodeBlock } from '../ui/code-block'
import { StatusCard } from '../ui/status-card'
import { Tabs } from '../ui/tabs'
import { EmptyState } from '../ui/empty-state'
import { StatusDot } from '../ui/status-dot'
import { createSecretChip, createSecretChipDamaged } from '../ui/secret-chip'
import { layOutJSON } from '../ui/format-json'
import { ResponseBody } from './response-body'
import type { ApiRun, ApiRunView } from './api-store'
import { TimingBar } from './timing-bar'
import {
  type ApiCertificate,
  type ApiTimings,
  type ApiRaw,
  type ApiRawSegment,
  bodySummary,
  certificateText,
  acceptedUntrusted,
  connectionRawText,
  formatElapsed,
  formatSize,
  hasBodyText,
  isJSONResponse,
  phaseSentence,
  rawSegments,
  responseHeaderText,
  untrustedSentence,
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
  const elapsed = pendingElapsed(props)
  /** The verdict a warning is drawn from, and undefined for the three states
   *  that are not warnings — including the quiet one, which is a line in the
   *  connection block rather than a badge. */
  const untrusted = () => {
    const trust = run().response?.trust
    return acceptedUntrusted(trust) ? trust : undefined
  }

  /**
   * The body as the BODY TAB shows it, and the sentence that goes under it.
   *
   * WHAT DECIDES WHETHER TO TRY is still the CONTENT TYPE the server sent
   * (api-model.ts) — the bytes are parsed only after the header has said they
   * are JSON, so this is not the panel guessing at a payload and it cannot
   * start arguing with a server that declared one thing and sent another.
   *
   * THREE OUTCOMES, and only one of them is a layout. A body that is not
   * declared JSON is shown as it arrived. A body that is declared JSON and
   * does not parse is shown as it arrived too — with nothing claiming it was
   * formatted, and with the JSON grammar's own gutter marking where it stops
   * being JSON, which is a better sentence than any this component could
   * write. A body too large to lay out cheaply is shown as it arrived AND
   * SAYS SO: the work is a main-thread parse that cannot be interrupted, so
   * the alternative is freezing the pane it is being read in, and a soft
   * degrade nothing on screen mentions is how a feature that does not exist
   * survives a release.
   *
   * A memo because the body of one run never changes and the list re-renders
   * around it — re-parsing a payload on every render of the column is the
   * cost this is here to avoid.
   */
  const readableBody = createMemo((): { text: string; note: string } | null => {
    const res = response()
    if (!res || !hasBodyText(res)) return null
    if (!isJSONResponse(res)) return { text: res.text, note: '' }
    const laid = layOutJSON(res.text)
    if (laid.kind === 'laid-out') return { text: laid.text, note: '' }
    if (laid.kind === 'too-large') {
      return {
        text: res.text,
        note: `Shown as it arrived: over ${laid.limit / 1024}K characters is too large to lay out here.`,
      }
    }
    return { text: res.text, note: '' }
  })

  // THE TABS EXIST FOR AN ATTEMPT, not for an answer. A failed run has a
  // request, a route and how far it got, and the raw view is where a person
  // reads them — so the tab row appears as soon as there is an exchange to
  // show, and Body and Headers simply are not among the tabs when nothing
  // answered. Before this the whole viewer was behind `response`, which is
  // why a failure was one red sentence and nothing else.
  const tabs = () => {
    const items = []
    const res = response()
    if (res) {
      items.push({
        id: 'body',
        label: 'Body',
        content: () => (
          <Show when={readableBody()} fallback={<Caption>{bodySummary(res)}</Caption>}>
            {(view) => (
              <>
                <ResponseBody
                  ariaLabel="Response body"
                  text={view().text}
                  language={isJSONResponse(res) ? 'json' : 'text'}
                />
                {/* Present only when something was NOT done and the reason is
                    worth a sentence. Nothing here ever claims a body WAS laid
                    out — that is visible, and a label on the ordinary case is
                    noise. */}
                <Show when={view().note !== ''}>
                  <Caption>{view().note}</Caption>
                </Show>
                {/* The summary stays for the three states that are not
                    "here it is": truncated, lossy, and the size itself
                    (§12.3). Four sentences, still four — and the size is the
                    run's own, off the socket, whatever the tab is showing. */}
                <Caption>{bodySummary(res)}</Caption>
              </>
            )}
          </Show>
        ),
      })
      items.push({
        id: 'headers',
        label: `Headers ${res.headers.length}`,
        content: () => (
          <ResponseBody
            ariaLabel="Response headers"
            text={responseHeaderText(res)}
            language="text"
          />
        ),
      })
    }
    const request = run().request
    if (request) {
      items.push({
        id: 'raw',
        label: 'Raw',
        content: () => (
          <RawExchange
            request={request}
            response={res?.raw ?? null}
            connection={connectionRawText({
              remoteAddr: run().remoteAddr,
              dnsAddresses: run().dnsAddresses,
              routedThrough:
                run().route.kind === 'connection'
                  ? props.connectionName(run().route.profileId)
                  : undefined,
              tlsVersion: res?.tlsVersion,
              tlsCipherSuite: res?.tlsCipherSuite,
              trust: res?.trust,
            })}
            timings={run().timings ?? EMPTY_TIMINGS}
            certificates={run().certificates}
          />
        ),
      })
    }
    return items
  }

  // Which tab is showing has to be one the run HAS. A failed run offers only
  // Raw, and a stored choice of "body" from a previous look would select
  // nothing at all — an empty pane where the one thing there is to read was.
  const active = () => {
    const items = tabs()
    return items.some((i) => i.id === run().view) ? run().view : (items[0]?.id ?? 'raw')
  }

  return (
    <div class="api-run" data-run-id={run().id} data-outcome={run().outcome}>
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
          run().environment !== '' || run().route.kind === 'connection' || untrusted() !== undefined
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
          {/* AND WHETHER THIS RUN ACCEPTED SOMETHING IT WOULD OTHERWISE HAVE
              REFUSED — the backend's verdict on the chain, never the
              environment's setting.
              It was the setting (`route.insecureTls`), which is true of
              every run under that environment: the badge sat on
              `https://httpbin.org` — a public host with an ordinary chain —
              in the same colour and words a self-signed development host
              would get. A warning that is on most of the time is a warning
              nobody reads, and the one run where it mattered looked exactly
              like the twenty where it did not. The quiet case — verification
              off over a chain that would have passed anyway — is a line in
              the connection block instead (api-model.ts), because it is a
              fact and not a warning. */}
          <Show when={untrusted()}>
            {(trust) => <Badge tone="warning">{untrustedSentence(trust())}</Badge>}
          </Show>
        </div>
      </Show>

      {/* THE RUN THAT HAS NOT ANSWERED YET. It is the whole reason this row
          exists before its answer does: a person who has pressed Send can see
          that something is happening and how long it has been happening for.
          The clock is the RENDERER's — the backend reports timings for an
          exchange that has ended, and this is the one number that exists
          while it has not. */}
      <Show when={run().outcome === 'pending'}>
        <div class="api-run__state">
          <Badge tone="info">Sending…</Badge>
          <span class="api-run__elapsed">{formatElapsed(elapsed())}</span>
        </div>
      </Show>

      {/* THE ASK THAT NEVER BECAME AN EXCHANGE — an unknown handle, an auth
          variable nothing can resolve. There is no request, no route and no
          phase, because nothing was attempted, so the card IS the whole
          story here. It is the kit's danger card, which is what "this did
          not work, and here is what was said" looks like everywhere else in
          the product. */}
      <Show when={run().outcome === 'refused' && run().error !== ''}>
        <StatusCard tone="danger" title="The request did not go out" description={run().error} />
      </Show>

      {/* AN ATTEMPT THAT ENDED WITHOUT AN ANSWER. The card says WHERE it
          stopped in the product's words and what the backend said underneath;
          the tabs below carry the rest — the request text, the route, how far
          it got. So the card is no longer the whole story of a failure.
          A STOP IS NOT TONED AS ONE: the person ended it on purpose, and a
          red box telling them so would be the product disagreeing with them
          about something they did. */}
      <Show when={run().failure}>
        {(failure) => (
          <StatusCard
            tone={run().outcome === 'stopped' ? 'neutral' : 'danger'}
            title={run().outcome === 'stopped' ? 'Stopped' : phaseSentence(failure().phase)}
            description={failure().reason}
          />
        )}
      </Show>

      <Show when={tabs().length > 0}>
        <Tabs
          orientation="horizontal"
          ariaLabel="This exchange"
          active={active()}
          onChange={(v) => props.onView(run().id, v as ApiRunView)}
          // THE NUMBERS RIDE THE TAB ROW, which is where the owner's
          // reference puts them and where they cost no line of their own:
          // what came back, how long it took and how much of it there was
          // are one glance, beside the choice of what to look at.
          actions={
            <span class="api-run__stats">
              <Show when={response()}>
                {(res) => (
                  <>
                    <StatusDot
                      tone={statusTone(res().status)}
                      accessibleName={`HTTP status ${res().status}`}
                    >
                      <span>{String(res().status)}</span>
                    </StatusDot>
                    <span class="api-run__size">{formatSize(res().size)}</span>
                  </>
                )}
              </Show>
              {/* The time is the ATTEMPT's, so it is here for a run that
                  failed as well: how long it spent before it gave up is the
                  difference between a refusal and a hang. */}
              <Show when={run().timings}>
                {(t) => <span class="api-run__elapsed">{formatElapsed(t().totalMs)}</span>}
              </Show>
            </span>
          }
          items={tabs()}
        />
      </Show>
    </div>
  )
}

/** A run that never reached the network has no phases, and zeroes are what
 *  "never reached" means for every one of them (contracts/api.request.send).
 *  It is a constant rather than a literal at the call site so the shape is
 *  declared once. */
const EMPTY_TIMINGS = { dnsMs: 0, connectMs: 0, tlsMs: 0, ttfbMs: 0, totalMs: 0 }

/**
 * How long the run in front of a person has been going, in milliseconds.
 *
 * A ticking signal rather than a duration anybody waits on: it exists ONLY
 * while the run is pending, is disposed with the component, and stops the
 * moment the exchange settles — after which the run's own totalMs is the
 * number, because that one is the backend's measurement of the exchange
 * rather than the renderer's of the round trip.
 */
function pendingElapsed(props: { run: ApiRun }): () => number {
  const [now, setNow] = createSignal(Date.now())
  // The props object rather than an accessor over it, so every read of the
  // run happens inside a tracked scope: the effect that owns the timer, and
  // the memo that computes the number.
  createEffect(() => {
    if (props.run.outcome !== 'pending') return
    const timer = setInterval(() => setNow(Date.now()), TICK_MS)
    onCleanup(() => clearInterval(timer))
  })
  const elapsed = createMemo(() => Math.max(0, now() - props.run.startedAt))
  return elapsed
}

/** Often enough that the number visibly moves, rarely enough that a column
 *  of runs is not re-rendering continuously. */
const TICK_MS = 100

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
  request: ApiRaw
  /** The response side, or null when nothing answered. Two props rather than
   *  one pair, because they are known at two different times: the request is
   *  composed before the dial and the response exists only if there was one
   *  (api-model.ts). */
  response: ApiRaw | null
  connection: string
  /** How the elapsed time was spent — drawn, because five numbers in a row
   *  make the reader do the subtraction that answers "where did it go". */
  timings: ApiTimings
  certificates: readonly ApiCertificate[]
}) {
  return (
    <div class="api-run__raw">
      {/* THE SAME ANSWER THE EDITOR GIVES, because these are the same octets:
          the request preview does not wrap, so a long header or a one-line
          body is reached by scrolling sideways inside the block rather than
          folded into a paragraph that no longer looks like what went out
          (nocx-kdawd). The connection block and the certificates below keep
          wrapping — they are text this renderer composed out of facts, not
          bytes off the wire, and a fingerprint is a thing people copy out of
          a block that shows it whole. */}
      <Caption>── request ──</Caption>
      <CodeBlock ariaLabel="Raw request" wrap={false}>
        <For each={rawSegments(props.request)}>{(seg) => rawSegment(seg)}</For>
      </CodeBlock>
      <Caption>── connection ──</Caption>
      <Show when={props.connection !== ''}>
        <CodeBlock ariaLabel="Connection">{props.connection}</CodeBlock>
      </Show>
      <TimingBar timings={props.timings} />
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
      {/* ABSENT rather than empty when nothing answered. A "── response ──"
          heading over an empty block would be the product saying a server
          replied with nothing, which is a different fact from not replying. */}
      <Show when={props.response}>
        {(raw) => (
          <>
            <Caption>── response ──</Caption>
            <CodeBlock ariaLabel="Raw response" wrap={false}>
              <For each={rawSegments(raw())}>{(seg) => rawSegment(seg)}</For>
            </CodeBlock>
          </>
        )}
      </Show>
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
