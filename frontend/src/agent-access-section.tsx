/**
 * WHICH AGENTS MAY USE NOCX'S TOOLS, AND UNMAKING AN ANSWER (nocx-6jbad).
 *
 * The window that asks "allow this agent and commands it launches" writes a
 * DURABLE answer, and a denial is never silently retried (nocx-rowqt.12) —
 * which is right, because a question that comes back every time is one a
 * person can be worn down into answering yes. It is only defensible while
 * there is somewhere to reconsider, and there was not: no surface read
 * agent-approvals.json, so a person who pressed Deny once had no move inside
 * the product at all. That is what this page is.
 *
 * IT IS NOT THE ASSISTANT'S PERMISSIONS, and the two pages are next to each
 * other, so the distinction has to be in the words. That page governs what
 * NOCX'S OWN assistant may do on its own. This one lists the FOREIGN
 * programs — claude, codex, whatever the person typed — that were admitted
 * to nocx's tools, or refused them. Same rail, different subjects
 * (nocx-t72hg is the regrouping this page will move under).
 *
 * A PULL, and the request is the whole of the interval: the page reads on
 * mount and after each forget, and asks nothing when it is not open.
 *
 * WHAT A FORGET DOES, said on the page rather than left to be discovered: the
 * answer goes, so the next start of that agent asks again. It does not reach
 * into a pane already running — it does not have to, because the admit check
 * reads the store on every call, so an agent whose answer is gone has its
 * next tool call refused and goes on running without nocx's tools.
 */
import { createSignal, For, onMount, Show } from 'solid-js'
import type { AgentAccessAnswer, AgentAccessClient } from './agent-access-client'
import { Button } from './ui/button'
import { PageSection } from './ui/page-section'
import { RecordRow } from './ui/record-row'
import { showToast } from './ui/toast'

export interface AgentAccessSectionProps {
  client: AgentAccessClient
}

export function AgentAccessSection(props: AgentAccessSectionProps) {
  const [answers, setAnswers] = createSignal<readonly AgentAccessAnswer[]>([])
  const [loaded, setLoaded] = createSignal(false)
  const [busy, setBusy] = createSignal('')

  const refresh = async () => {
    try {
      const result = await props.client.list()
      setAnswers(result.answers)
      // ONLY on a read that answered. In a `finally` this flag would draw the
      // empty state over a document nocx could not read, which says "you have
      // decided nothing" to a person who has decided something — the silent
      // degrade this file's own comment is about, written and then committed
      // three lines later.
      setLoaded(true)
    } catch (err) {
      // Said out loud rather than left as an empty list: "nothing is
      // remembered" and "nocx could not read what is remembered" are
      // different facts, and drawing the first over the second is the silent
      // degrade AGENTS.md names.
      showToast({
        level: 'danger',
        message: `Could not read which agents are allowed: ${message(err)}`,
        duration: 0,
      })
    }
  }

  onMount(() => {
    void refresh()
  })

  const forget = async (answer: AgentAccessAnswer) => {
    setBusy(key(answer))
    try {
      await props.client.forget(answer)
      // Refreshed either way, including on forgotten:false — the list this
      // page holds is a read that may predate somebody else's forget.
      await refresh()
    } catch (err) {
      showToast({
        level: 'danger',
        message: `Could not forget this answer: ${message(err)}`,
        duration: 0,
      })
    } finally {
      setBusy('')
    }
  }

  return (
    <PageSection
      title="Agent access"
      description="Agents you started in a pane and answered for when they asked to use nocx's tools — starting other agents in new tabs, watching them, and stopping them. Forgetting an answer means the next start of that agent asks again."
    >
      <Show
        when={answers().length > 0}
        fallback={
          <Show when={loaded()}>
            <p>
              No agent has asked yet. The question comes up the first time you start one in a pane.
            </p>
          </Show>
        }
      >
        <For each={answers()}>
          {(answer) => (
            <div data-agent-access={key(answer)}>
              <RecordRow
                title={answer.executable}
                meta={`Every tab in the ${answer.workspace} workspace · ${answer.digest}`}
                status={
                  answer.answer === 'granted'
                    ? { tone: 'ok' as const, text: 'Allowed' }
                    : { tone: 'neutral' as const, text: 'Denied' }
                }
                actions={
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy() !== ''}
                    ariaLabel={`Forget the answer for ${answer.executable}`}
                    onClick={() => void forget(answer)}
                  >
                    {busy() === key(answer) ? 'Forgetting…' : 'Forget'}
                  </Button>
                }
              />
            </div>
          )}
        </For>
      </Show>
    </PageSection>
  )
}

/** A row's identity: the same three facts the answer is keyed by. */
function key(answer: AgentAccessAnswer): string {
  return `${answer.executable}|${answer.digest}|${answer.workspace}`
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}
