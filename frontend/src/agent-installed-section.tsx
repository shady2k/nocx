/**
 * WHICH OF THE AGENTS THE ASSISTANT CAN RUN ARE ON THIS COMPUTER
 * (nocx-a62cl.12.2, ADR-anntnxdj decision 6).
 *
 * The backend answers one question — `agentRecords.detect` — and this page is
 * the surface for it: five rows, each found or not on the person's login
 * shell's PATH, the version the command printed, and the readiness the
 * agent's own probe reported. Nothing here starts an agent, and nothing here
 * changes how the assistant answers: eino still answers until 4b, and this
 * page is the list 4b's entry will read.
 *
 * A PULL, and the request is the whole of the interval. The page reads when it
 * opens and when the person presses Refresh, and asks nothing while it is not
 * open. Refresh asks with `refresh: true` because the backend caches one
 * detection pass: a plain second read would redraw the answer already on
 * screen, which is what pressing the control was meant to escape.
 *
 * THE ROWS ARE THE BACKEND'S, INCLUDING THEIR NAMES. The record of the five
 * agents is `internal/agentrecord/detect.go`, and the frontend iterates the
 * answer rather than knowing an id, a display name or a command. That is the
 * stage's constraint stated as a surface: nothing names an agent outside the
 * detection's own data record. It is also why this page cannot go stale by
 * itself when a sixth agent is added.
 *
 * NOTHING IS CLAIMED FROM A READ THAT FAILED. A detection pass that could not
 * run — a login shell that exited, a socket that refused — is an error on the
 * page beside the rows already there, never an empty list: "nothing is
 * installed" and "nocx could not look" are different facts, and drawing the
 * first over the second is the silent degrade AGENTS.md names.
 */
import { For, Show, createSignal, onMount } from 'solid-js'
import type { Agent } from './generated/agentRecords.detect'
import type { AgentRecordsClient } from './agent-records-client'
import { Button } from './ui/button'
import { PageSection } from './ui/page-section'
import { RecordRow } from './ui/record-row'
import { StatusCard } from './ui/status-card'
import type { StatusDotTone } from './ui/status-dot'
import { t } from './i18n/locale-model'

export interface AgentInstalledSectionProps {
  client: AgentRecordsClient
}

/**
 * One readiness state, in the kit's dot vocabulary and the product's words.
 *
 * The switch is exhaustive over the contract's closed set on purpose: a state
 * added to the wire without a branch here fails the type check rather than
 * landing in whichever branch was written last, which is the whole reason the
 * backend declares a closed set (`agentrecord.Readiness`).
 */
function readinessStatus(readiness: Agent['readiness']): {
  tone: StatusDotTone
  text: string
} {
  switch (readiness) {
    case 'not_installed':
      return { tone: 'neutral', text: t('agents.installed.notInstalled', 'Not installed') }
    case 'installed':
      return { tone: 'neutral', text: t('agents.installed.installed', 'Installed') }
    case 'ready':
      return { tone: 'ok', text: t('agents.installed.ready', 'Ready') }
    case 'not_ready':
      return { tone: 'warning', text: t('agents.installed.notReady', 'Not ready') }
    case 'invalid':
      return { tone: 'error', text: t('agents.installed.invalid', 'Not usable') }
    case 'unknown':
      return { tone: 'warning', text: t('agents.installed.unknown', 'Could not tell') }
  }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export function AgentInstalledSection(props: AgentInstalledSectionProps) {
  const [agents, setAgents] = createSignal<readonly Agent[]>([])
  /** True until the first answer arrives — the placeholder, never a claim. */
  const [loading, setLoading] = createSignal(true)
  /** True while a read is in flight, so Refresh cannot stack passes. */
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal('')

  const read = async (refresh: boolean): Promise<void> => {
    setBusy(true)
    setError('')
    try {
      setAgents((await props.client.detect(refresh)).agents)
    } catch (err) {
      setError(
        `${t('agents.installed.failedHead', 'Could not look for the agents:')} ${message(err)}`,
      )
    } finally {
      setLoading(false)
      setBusy(false)
    }
  }

  onMount(() => {
    void read(false)
  })

  return (
    <PageSection
      title={t('agents.installed.title', 'Agents on this computer')}
      description={t(
        'agents.installed.desc',
        "Which of the agents nocx can run are on your login shell's PATH, the version each reports, and whether it says it can answer. Nothing here starts an agent or changes how the assistant answers.",
      )}
    >
      <div>
        <Button disabled={busy()} onClick={() => void read(true)}>
          {t('agents.installed.refresh', 'Refresh')}
        </Button>
      </div>
      <div role="status" aria-live="polite" aria-atomic="true">
        <Show when={!error()}>
          <StatusCard
            title={
              loading()
                ? t(
                    'agents.installed.loading',
                    'Looking for the agents on your login shell’s PATH…',
                  )
                : busy()
                  ? t(
                      'agents.installed.refreshing',
                      'Refreshing… Shown agents are from the previous result.',
                    )
                  : t('agents.installed.complete', 'Agent check complete.')
            }
          />
        </Show>
      </div>
      <Show when={error()}>
        <p role="alert">{error()}</p>
      </Show>
      <Show when={agents().length > 0}>
        <div
          role="list"
          aria-label={t('agents.installed.listAria', 'Agents on this computer')}
          aria-busy={busy()}
        >
          <For each={agents()}>
            {(agent) => (
              // The wrapper carries the row's identity for a test and for a
              // person reading the DOM; the appearance stays the kit's.
              <div data-agent={agent.id}>
                <RecordRow
                  title={agent.displayName}
                  meta={agent.version === '' ? undefined : agent.version}
                  status={readinessStatus(agent.readiness)}
                  actions={null}
                />
              </div>
            )}
          </For>
        </div>
      </Show>
    </PageSection>
  )
}
