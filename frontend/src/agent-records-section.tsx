import { For, Show, createSignal, onMount, untrack } from 'solid-js'
import type { Agent } from './generated/agentRecords.list'
import type { AgentRecordDraft, AgentRecordsClient } from './agent-records-client'
import { Button, Checkbox, PageSection, Section, Stack, TextField } from './ui'

export interface AgentRecordsSectionProps {
  client: AgentRecordsClient
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'The agent record could not be saved'
}

function lines(value: string): string[] {
  return value.split('\n').filter((line) => line !== '')
}

function draftFromAgent(agent: Agent): AgentRecordDraft {
  return {
    id: agent.id,
    displayName: agent.displayName,
    command: agent.command,
    args: agent.args,
    icon: agent.icon,
    colour: agent.colour,
    disabled: agent.disabled,
    env: agent.env,
    resume: agent.resume,
  }
}

function AgentRecordCard(props: {
  agent: Agent
  client: AgentRecordsClient
  saved: boolean
  onSaving: (id: string) => void
  onSaved: (agent: Agent) => void
  onRemoved: (id: string) => void
}) {
  const [displayName, setDisplayName] = createSignal(untrack(() => props.agent.displayName))
  const [command, setCommand] = createSignal(untrack(() => props.agent.command))
  const [args, setArgs] = createSignal(untrack(() => props.agent.args.join('\n')))
  const [env, setEnv] = createSignal(untrack(() => props.agent.env.join('\n')))
  const [disabled, setDisabled] = createSignal(untrack(() => props.agent.disabled))
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal('')

  const changeOffering = async (enabled: boolean): Promise<void> => {
    setBusy(true)
    setError('')
    props.onSaving(props.agent.id)
    try {
      const result = await props.client.save({ ...draftFromAgent(props.agent), disabled: !enabled })
      setDisabled(result.agent.disabled)
      props.onSaved(result.agent)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const save = async (): Promise<void> => {
    setBusy(true)
    setError('')
    props.onSaving(props.agent.id)
    try {
      const result = await props.client.save({
        ...draftFromAgent(props.agent),
        displayName: displayName(),
        command: command(),
        args: lines(args()),
        env: lines(env()),
        disabled: disabled(),
      })
      props.onSaved(result.agent)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (): Promise<void> => {
    setBusy(true)
    setError('')
    try {
      await props.client.remove(props.agent.id)
      props.onRemoved(props.agent.id)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section title={props.agent.displayName || props.agent.id}>
      <Stack gap="loose">
        <p>
          Agent ID: {props.agent.id}
          {props.agent.builtin ? ' · Built in' : ''}
        </p>
        <TextField label="Display name" value={displayName()} onInput={setDisplayName} />
        <TextField label="Command" value={command()} onInput={setCommand} required />
        <TextField
          label="Arguments (one per line)"
          value={args()}
          onInput={setArgs}
          multiline
          rows={4}
        />
        <TextField
          label="Environment (KEY=VALUE, one per line)"
          value={env()}
          onInput={setEnv}
          multiline
          rows={4}
        />
        <Checkbox
          label="Offer this agent in new shells"
          variant="switch"
          checked={props.agent.state !== 'unreadable' && !disabled()}
          disabled={busy() || props.agent.state === 'unreadable'}
          onChange={changeOffering}
        />
        <Show when={props.agent.state === 'unreadable'}>
          <p role="alert">This record could not be read: {props.agent.problem}</p>
        </Show>
        <Show when={props.saved}>
          <p role="status">Saved. The next launch will use these settings.</p>
        </Show>
        <Show when={error()}>
          <p role="alert">{error()}</p>
        </Show>
        <div>
          <Button variant="primary" disabled={busy()} onClick={() => void save()}>
            Save agent
          </Button>
          <Show when={!props.agent.builtin}>
            <Button variant="danger" disabled={busy()} onClick={() => void remove()}>
              Remove agent
            </Button>
          </Show>
        </div>
      </Stack>
    </Section>
  )
}

export function AgentRecordsSection(props: AgentRecordsSectionProps) {
  const [agents, setAgents] = createSignal<Agent[]>([])
  const [savedAgentIDs, setSavedAgentIDs] = createSignal<ReadonlySet<string>>(new Set())
  const [loading, setLoading] = createSignal(true)
  const [loadError, setLoadError] = createSignal('')
  const [newID, setNewID] = createSignal('')
  const [newName, setNewName] = createSignal('')
  const [newCommand, setNewCommand] = createSignal('')
  const [newArgs, setNewArgs] = createSignal('')
  const [newEnv, setNewEnv] = createSignal('')
  const [creating, setCreating] = createSignal(false)
  const [createError, setCreateError] = createSignal('')
  const [createSaved, setCreateSaved] = createSignal(false)

  const refresh = async (): Promise<void> => {
    setLoading(true)
    setLoadError('')
    try {
      setAgents((await props.client.list()).agents)
    } catch (err) {
      setLoadError(`Could not read agent records: ${errorMessage(err)}`)
    } finally {
      setLoading(false)
    }
  }

  const markAgentSaving = (id: string): void => {
    setSavedAgentIDs((current) => {
      const next = new Set(current)
      next.delete(id)
      return next
    })
  }

  const markAgentSaved = (saved: Agent): void => {
    setSavedAgentIDs((current) => new Set(current).add(saved.id))
    setAgents((current) => current.map((item) => (item.id === saved.id ? saved : item)))
  }

  const removeAgent = (id: string): void => {
    setAgents((current) => current.filter((item) => item.id !== id))
    setSavedAgentIDs((current) => {
      const next = new Set(current)
      next.delete(id)
      return next
    })
  }

  onMount(() => {
    void refresh()
  })

  const saveNew = async (): Promise<void> => {
    setCreating(true)
    setCreateError('')
    setCreateSaved(false)
    try {
      const result = await props.client.save({
        id: newID(),
        displayName: newName(),
        command: newCommand(),
        args: lines(newArgs()),
        icon: '',
        colour: '',
        disabled: false,
        env: lines(newEnv()),
        resume: { sessionIdArgs: [], resumeIdArgs: [], resumeCwdArgs: [] },
      })
      setSavedAgentIDs((current) => {
        const next = new Set(current)
        next.delete(result.agent.id)
        return next
      })
      setAgents((current) =>
        [...current.filter((agent) => agent.id !== result.agent.id), result.agent].sort((a, b) =>
          a.id.localeCompare(b.id),
        ),
      )
      setNewID('')
      setNewName('')
      setNewCommand('')
      setNewArgs('')
      setNewEnv('')
      setCreateSaved(true)
    } catch (err) {
      setCreateError(errorMessage(err))
    } finally {
      setCreating(false)
    }
  }

  return (
    <PageSection
      title="Agents"
      description="Edit the commands nocx offers in new shells. Changes apply to the next agent launch; built-in agents can be edited but not removed."
    >
      <Stack gap="loose">
        <Show when={loading()}>
          <p role="status">Loading agent records…</p>
        </Show>
        <Show when={loadError()}>
          <p role="alert">{loadError()}</p>
        </Show>
        <For each={agents()}>
          {(agent) => (
            <AgentRecordCard
              agent={agent}
              client={props.client}
              saved={savedAgentIDs().has(agent.id)}
              onSaving={markAgentSaving}
              onSaved={markAgentSaved}
              onRemoved={removeAgent}
            />
          )}
        </For>
        <Section title="Add an agent">
          <Stack gap="loose">
            <TextField label="Agent ID" value={newID()} onInput={setNewID} required />
            <TextField label="Display name" value={newName()} onInput={setNewName} />
            <TextField label="Command" value={newCommand()} onInput={setNewCommand} required />
            <TextField
              label="Arguments (one per line)"
              value={newArgs()}
              onInput={setNewArgs}
              multiline
              rows={4}
            />
            <TextField
              label="Environment (KEY=VALUE, one per line)"
              value={newEnv()}
              onInput={setNewEnv}
              multiline
              rows={4}
            />
            <Show when={createSaved()}>
              <p role="status">Agent added. It is available in new shells.</p>
            </Show>
            <Show when={createError()}>
              <p role="alert">{createError()}</p>
            </Show>
            <Button variant="primary" disabled={creating()} onClick={() => void saveNew()}>
              Add agent
            </Button>
          </Stack>
        </Section>
      </Stack>
    </PageSection>
  )
}
