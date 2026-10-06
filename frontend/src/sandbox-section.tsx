import { For, Show, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import type { SandboxSettingsServices, SandboxPaneContext } from './sandbox-ui'
import type { SandboxProfileResult } from './generated/sandbox.profile.get'
import type { SandboxPreviewResult } from './generated/sandbox.preview'
import type { SandboxOperationResult } from './generated/sandbox.replace'
import type { SandboxStatusResult } from './generated/sandbox.status'
import type { SandboxGrantResult } from './generated/sandbox.grant.get'
import { Button, Checkbox, EditableRowList, PageSection, Select, StatusCard, TextField } from './ui'
import { Dialog } from './ui/dialog'
import { showToast } from './ui/toast'
import { SandboxDiagnosticsSection } from './sandbox-diagnostics-section'

export interface SandboxSectionProps {
  services?: SandboxSettingsServices
  context: SandboxPaneContext | null
}

export function SandboxSection(props: SandboxSectionProps) {
  const initialContext = untrack(() => props.context)
  const [profile, setProfile] = createSignal<SandboxProfileResult | null>(null)
  const [pinnedProfile, setPinnedProfile] = createSignal<SandboxProfileResult | null>(null)
  const [status, setStatus] = createSignal<SandboxStatusResult | null>(null)
  const [grant, setGrant] = createSignal<SandboxGrantResult | null>(null)
  const [selectedWorkspace, setSelectedWorkspace] = createSignal<string | null>(
    untrack(() =>
      initialContext?.workspaceId &&
      props.services?.workspaces().some((workspace) => workspace.id === initialContext.workspaceId)
        ? initialContext.workspaceId
        : null,
    ),
  )
  const [roRoots, setRoRoots] = createSignal<string[]>([])
  const [rwRoots, setRwRoots] = createSignal<string[]>([])
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal('')
  const [preview, setPreview] = createSignal<SandboxPreviewResult | null>(null)
  const [recoverOperation, setRecoverOperation] = createSignal('')
  const [operationInFlight, setOperationInFlight] = createSignal<SandboxPreviewResult | null>(null)
  const [cancelledOperation, setCancelledOperation] = createSignal('')
  const [canceling, setCanceling] = createSignal(false)
  const [disposed, setDisposed] = createSignal(false)
  let profileRead = 0
  let pinnedProfileRead = 0
  let statusRead = 0
  onCleanup(() => setDisposed(true))

  const context = () => props.context
  const service = () => props.services
  const readProfile = async () => {
    const s = service()
    if (!s) return
    const request = ++profileRead
    try {
      const workspaceId = selectedWorkspace()
      const result = await s.client.sandboxProfile(workspaceId ? { workspaceId } : {})
      if (disposed() || request !== profileRead) return
      setProfile(result)
      setRoRoots(result.effective.readOnlyDirs)
      setRwRoots(result.effective.readWriteDirs)
      setError('')
    } catch (e) {
      if (!disposed() && request === profileRead) setError(message(e))
    }
  }
  const readStatus = async () => {
    const s = service()
    const c = context()
    const request = ++statusRead
    if (!s || !c) {
      setStatus(null)
      setGrant(null)
      return
    }
    setStatus(null)
    setGrant(null)
    try {
      const registered = await c.registered
      if (!registered) {
        if (!disposed() && request === statusRead)
          setError('Эта панель ещё не зарегистрирована в backend.')
        return
      }
      const result = await s.client.sandboxStatus({ paneId: c.paneId })
      if (disposed() || request !== statusRead || !c.isCurrent()) return
      setStatus(result)
      if (result.head) {
        const currentGrant = await s.client.sandboxGrant({ launchId: result.head.launchId })
        if (!disposed() && request === statusRead && c.isCurrent()) setGrant(currentGrant)
      }
      if (result.preparingOperationId) setRecoverOperation(result.preparingOperationId)
    } catch (e) {
      if (!disposed() && request === statusRead) setError(message(e))
    }
  }
  const readPinnedProfile = async () => {
    const s = service()
    const c = context()
    const request = ++pinnedProfileRead
    if (!s || !c) {
      setPinnedProfile(null)
      return
    }
    try {
      const defaultWorkspaceId = s.defaultWorkspaceId()
      if (!defaultWorkspaceId) {
        setPinnedProfile(null)
        return
      }
      const result = await s.client.sandboxProfile({
        workspaceId: c.workspaceId === defaultWorkspaceId ? '' : c.workspaceId,
      })
      if (!disposed() && request === pinnedProfileRead && context()?.workspaceId === c.workspaceId)
        setPinnedProfile(result)
    } catch (e) {
      if (!disposed() && request === pinnedProfileRead) setError(message(e))
    }
  }
  createEffect(() => {
    void context()?.paneId
    void readStatus()
  })
  createEffect(() => {
    selectedWorkspace()
    setProfile(null)
    void readProfile()
  })
  createEffect(() => {
    void context()?.workspaceId
    service()?.workspaces()
    service()?.defaultWorkspaceId()
    void readPinnedProfile()
  })
  const saveProfile = async () => {
    const s = service()
    const p = profile()
    if (!s || !p || busy()) return
    setBusy(true)
    setError('')
    try {
      const workspaceId = selectedWorkspace()
      const updated = await s.client.sandboxUpdateProfile({
        ...(workspaceId ? { workspaceId } : {}),
        expectedRevision: workspaceId ? (p.workspace?.revision ?? 0) : p.standard.revision,
        ...(workspaceId ? {} : { enabled: p.standard.enabled }),
        roots: {
          readOnlyDirs: roRoots()
            .map((value) => value.trim())
            .filter(Boolean),
          readWriteDirs: rwRoots()
            .map((value) => value.trim())
            .filter(Boolean),
        },
      })
      if (disposed()) return
      setProfile(updated)
      setRoRoots(updated.effective.readOnlyDirs)
      setRwRoots(updated.effective.readWriteDirs)
      s.statusChanged()
      void readPinnedProfile()
      showToast({ message: 'Профиль песочницы сохранён' })
    } catch (e) {
      if (!disposed()) setError(message(e))
    } finally {
      if (!disposed()) setBusy(false)
    }
  }
  const toggleEnabled = async (enabled: boolean) => {
    const s = service()
    const p = profile()
    if (!s || !p || busy()) return
    setBusy(true)
    setError('')
    try {
      const updated = await s.client.sandboxUpdateProfile({
        expectedRevision: p.standard.revision,
        enabled,
        roots: { readOnlyDirs: p.standard.readOnlyDirs, readWriteDirs: p.standard.readWriteDirs },
      })
      if (!disposed()) {
        setProfile({ ...p, standard: updated.standard })
        s.statusChanged()
        void readPinnedProfile()
        void readStatus()
      }
    } catch (e) {
      if (!disposed()) setError(message(e))
    } finally {
      if (!disposed()) setBusy(false)
    }
  }
  const resetWorkspace = async () => {
    const s = service()
    const p = profile()
    const workspaceId = selectedWorkspace()
    if (!s || !p || !workspaceId || busy()) return
    setBusy(true)
    setError('')
    try {
      const updated = await s.client.sandboxResetProfile({
        workspaceId,
        expectedRevision: p.workspace?.revision ?? 0,
      })
      if (!disposed()) {
        setProfile(updated)
        setRoRoots(updated.effective.readOnlyDirs)
        setRwRoots(updated.effective.readWriteDirs)
        s.statusChanged()
        void readPinnedProfile()
      }
    } catch (e) {
      if (!disposed()) setError(message(e))
    } finally {
      if (!disposed()) setBusy(false)
    }
  }
  const makePreview = async (mode: 'enforce' | 'off') => {
    const s = service()
    const c = context()
    const current = status()
    if (!s || !c || !current || busy() || !c.isCurrent()) return
    setBusy(true)
    setError('')
    try {
      if (!(await c.registered) || !c.isCurrent())
        throw new Error('Эта панель недоступна для операции песочницы.')
      const result = await s.client.sandboxPreview({
        paneId: c.paneId,
        source: current.source,
        expectedHeadId: current.head?.launchId ?? '',
        mode,
        delta: { readOnlyDirs: [], readWriteDirs: [] },
      })
      if (disposed() || !c.isCurrent()) {
        await s.client.sandboxCancel({
          operationId: result.operationId,
          confirmationId: result.confirmationId,
        })
        return
      }
      setPreview(result)
    } catch (e) {
      if (!disposed()) setError(message(e))
    } finally {
      if (!disposed()) setBusy(false)
    }
  }
  const cancelPreview = async () => {
    const s = service()
    const active = operationInFlight()
    const p = active ?? preview()
    if (!s || !p || canceling()) return
    setCanceling(true)
    setBusy(true)
    try {
      await s.client.sandboxCancel({ operationId: p.operationId, confirmationId: p.confirmationId })
      if (disposed()) return
      if (active) {
        setCancelledOperation(active.operationId)
        setOperationInFlight(null)
        setRecoverOperation('')
      }
      setPreview(null)
      s.statusChanged()
      if (context()) void readStatus()
    } catch (e) {
      if (!disposed()) {
        setPreview(null)
        setOperationInFlight(null)
        setRecoverOperation(p.operationId)
        setError(message(e))
        s.statusChanged()
        void readStatus()
      }
    } finally {
      if (!disposed()) {
        setCanceling(false)
        setBusy(false)
      }
    }
  }
  const confirmPreview = async () => {
    const s = service()
    const c = context()
    const p = preview()
    if (!s || !c || !p || busy() || !c.isCurrent()) return
    setBusy(true)
    setError('')
    setOperationInFlight(p)
    try {
      let result: SandboxOperationResult
      try {
        result = await s.client.sandboxReplace({
          operationId: p.operationId,
          confirmationId: p.confirmationId,
        })
      } catch {
        setRecoverOperation(p.operationId)
        result = await s.client.sandboxOperation({ operationId: p.operationId })
      }
      if (disposed()) return
      setPreview(null)
      if (result.state !== 'preparing') setOperationInFlight(null)
      if (cancelledOperation() === result.operationId && result.state !== 'active') {
        setRecoverOperation('')
        setOperationInFlight(null)
        void readStatus()
      } else if (result.state === 'active') {
        setRecoverOperation('')
        if (!c.isCurrent()) setError('Целевая вкладка изменилась. Новый процесс не привязан.')
        else {
          const bound = await s.bindCandidate(c, result)
          if (!bound) setError('Целевая вкладка изменилась. Новый процесс не привязан.')
        }
      } else if (result.state === 'failed') {
        setRecoverOperation('')
        setError(result.reason || 'Операция завершилась ошибкой')
      } else if (result.state === 'preparing') setRecoverOperation(result.operationId)
      else {
        setRecoverOperation('')
        void readStatus()
      }
      s.statusChanged()
      await readStatus()
    } catch (e) {
      if (!disposed()) setError(message(e))
    } finally {
      if (!disposed()) setBusy(false)
    }
  }
  const recover = async () => {
    const s = service()
    const c = context()
    const id = recoverOperation()
    if (!s || !c || !id || busy()) return
    setBusy(true)
    try {
      if (!(await c.registered) || !c.isCurrent())
        throw new Error('Эта панель недоступна для операции песочницы.')
      const operation = await s.client.sandboxOperation({ operationId: id })
      if (disposed()) return
      if (operation.state === 'active') {
        if (!c.isCurrent()) setError('Целевая вкладка изменилась. Новый процесс не привязан.')
        else {
          const bound = await s.bindCandidate(c, operation)
          if (!bound) setError('Целевая вкладка изменилась. Новый процесс не привязан.')
        }
      } else if (operation.state === 'failed')
        setError(operation.reason || 'Операция завершилась ошибкой')
      if (operation.state !== 'preparing') {
        setRecoverOperation('')
        setOperationInFlight(null)
      }
      await readStatus()
    } catch (e) {
      if (!disposed()) setError(message(e))
    } finally {
      if (!disposed()) setBusy(false)
    }
  }
  const policy = () => preview()?.policy ?? operationInFlight()?.policy
  const confirmation = () => preview() ?? operationInFlight()
  const targetIsCurrent = () => !!context()?.isCurrent()
  const applyDiagnosticProfile = (updated: SandboxProfileResult) => {
    setPinnedProfile(updated)
    const editorWorkspace = selectedWorkspace()
    const boundWorkspace = updated.workspace?.workspaceId
    const updatedWorkspace =
      boundWorkspace && boundWorkspace !== service()?.defaultWorkspaceId() ? boundWorkspace : null
    if (editorWorkspace !== updatedWorkspace) return
    const current = profile()
    if (!current) return
    const draftMatches =
      roRoots().length === current.effective.readOnlyDirs.length &&
      roRoots().every((root, index) => root === current.effective.readOnlyDirs[index]) &&
      rwRoots().length === current.effective.readWriteDirs.length &&
      rwRoots().every((root, index) => root === current.effective.readWriteDirs[index])
    setProfile(updated)
    if (draftMatches) {
      setRoRoots(updated.effective.readOnlyDirs)
      setRwRoots(updated.effective.readWriteDirs)
    }
  }

  return (
    <PageSection title="Песочница">
      <Show
        when={service()}
        fallback={
          <StatusCard
            tone="warning"
            title="Песочница недоступна"
            description="В этом окне нет подключения к службе песочницы."
          />
        }
      >
        <Show
          when={profile()}
          fallback={
            <Show
              when={error()}
              fallback={
                <StatusCard
                  title="Загрузка профиля песочницы"
                  description="Читаем актуальную конфигурацию из службы."
                />
              }
            >
              <StatusCard
                tone="danger"
                title="Не удалось загрузить профиль"
                description={error()}
              />
            </Show>
          }
        >
          {(p) => (
            <>
              <Show
                when={status()}
                fallback={
                  <StatusCard
                    tone="neutral"
                    title="Доступность нативного механизма не проверена"
                    description={
                      context()
                        ? 'Ожидаем актуальное состояние backend для закреплённой панели.'
                        : 'К этой странице не привязан терминал; выберите панель, чтобы проверить поддержку и причину отказа.'
                    }
                  />
                }
              >
                {(current) => (
                  <StatusCard
                    tone={current().availability === 'available' ? 'ok' : 'warning'}
                    title={
                      current().availability === 'available'
                        ? 'Нативная песочница доступна'
                        : 'Нативная песочница недоступна'
                    }
                    description={
                      current().reason ||
                      'Доступность механизма не меняет права текущего запуска. Состояние наблюдателя показано в журнале диагностики.'
                    }
                  />
                )}
              </Show>
              <Show when={status()}>
                {(current) => (
                  <For
                    each={
                      current().paneId === context()?.paneId &&
                      context()?.kind === 'local' &&
                      current().head?.mode === 'enforce' &&
                      current().head?.state === 'active' &&
                      current().head?.launchId
                        ? [current().head!.launchId]
                        : []
                    }
                  >
                    {(launchId) => (
                      <SandboxDiagnosticsSection
                        services={service()!}
                        context={context()!}
                        launchId={launchId}
                        onProfileResolved={applyDiagnosticProfile}
                      />
                    )}
                  </For>
                )}
              </Show>
              <Checkbox
                variant="switch"
                label="Включить песочницу для новых запусков"
                checked={p().standard.enabled}
                disabled={busy()}
                onChange={toggleEnabled}
              />
              <label>
                Профиль
                <Select
                  disabled={busy()}
                  ariaLabel="Профиль песочницы"
                  value={selectedWorkspace() ?? ''}
                  options={[
                    { value: '', label: 'Стандартный' },
                    ...(service()
                      ?.workspaces()
                      .map((workspace) => ({ value: workspace.id, label: workspace.name })) ?? []),
                  ]}
                  onChange={(value) => setSelectedWorkspace(value || null)}
                />
              </label>
              <p>
                Источник: {p().profileSource} · стандартная ревизия {p().standard.revision}
                <Show when={p().workspace}>
                  {(workspace) => <span> · ревизия рабочей области {workspace().revision}</span>}
                </Show>
              </p>
              <EditableRowList
                ariaLabel="Корни только для чтения"
                rows={roRoots()}
                addLabel="Добавить корень"
                emptyLabel="Корни только для чтения не заданы."
                disabled={busy()}
                onAdd={() => setRoRoots([...roRoots(), ''])}
                onRemove={(index) => setRoRoots(roRoots().filter((_, i) => i !== index))}
                renderRow={(row, index) => (
                  <TextField
                    ariaLabel={`Корень только для чтения ${index + 1}`}
                    value={row()}
                    disabled={busy()}
                    onInput={(value) =>
                      setRoRoots(roRoots().map((path, i) => (i === index ? value : path)))
                    }
                  />
                )}
              />
              <EditableRowList
                ariaLabel="Корни для чтения и записи"
                rows={rwRoots()}
                addLabel="Добавить корень"
                emptyLabel="Корни для чтения и записи не заданы."
                disabled={busy()}
                onAdd={() => setRwRoots([...rwRoots(), ''])}
                onRemove={(index) => setRwRoots(rwRoots().filter((_, i) => i !== index))}
                renderRow={(row, index) => (
                  <TextField
                    ariaLabel={`Корень для чтения и записи ${index + 1}`}
                    value={row()}
                    disabled={busy()}
                    onInput={(value) =>
                      setRwRoots(rwRoots().map((path, i) => (i === index ? value : path)))
                    }
                  />
                )}
              />
              <Button disabled={busy()} onClick={() => void saveProfile()}>
                Сохранить профиль
              </Button>
              <Show when={selectedWorkspace()}>
                <Button disabled={busy()} onClick={() => void resetWorkspace()}>
                  Сбросить к стандартному
                </Button>
              </Show>
            </>
          )}
        </Show>
        <Show when={context()}>
          <PageSection title="Текущая панель">
            <Show when={!targetIsCurrent()}>
              <StatusCard
                tone="warning"
                title="Целевая панель изменилась"
                description="Панель закрыта или перемещена. Откройте песочницу из нужного терминала; эта страница не переключает цель автоматически."
              />
            </Show>
            <StatusCard
              title={
                status()?.head
                  ? `Режим ${status()!.head!.mode.toUpperCase()}`
                  : 'Нет закреплённого запуска'
              }
              description={
                status()?.head
                  ? `Панель ${context()!.paneId} · рабочая область ${context()!.workspaceId} · ${context()!.kind} · запуск ${status()!.head!.launchId} · состояние ${status()!.head!.state} · enforcement ${status()!.head!.enforcement} · наблюдатель ${status()!.head!.observer}`
                  : `Панель ${context()!.paneId} · рабочая область ${context()!.workspaceId} · ${context()!.kind} · нет закреплённого запуска.`
              }
            />
            <Show when={status()?.source}>
              {(source) => (
                <p>
                  Исходная оболочка: {source().sessionId} · incarnation {source().instanceId} ·
                  epoch {source().sessionEpoch}
                </p>
              )}
            </Show>
            <p>
              Закреплённое разрешение: {grant()?.grantId ?? 'отсутствует'} · режим{' '}
              {grant()?.mode ?? '—'} · неизменяемый digest {grant()?.policyDigest ?? '—'}.
            </p>
            <Show when={grant()?.policy}>
              {(policy) => (
                <>
                  <p>
                    Ревизии закреплённого запуска: {policy().standardRevision}/
                    {policy().workspaceRevision}
                  </p>
                  <For each={policy().roots}>
                    {(root) => (
                      <p>
                        {root.access.toUpperCase()} · {root.path} · {root.provenance}
                      </p>
                    )}
                  </For>
                </>
              )}
            </Show>
            <Show
              when={
                grant()?.policy &&
                pinnedProfile() &&
                (grant()!.policy!.standardRevision !== pinnedProfile()!.standard.revision ||
                  grant()!.policy!.workspaceRevision !==
                    (pinnedProfile()!.workspace?.revision ?? 0))
              }
            >
              <StatusCard
                tone="warning"
                title="Профиль по умолчанию изменился"
                description="Текущий запуск сохраняет своё выданное разрешение. Новые значения не меняют его; подтверждённый перезапуск выдаст новую политику."
              />
            </Show>
            <Show
              when={status()?.head?.mode === 'enforce' && status()?.availability === 'available'}
            >
              <Button
                disabled={busy() || !targetIsCurrent()}
                onClick={() => void makePreview('off')}
              >
                Удалить ограничение
              </Button>
            </Show>
            <Show when={status()}>
              <Button
                disabled={
                  busy() ||
                  !status()?.enabled ||
                  status()?.availability !== 'available' ||
                  !targetIsCurrent()
                }
                onClick={() => void makePreview('enforce')}
              >
                {status()?.head?.mode === 'enforce'
                  ? 'Применить / перезапустить'
                  : 'Применить песочницу'}
              </Button>
            </Show>
          </PageSection>
          <Show when={recoverOperation()}>
            <Button disabled={busy() || !targetIsCurrent()} onClick={() => void recover()}>
              Проверить состояние операции
            </Button>
          </Show>
        </Show>
        <Show when={error()}>
          <StatusCard tone="danger" title="Не удалось выполнить действие" description={error()} />
        </Show>
        <Dialog
          open={!!confirmation()}
          title={
            confirmation()?.mode === 'off' ? 'Удалить ограничение?' : 'Перезапустить с песочницей?'
          }
          onClose={() => void cancelPreview()}
          footer={
            <>
              <Button disabled={canceling()} onClick={() => void cancelPreview()}>
                {operationInFlight() ? 'Отменить операцию' : 'Отмена'}
              </Button>
              <Show when={preview()}>
                <Button
                  disabled={
                    busy() ||
                    !targetIsCurrent() ||
                    (preview()?.mode === 'enforce' && !preview()?.policy)
                  }
                  onClick={() => void confirmPreview()}
                >
                  Подтвердить
                </Button>
              </Show>
            </>
          }
        >
          <p>
            Текущая оболочка будет закрыта. Уже отделившиеся фоновые процессы не будут
            ретроспективно ограничены или завершены.
            {confirmation()?.mode === 'off'
              ? ' Новый процесс будет запущен без песочницы.'
              : ' Новая оболочка получит показанную ниже эффективную политику.'}
          </p>
          <p>Подтверждение действительно до: {confirmation()?.expiresAt}</p>
          <Show when={policy()}>
            {(effective) => (
              <>
                <p>Корень рабочей области: {effective().workspaceRoot}</p>
                <p>
                  Ограничения: сеть не изолируется; системные изоляции не распространяются на
                  переменные среды, ранее открытые файлы и уже отделившиеся процессы.
                </p>
                <For each={effective().roots}>
                  {(root) => (
                    <p>
                      {root.access.toUpperCase()} · {root.path} · {root.provenance}
                    </p>
                  )}
                </For>
              </>
            )}
          </Show>
          <Show when={operationInFlight()}>
            <p>
              Запрошена операция {operationInFlight()!.operationId}. Можно получить её
              backend-состояние или отменить через отдельный RPC.
            </p>
            <Button disabled={busy()} onClick={() => void recover()}>
              Проверить состояние
            </Button>
          </Show>
        </Dialog>
      </Show>
    </PageSection>
  )
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
