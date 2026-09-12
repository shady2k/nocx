/**
 * AGENT RULES: the rule that reads an agent's pane, written by the person who
 * runs it (nocx-y6w66).
 *
 * nocx reads an agent pane's state from a rule, and the rules used to be
 * compile-time — a person whose agent nocx read wrongly could do nothing but
 * wait for a release. This page is where they write one instead, switch
 * detection off for an agent, or go back to the rule nocx ships.
 *
 * THREE THINGS IT IS CAREFUL ABOUT, and each is a state a person is actually
 * in:
 *
 * The document starts from the SHIPPED rule when they have not written one, so
 * the first edit is a repair of something that works rather than a blank page.
 * Editing is not switching on: a save while detection is off leaves it off.
 * And a document that cannot be read is shown as a problem with a way out
 * (remove it), never as the shipped rule — a page filled with a rule that is
 * not in force would invite a save that overwrites what its author wrote.
 *
 * There is no poll. A rule is a document a person types into, and a re-read
 * landing mid-sentence would redraw the text out from under them; every write
 * answers with the state it produced, which is the only refresh this page
 * needs.
 */
import { createSignal, For, onMount, Show } from 'solid-js'
import { createStore } from 'solid-js/store'

import type {
  AgentRuleRow,
  AgentRules,
  AgentRulesClient,
  AgentRuleState,
} from './agent-rules-client'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { EmptyState } from './ui/empty-state'
import { PageSection } from './ui/page-section'
import { Section } from './ui/section'
import { Spinner } from './ui/spinner'
import { StatusCard } from './ui/status-card'
import { TextField } from './ui/text-field'

