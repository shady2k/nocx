/** Per-agent model choices. Unassigned roles use the agent's own default. */
import { For, Show, createSignal, onMount } from 'solid-js'
import { Select, Stack, Button, Field } from './ui'
import { t } from './i18n/locale-model'
import type { EndpointClient, WireRole } from './endpoints'

export interface RolesSectionProps {
  client: EndpointClient
  agentId: string
  models: readonly { id: string; name: string }[]
}
function roleName(role: string): string {
  switch (role) {
    case 'answering':
      return t('roles.answering.name', 'Answering')
    case 'classifier':
      return t('roles.classifier.name', 'Classifier')
    case 'summarizing':
      return t('roles.summarizing.name', 'Summarizing')
    case 'auditing':
      return t('roles.auditing.name', 'Auditing')
    default:
      return role
  }
}
export function RolesSection(props: RolesSectionProps) {
  const [roles, setRoles] = createSignal<WireRole[]>([])
  const [error, setError] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const load = async () => {
    setBusy(true)
    setError('')
    try {
      setRoles((await props.client.listRoles(props.agentId)).roles)
    } catch (err) {
      setError(String(err))
    } finally {
      setBusy(false)
    }
  }
  onMount(() => {
    void load()
  })
  const commit = async (role: string, model: string) => {
    setBusy(true)
    setError('')
    try {
      setRoles(
        (await props.client.assignRole({ agentId: props.agentId, role, model: model || null }))
          .roles,
      )
    } catch (err) {
      setError(String(err))
      setRoles((current) => current.map((row) => ({ ...row })))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Stack>
      <p>
        {t(
          'roles.agentDefault.desc',
          'Unassigned roles use this agent’s default. A conversation keeps the model from its first turn; changes apply to new conversations.',
        )}
      </p>
      <Show when={props.models.length === 0}>
        <p>
          {t(
            'roles.noCatalogue',
            'This agent does not offer model choices. It uses its own default.',
          )}
        </p>
      </Show>
      <Show when={error()}>
        <p role="alert">
          {t('roles.failed', 'Could not read or save model roles:')} {error()}{' '}
          <Button disabled={busy()} onClick={() => void load()}>
            {t('roles.retry', 'Retry')}
          </Button>
        </p>
      </Show>
      <For each={roles()}>
        {(row) => {
          const removed = () =>
            row.model !== null && !props.models.some((model) => model.id === row.model)
          const options = () => [
            { value: '', label: t('roles.agentDefault', 'Agent default') },
            ...props.models.map((model) => ({
              value: model.id,
              label: `${model.name} (${model.id})`,
            })),
            ...(removed() ? [{ value: row.model!, label: row.model!, disabled: true }] : []),
          ]
          return (
            <div data-role={row.role}>
              <Field for={`role-${props.agentId}-${row.role}`} label={roleName(row.role)}>
                <Select
                  id={`role-${props.agentId}-${row.role}`}
                  value={row.model ?? ''}
                  options={options()}
                  disabled={busy() || row.role !== 'answering'}
                  onChange={(model) => void commit(row.role, model)}
                />
              </Field>
              <Show when={row.role !== 'answering'}>
                <p>
                  {t('roles.unused', 'Declared for a future feature; not used in this version.')}
                </p>
              </Show>
              <Show when={removed()}>
                <p role="alert">
                  {t('roles.removed', 'The chosen model is no longer listed by this agent:')}{' '}
                  {row.model}
                </p>
              </Show>
            </div>
          )
        }}
      </For>
    </Stack>
  )
}
