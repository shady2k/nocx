/**
 * WHAT THIS AGENT IS EMITTING, LIVE, AND WHAT THE RULE SEES ON IT
 * (nocx-02uci; the per-agent driver configuration design, 2026-08-27 §5-§6).
 *
 * The design is explicit that this surface is not optional: "a rule the user
 * must write blind is a dead rule." So there are TWO halves here and the
 * second is the one that is easy to forget — the screen, and the rule's
 * reading of it. A view that showed only the screen would be a terminal, and
 * the person already has one of those.
 *
 * # The interval, and both its ends
 *
 * The reading is PULLED on a timer that lives with this component, and that is
 * the whole of the interval: it opens on the first read after this section
 * mounts with a pane selected, and closes when the timer is cleared — on
 * unmount, and when the person selects no pane. There is nothing to close in
 * the backend because there is nothing open there: the request is the looking,
 * so a surface that has gone away asks nothing and no pane is read for
 * somebody who is not watching it (the defect `nocx-8hdia` is an epic about).
 *
 * # It decides nothing, and that is why it is not a third power
 *
 * The AD-6 amendment grants an enrolled pane's backend grid exactly two powers
 * — whether nocx may write into the pane, and what its indicator shows — and
 * says the list is exhaustive. This page exercises neither. It cannot enrol a
 * pane, it cannot type into one, and it never lights an indicator: it hands
 * the pane's own operator a read-out of a screen they own and are already
 * looking at. What the tab's dot shows still comes from
 * session.observationChanged, from the same evaluation, through
 * pane-observation.ts.
 *
 * # Column geometry survives to the DOM
 *
 * A row's cells arrive one per COLUMN, so an index into them is a column
 * index — ADR-0041 pins the emulator for exactly that, and both permitted
 * powers are positional. The cursor is drawn by splitting a row at its cursor
 * column rather than by counting characters, because a double-width grapheme
 * occupies two columns and one character and the two counts disagree there.
 */
import { createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import {
  type EmittingBranch,
  type EmittingClient,
  type EmittingPane,
  type EmittingReading,
} from './emitting-client'
import { PageSection, Section, Select, Badge, EmptyState, FactList, CodeBlock } from './ui'
import type { Fact } from './ui'
import { Spinner } from './ui/spinner'

/**
 * How often the open view asks again.
 *
 * Half a second, and the number is a product decision rather than a technical
 * one: a person reading a screen and a rule side by side is comparing them,
 * and a grid that redraws eight times a second is a grid they cannot read a
 * row of. The backend's own classification sweep is much faster (120ms) and
 * stays that way — it feeds the indicator, which has to be prompt; this feeds
 * a pair of human eyes.
 *
 * Nothing waits on this number: every test here drives the read directly and
 * asserts on what appeared.
 */
export const EMITTING_POLL_MS = 500

export interface AgentEmittingSectionProps {
  client: EmittingClient
  /** What to call a pane, when the window knows. The backend answers with a
   *  session id and the agent's name, which is honest and unmemorable; the
   *  renderer already names every pane in its tab strip. Optional, because
   *  the surface must still work in a window that cannot answer — it then
   *  shows the agent and the id, which is what the wire said. */
  nameOf?: (sessionId: string) => string | null
}

/** The verdict's tone. `free_text` is the only state nocx may type into, so
 *  it is the only one drawn as settled; `unknown` is not a state of the agent
 *  but a state of our knowledge, and every consumer treats it as busy. */
function stateTone(
  state: EmittingReading['state'],
): 'neutral' | 'info' | 'success' | 'warning' | 'danger' {
  switch (state) {
    case 'free_text':
      return 'success'
    case 'working':
      return 'info'
    case 'permission_choice':
    case 'modal_choice':
      return 'warning'
    case 'error':
      return 'danger'
    default:
      return 'neutral'
  }
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

export function AgentEmittingSection(props: AgentEmittingSectionProps) {
  const [panes, setPanes] = createSignal<EmittingPane[]>([])
  const [selected, setSelected] = createSignal<string>('')
  const [reading, setReading] = createSignal<EmittingReading | null>(null)
  const [loaded, setLoaded] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)

  const read = async (): Promise<void> => {
    try {
      const answer = await props.client.read(selected() || undefined)
      setPanes(answer.panes)
      // The selection is adopted from the ANSWER, never held against it: a
      // pane whose observation closed leaves the list, and keeping it
      // selected would leave the person staring at a frame that stopped
      // being refreshed without saying so.
      const still = answer.panes.some((p) => p.sessionId === selected())
      if (!still) setSelected('')
      setReading(answer.reading ?? null)
      setError(null)
    } catch (e) {
      setError(messageOf(e))
      setReading(null)
    } finally {
      setLoaded(true)
    }
  }

  onMount(() => {
    void read()
    const timer = setInterval(() => void read(), EMITTING_POLL_MS)
    // THE SECOND END OF THE INTERVAL. Without this the view goes on reading a
    // pane after the person has closed it, which is the same defect as a
    // backend that keeps polling a pane nobody is looking at — only harder to
    // see, because nothing in the backend is holding it open.
    onCleanup(() => clearInterval(timer))
  })

  const options = createMemo(() => [
    { value: '', label: 'Choose a pane…' },
    ...panes().map((p) => ({
      value: p.sessionId,
      label: props.nameOf?.(p.sessionId) ?? `${p.agent} · ${p.sessionId.slice(0, 8)}`,
    })),
  ])

  /** Which anchors bound to each row, so the grid can say where the rule's
   *  own arithmetic landed. Names, not numbers: a person repairing a rule is
   *  reading the document beside this. */
  const anchorsByRow = createMemo(() => {
    const map = new Map<number, string[]>()
    for (const a of reading()?.anchors ?? []) {
      if (!a.bound || a.row === undefined) continue
      map.set(a.row, [...(map.get(a.row) ?? []), a.name])
    }
    return map
  })

  /** Every row any part of the rule was PERMITTED to search. The cap is the
   *  engine's, so seeing the span is how a person tells "the rule looked in
   *  the wrong place" from "the rule was not allowed to look that far". */
  const regionRows = createMemo(() => {
    const rows = new Set<number>()
    const add = (span?: { from: number; to: number }): void => {
      if (!span) return
      for (let y = span.from; y <= span.to; y++) rows.add(y)
    }
    for (const b of reading()?.branches ?? []) for (const p of b.predicates) add(p.region)
    for (const e of reading()?.extractors ?? []) add(e.region)
    return rows
  })

  const anchorFacts = createMemo<Fact[]>(() =>
    (reading()?.anchors ?? []).map((a) => ({
      name: a.name,
      value: a.bound ? `row ${a.row}` : 'did not bind',
      note: a.from ? `${a.kind} from ${a.from}` : a.kind,
    })),
  )

  return (
    <PageSection
      title="What this agent is emitting"
      description="The screen an enrolled pane's detection rule is reading, and what the rule sees on it — which anchor bound where, which branch answered, and for a branch that did not, the predicate it stopped at."
    >
      <Show when={error()}>
        <Badge tone="danger">{`Could not read the pane: ${error()}`}</Badge>
      </Show>
      <Show when={!loaded() && error() === null}>
        <Spinner size="sm" label="Looking for panes nocx is watching" />
      </Show>
      <Show when={loaded()}>
        <Show
          when={panes().length > 0}
          fallback={
            <EmptyState
              title="No pane is enrolled"
              description="nocx watches a pane only after an agent asks it to. Start an agent in a terminal tab and it appears here."
            />
          }
        >
          <div class="st-emitting__picker">
            <Select
              value={selected()}
              options={options()}
              onChange={(v) => {
                setSelected(v)
                setReading(null)
                void read()
              }}
            />
          </div>
        </Show>
        <Show when={reading()} keyed>
          {(r) => (
            <>
              <Section id="emitting-verdict" title="What the rule answered">
                <div class="st-emitting__verdict">
                  <Badge tone={stateTone(r.state)}>{r.state}</Badge>
                  <span class="st-emitting__from">
                    {r.matchedBranch === undefined
                      ? `no branch matched — this is the rule's fall-through default (${r.fallback})`
                      : `from branch ${r.matchedBranch}`}
                  </span>
                </div>
                <Show when={!r.hasRule}>
                  <Badge tone="warning">
                    nocx has no rule for this agent, so nothing is reading this screen yet
                  </Badge>
                </Show>
              </Section>

              <Section id="emitting-screen" title="The screen, as the rule sees it">
                <CodeBlock
                  wrap={false}
                  label="Frame"
                  ariaLabel={`Frame of ${r.agent}, ${r.frame.cols} by ${r.frame.rows}`}
                >
                  <For each={r.frame.lines}>
                    {(line, y) => (
                      <div
                        class="st-emitting__row"
                        data-rule={line.rule ? 'true' : undefined}
                        data-region={regionRows().has(y()) ? 'true' : undefined}
                        data-row={y()}
                      >
                        <span class="st-emitting__gutter">{String(y()).padStart(3, ' ')}</span>
                        <span class="st-emitting__cells">
                          <Show
                            when={y() === r.frame.cursorY && r.frame.cursorX < line.cells.length}
                            fallback={<span>{line.cells.join('')}</span>}
                          >
                            <span>{line.cells.slice(0, r.frame.cursorX).join('')}</span>
                            <span class="st-emitting__cursor" data-cursor="true">
                              {line.cells[r.frame.cursorX] || ' '}
                            </span>
                            <span>{line.cells.slice(r.frame.cursorX + 1).join('')}</span>
                          </Show>
                        </span>
                        <span class="st-emitting__marks">
                          <Show when={line.rule}>
                            <span class="st-emitting__mark">full-width rule</span>
                          </Show>
                          <For each={anchorsByRow().get(y()) ?? []}>
                            {(name) => <span class="st-emitting__mark">{name}</span>}
                          </For>
                        </span>
                      </div>
                    )}
                  </For>
                </CodeBlock>
              </Section>

              <Section id="emitting-anchors" title="Where the anchors bound">
                <FactList facts={anchorFacts()} />
              </Section>

              <Section id="emitting-branches" title="The branches, in the order they were tried">
                <For each={r.branches}>
                  {(branch, i) => <BranchRow branch={branch} index={i()} />}
                </For>
              </Section>

              <Show when={r.extractors.length > 0}>
                <Section id="emitting-extractors" title="What the extractors read">
                  <For each={r.extractors}>
                    {(e) => (
                      <div class="st-emitting__extractor" data-extractor={e.name}>
                        <span class="st-emitting__name">{e.name}</span>
                        <span class="st-emitting__detail">
                          {e.region
                            ? `rows ${e.region.from}–${e.region.to}, from ${e.anchor}`
                            : `${e.anchor} did not bind, so it never ran`}
                        </span>
                        <For each={e.rows}>
                          {(row) => (
                            <span class="st-emitting__yield">
                              {row.fields.map((f) => `${f.name}=${f.value}`).join('  ')}
                            </span>
                          )}
                        </For>
                      </div>
                    )}
                  </For>
                </Section>
              </Show>
            </>
          )}
        </Show>
      </Show>
    </PageSection>
  )
}

/** One branch of the walk.
 *
 * The three states a branch can be in are drawn apart, because collapsing any
 * two of them sends a person to the wrong line: it MATCHED; it was tried and
 * stopped at a predicate; or it was never reached at all, because an earlier
 * branch had already answered. Branch order is a safety property of the
 * document — the dialog branches come before the free-text branch so a dialog
 * can never be masked by an input box drawn beneath it — so a person who
 * cannot see that a later branch was never asked will "fix" one that could not
 * have run. */
function BranchRow(props: { branch: EmittingBranch; index: number }) {
  const stopped = createMemo(() => props.branch.predicates.findIndex((p) => p.evaluated && !p.held))
  return (
    <div
      class="st-emitting__branch"
      data-branch={props.index}
      data-matched={props.branch.matched ? 'true' : undefined}
      data-reached={props.branch.reached ? 'true' : 'false'}
    >
      <span class="st-emitting__name">
        {`${props.index}. ${props.branch.state || 'answers nothing on its own'}`}
      </span>
      <span class="st-emitting__detail">
        {!props.branch.reached
          ? 'never reached — an earlier branch had already answered'
          : props.branch.matched
            ? 'matched'
            : stopped() >= 0
              ? `stopped at predicate ${stopped()}`
              : 'did not match'}
      </span>
      <Show when={props.branch.below} keyed>
        {(below) => (
          <span class="st-emitting__detail" data-below="true">
            {below.anchorBound
              ? `below ${below.anchor}: ${below.verdict} (${below.glyphs.join(' ')})`
              : `below ${below.anchor}: the anchor did not bind, so this decided nothing`}
          </span>
        )}
      </Show>
      <For each={props.branch.predicates}>
        {(p, i) => (
          <span
            class="st-emitting__predicate"
            data-predicate={p.kind}
            data-evaluated={p.evaluated ? 'true' : 'false'}
            data-held={p.evaluated ? (p.held ? 'true' : 'false') : undefined}
          >
            <span class="st-emitting__kind">{`${i()}. ${p.kind}`}</span>
            <span class="st-emitting__detail">
              {!p.evaluated
                ? 'never asked — the branch had already stopped'
                : [
                    p.held ? 'held' : 'did not hold',
                    p.anchor ? `anchor ${p.anchor}` : '',
                    p.detail ?? '',
                    p.region ? `rows ${p.region.from}–${p.region.to}` : '',
                  ]
                    .filter((s) => s !== '')
                    .join(' · ')}
            </span>
          </span>
        )}
      </For>
    </div>
  )
}