export interface AgentRulesSectionProps {
  client: AgentRulesClient
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** The badge's word for a state. It is the state's own name, because the
 *  vocabulary is the contract's (contracts/agent.rules.schema.json) and a
 *  second spelling here is a second thing to keep in step. */
function stateLabel(state: AgentRuleState): string {
  switch (state) {
    case 'shipped':
      return 'shipped rule'
    case 'user':
      return 'your rule'
    case 'disabled':
      return 'detection off'
    case 'unreadable':
      return 'cannot be read'
  }
}

function stateTone(state: AgentRuleState): 'neutral' | 'info' | 'warning' | 'danger' {
  switch (state) {
    case 'shipped':
      return 'neutral'
    case 'user':
      return 'info'
    case 'disabled':
      return 'warning'
    case 'unreadable':
      return 'danger'
  }
}

/**
 * WHAT NOCX IS READING THIS AGENT WITH, said as what it will do rather than as
 * a code. The two silent states are named apart on purpose: one is a control
 * the person pressed and can press again, the other is a file they broke and
 * whose text this page cannot show them.
 */
function stateSentence(row: AgentRuleRow): string {
  switch (row.state) {
    case 'shipped':
      return `nocx is reading ${row.agent} with the rule it ships — the document below is that rule, and saving it gives you a copy of your own to keep.`
    case 'user':
      return `nocx is reading ${row.agent} with the document below, in place of the rule it ships.`
    case 'disabled':
      return `Detection is switched off for ${row.agent}: the pane reports “unknown”, which reads as busy, and nocx will not type into it. Your document is still here and switching detection back on returns it.`
    case 'unreadable':
      return `Your document for ${row.agent} could not be used, so the pane reports “unknown” and nocx will not type into it — not the shipped rule, because falling back to it would hide from you that your edit is not in force.`
  }
}

export function AgentRulesSection(props: AgentRulesSectionProps) {
  const [rules, setRules] = createSignal<AgentRuleRow[]>([])
  const [directory, setDirectory] = createSignal('')
  const [drafts, setDrafts] = createStore<Record<string, string>>({})
  const [loaded, setLoaded] = createSignal(false)
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [refused, setRefused] = createSignal<Record<string, string>>({})

  /**
   * Adopt an answer as the whole truth. Every write replies with the state it
   * produced, so a draft is seeded from the ANSWER rather than kept beside it —
   * except that a failed write leaves the text where the person typed it, which
   * is why the draft is only rewritten when an answer arrives.
   */
  const adopt = (answer: AgentRules): void => {
    setRules(answer.rules)
    setDirectory(answer.directory)
    for (const row of answer.rules) setDrafts(row.agent, row.document)
    setRefused({})
    setError(null)
  }

  /** One call at a time. A person editing a document is not a source of
   *  concurrency, and two writes racing over one file is two answers to which
   *  rule is in force. */
  const start = (): boolean => {
    if (busy()) return false
    setBusy(true)
    return true
  }

  const finish = (): void => {
    // Loaded either way: a page that failed its first read must say so rather
    // than spin forever, and a spinner beside a refusal is two answers to one
    // question.
    setLoaded(true)
    setBusy(false)
  }

  /**
   * A write that failed is the product working, and the reason belongs beside
   * the document it was about — a person's text is in that field, and losing
   * it to a page-level banner is how a repair becomes a retype.
   */
  const refuse = (agent: string, e: unknown): void => {
    setRefused((prev) => ({ ...prev, [agent]: messageOf(e) }))
  }

  const read = async (): Promise<void> => {
    if (!start()) return
    try {
      adopt(await props.client.read())
    } catch (e) {
      setError(messageOf(e))
    } finally {
      finish()
    }
  }

  const save = async (row: AgentRuleRow): Promise<void> => {
    if (!start()) return
    try {
      adopt(await props.client.set(row.agent, drafts[row.agent] ?? ''))
    } catch (e) {
      refuse(row.agent, e)
    } finally {
      finish()
    }
  }

  const toggle = async (row: AgentRuleRow, enabled: boolean): Promise<void> => {
    if (!start()) return
    try {
      adopt(await props.client.setEnabled(row.agent, enabled))
    } catch (e) {
      refuse(row.agent, e)
    } finally {
      finish()
    }
  }

  const remove = async (row: AgentRuleRow): Promise<void> => {
    if (!start()) return
    try {
      adopt(await props.client.remove(row.agent))
    } catch (e) {
      refuse(row.agent, e)
    } finally {
      finish()
    }
  }

  onMount(() => {
    void read()
  })

  return (
    <PageSection
      title="Agent rules"
      description="nocx reads an agent's screen with a rule. These are the rules it ships and the ones you have written: edit a document for an agent nocx reads wrongly, switch detection off for one it should leave alone, or remove your document to go back to the shipped rule."
    >
      <Show when={error()}>
        <Badge tone="danger">{error()!}</Badge>
      </Show>
      <Show when={!loaded() && error() === null}>
        <Spinner size="sm" label="Reading the rules" />
      </Show>
      {/* A read that failed says so and offers to try again, and it does NOT
          fall through to the empty state below: "this build ships no rules" is
          a claim about the build, and a window that could not ask has no
          business making it. */}
      <Show when={loaded() && error() !== null}>
        <div class="st-rules__controls">
          <Button
            variant="default"
            disabled={busy()}
            onClick={() => void read()}
            data-rule-action="reread"
          >
            Read again
          </Button>
        </div>
      </Show>
      <Show when={loaded() && error() === null}>
        <Show
          when={rules().length > 0}
          fallback={
            <EmptyState
              title="No agent has a rule in this build"
              description="A rule is written per agent, and this build ships none — so there is nothing here to edit."
            />
          }
        >
          <For each={rules()}>
            {(row) => (
              <Section id={`rule-${row.agent}`} title={row.agent}>
                <div class="st-rules__state" data-rule-state={row.state}>
                  <Badge tone={stateTone(row.state)}>{stateLabel(row.state)}</Badge>
                  <span class="st-rules__summary">{stateSentence(row)}</span>
                </div>

                <Show when={row.state === 'unreadable'}>
                  <StatusCard
                    tone="danger"
                    title={`The document for ${row.agent} could not be read`}
                    description={`${row.problem ?? ''} Fix the file, or remove it below to go back to the shipped rule.`}
                  />
                </Show>

                <Show when={row.state !== 'unreadable'}>
                  <TextField
                    id={`rule-document-${row.agent}`}
                    label={`Rule for ${row.agent}`}
                    description="One JSON document, in the same grammar the shipped rules are written in. The backend compiles it before it is stored, so a document it could not read is refused here rather than quietly leaving the pane unread."
                    multiline
                    wrap
                    rows={16}
                    value={drafts[row.agent] ?? ''}
                    error={refused()[row.agent]}
                    onInput={(value) => setDrafts(row.agent, value)}
                  />
                </Show>

                <div class="st-rules__controls">
                  <Checkbox
                    variant="switch"
                    checked={row.state === 'shipped' || row.state === 'user'}
                    label={`Read ${row.agent}'s screen`}
                    disabled={busy() || row.state === 'unreadable'}
                    onChange={(checked) => toggle(row, checked)}
                  />
                  <Button
                    variant="primary"
                    disabled={busy() || row.state === 'unreadable'}
                    onClick={() => void save(row)}
                    data-rule-action="save"
                  >
                    Save this rule
                  </Button>
                  <Button
                    variant="default"
                    disabled={busy() || row.state === 'shipped'}
                    onClick={() => void remove(row)}
                    data-rule-action="remove"
                  >
                    Remove mine, use the shipped rule
                  </Button>
                </div>
              </Section>
            )}
          </For>
          <p class="st-rules__files">
            These are files as well as a page: one document per agent in <code>{directory()}</code>,
            so a rule can be written or repaired in an editor and nocx picks it up on the next pane
            it reads.
          </p>
        </Show>
      </Show>
    </PageSection>
  )
}
