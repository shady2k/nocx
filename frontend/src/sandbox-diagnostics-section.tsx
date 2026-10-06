import { For, Show, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import type { SandboxPaneContext, SandboxSettingsServices } from './sandbox-ui'
import type { SandboxAccessListResult } from './generated/sandbox.access.list'
import type { DiagnosticRecord } from './generated/sandbox.access.record'
import type { SandboxProfileResult } from './generated/sandbox.profile.get'
import { Button, StatusCard } from './ui'
import { Dialog } from './ui/dialog'
import { RpcError } from './dispatcher'

interface Props {
  services: SandboxSettingsServices
  context: SandboxPaneContext
  launchId: string
  onProfileResolved: (profile: SandboxProfileResult) => void
}

const PAGE_SIZE = 200
type Decision = 'dismiss' | 'allow-ro' | 'allow-rw'

export function SandboxDiagnosticsSection(props: Props) {
  const captured = untrack(() => props.context)
  const launchId = untrack(() => props.launchId)
  const [result, setResult] = createSignal<SandboxAccessListResult | null>(null)
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal('')
  const [resolutionError, setResolutionError] = createSignal('')
  const [cursor, setCursor] = createSignal(0)
  const [history, setHistory] = createSignal<number[]>([])
  const [choice, setChoice] = createSignal<{
    row: DiagnosticRecord
    decision: Decision
    standardRevision: number
    workspaceRevision: number
    workspaceId: string
    defaultWorkspaceId: string
  } | null>(null)
  const [working, setWorking] = createSignal(false)
  const [message, setMessage] = createSignal('')
  let generation = 0
  let actionGeneration = 0
  let disposed = false

  onCleanup(() => {
    disposed = true
    generation++
    actionGeneration++
  })

  const valid = () => {
    const currentContext = props.context
    return (
      !disposed &&
      captured.isCurrent() &&
      currentContext.isCurrent() &&
      currentContext.paneId === captured.paneId &&
      currentContext.workspaceId === captured.workspaceId &&
      currentContext.kind === captured.kind &&
      props.launchId === launchId
    )
  }

  const refresh = async (nextCursor = 0) => {
    const request = ++generation
    setLoading(true)
    setError('')
    try {
      const registered = await captured.registered
      if (!registered || disposed || request !== generation || !valid()) return
      const page = await props.services.client.sandboxAccessList({
        paneId: captured.paneId,
        launchId,
        cursor: nextCursor,
        limit: PAGE_SIZE,
      })
      if (disposed || request !== generation || !valid()) return
      if (
        page.paneId !== captured.paneId ||
        page.launchId !== launchId ||
        page.workspaceId !== captured.workspaceId
      ) {
        setResult(null)
        setError(
          'Ответ диагностики не соответствует закреплённой панели, запуску и рабочей области.',
        )
        return
      }
      setResult(page)
      setCursor(nextCursor)
    } catch (e) {
      if (!disposed && request === generation && valid())
        setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!disposed && request === generation) setLoading(false)
    }
  }

  createEffect(() => {
    void props.launchId
    void props.context
    void refresh(0)
  })

  createEffect(() => {
    const stop = props.services.client.onSandboxAccessChanged((fact) =>
      untrack(() => {
        if (!valid() || fact.paneId !== captured.paneId || fact.launchId !== launchId) return
        void refresh(cursor())
      }),
    )
    onCleanup(stop)
  })

  const goNext = () => {
    const current = result()
    if (
      !current ||
      current.inbox.nextCursor <= cursor() ||
      current.inbox.nextCursor >= current.inbox.total
    )
      return
    setHistory((pages) => [...pages, cursor()])
    void refresh(current.inbox.nextCursor)
  }
  const goPrevious = () => {
    const pages = history()
    const previous = pages[pages.length - 1]
    if (previous === undefined) return
    setHistory((pages) => pages.slice(0, -1))
    void refresh(previous)
  }

  const allowEligible = (row: DiagnosticRecord) =>
    row.state === 'unresolved' &&
    props.services.defaultWorkspaceId() !== '' &&
    row.pathKnown &&
    row.access !== 'unknown' &&
    row.proposal !== null &&
    row.proposal.directory.length > 0 &&
    row.operation !== ''
  const readCapturedProfile = () => {
    const defaultWorkspaceId = props.services.defaultWorkspaceId()
    return props.services.client.sandboxProfile(
      defaultWorkspaceId !== '' && captured.workspaceId === defaultWorkspaceId
        ? { workspaceId: '' }
        : { workspaceId: captured.workspaceId },
    )
  }

  const openChoice = (row: DiagnosticRecord, decision: Decision) => {
    if (!valid()) return
    const snapshot = result()
    if (!snapshot || row.state !== 'unresolved' || (decision !== 'dismiss' && !allowEligible(row)))
      return
    setChoice({
      row,
      decision,
      standardRevision: snapshot.standardRevision,
      workspaceRevision: snapshot.workspaceRevision,
      workspaceId: snapshot.workspaceId,
      defaultWorkspaceId: props.services.defaultWorkspaceId(),
    })
  }

  const resolve = async () => {
    const selected = choice()
    if (!selected || working() || !valid()) return
    const { row, decision, standardRevision, workspaceRevision } = selected
    if (
      row.state !== 'unresolved' ||
      (decision !== 'dismiss' &&
        (!allowEligible(row) ||
          selected.defaultWorkspaceId === '' ||
          selected.defaultWorkspaceId !== props.services.defaultWorkspaceId()))
    ) {
      setChoice(null)
      setMessage(
        'Рабочая область изменилась или ещё не определена. Обновите журнал и подтвердите действие заново.',
      )
      void refresh(0)
      return
    }
    const request = ++actionGeneration
    setWorking(true)
    setResolutionError('')
    try {
      const resolved = await props.services.client.sandboxResolveAccess({
        paneId: captured.paneId,
        launchId,
        eventId: row.id,
        eventRevision: row.revision,
        decision,
        expectedStandardRevision: standardRevision,
        expectedWorkspaceRevision: workspaceRevision,
      })
      if (disposed || request !== actionGeneration || !valid()) return
      setChoice(null)
      if (
        (decision === 'dismiss' && resolved.record.state === 'dismissed') ||
        (decision !== 'dismiss' && resolved.record.state === 'future-policy')
      ) {
        props.onProfileResolved(resolved.profile)
        setMessage(
          decision === 'dismiss'
            ? 'Событие отмечено обработанным; политика не изменена.'
            : 'Изменение будущего профиля сохранено. Текущий запуск и последняя команда не изменены.',
        )
      } else {
        setResolutionError('')
        setMessage(
          'Разрешение ещё не подтверждено; результат может быть неизвестен. Обновляем профиль и журнал.',
        )
        try {
          const currentProfile = await readCapturedProfile()
          if (!disposed && request === actionGeneration && valid())
            props.onProfileResolved(currentProfile)
        } catch {
          /* Keep the resolution outcome explicitly uncertain. */
        }
      }
      if (!disposed && request === actionGeneration && valid()) await refresh(0)
    } catch (e) {
      if (disposed || request !== actionGeneration || !valid()) return
      const text = e instanceof Error ? e.message : String(e)
      setChoice(null)
      setResolutionError(text)
      const reason =
        e instanceof RpcError &&
        typeof e.data === 'object' &&
        e.data !== null &&
        'reason' in e.data &&
        typeof e.data.reason === 'string'
          ? e.data.reason
          : ''
      const stale =
        reason === 'stale_revision' ||
        reason === 'profile_conflict' ||
        reason === 'diagnostic_conflict' ||
        reason === 'diagnostic_retarget'
      setMessage(
        stale
          ? 'Подтверждение устарело. Обновляем профиль и журнал; требуется новое подтверждение.'
          : 'Результат операции может быть неизвестен; изменения не считаются отменёнными. Обновляем профиль и журнал.',
      )
      try {
        const profile = await readCapturedProfile()
        if (!disposed && request === actionGeneration && valid()) props.onProfileResolved(profile)
      } catch {
        /* The original outcome remains explicitly uncertain. */
      }
      if (!disposed && request === actionGeneration && valid()) await refresh(0)
    } finally {
      if (!disposed && request === actionGeneration) setWorking(false)
    }
  }

  const confirmLabel = () =>
    choice()?.decision === 'dismiss' ? 'Отметить обработанным' : 'Добавить в будущий профиль'
  const futureScope = () => {
    const selected = choice()
    if (!selected) return ''
    if (selected.defaultWorkspaceId !== '' && selected.workspaceId === selected.defaultWorkspaceId)
      return 'будущую стандартную политику'
    const workspace = props.services.workspaces().find((item) => item.id === selected.workspaceId)
    return `будущую политику рабочей области «${workspace?.name ?? selected.workspaceId}»`
  }

  return (
    <section aria-label="Диагностика доступа песочницы">
      <h2>События доступа текущего запуска</h2>
      <p>Только локальный запуск Enforce: {launchId}</p>
      <Show
        when={result()}
        fallback={
          <StatusCard
            title="Журнал диагностики"
            description={loading() ? 'Загрузка…' : error() || 'Ожидаем журнал.'}
            tone={error() ? 'danger' : 'neutral'}
          />
        }
      >
        {(data) => (
          <>
            <p>
              Наблюдатель: {data().inbox.observer}; записей: {data().inbox.total}; ревизия журнала:{' '}
              {data().inbox.revision}; отброшено: {data().inbox.dropped}
              {data().inbox.discontinuity ? '; обнаружен разрыв истории' : ''}
            </p>
            <Show when={data().reason}>
              <p>{data().reason}</p>
            </Show>
            <Show
              when={
                data().inbox.observer === 'failed' ||
                data().inbox.observer === 'unavailable' ||
                data().inbox.observer === 'unsupported'
              }
            >
              <p>
                Наблюдение неактивно или недоступно; отсутствие событий не доказывает отсутствие
                попыток.
              </p>
            </Show>
            <Show when={!props.services.defaultWorkspaceId()}>
              <p role="status">
                Идентификатор стандартной рабочей области ещё не загружен; изменения будущей
                политики недоступны.
              </p>
            </Show>
            <Show when={error()}>
              <StatusCard tone="danger" title="Не удалось обновить журнал" description={error()} />
            </Show>
            <Show when={resolutionError()}>
              <StatusCard
                tone="danger"
                title="Изменение будущей политики не подтверждено"
                description={resolutionError()}
              />
            </Show>
            <Show when={message()}>
              <p role="status">{message()}</p>
            </Show>
            <Button
              disabled={loading() || working()}
              onClick={() => {
                setHistory([])
                void refresh(0)
              }}
            >
              Обновить
            </Button>
            <For each={data().inbox.records}>
              {(row) => (
                <article>
                  <h3>
                    {row.operation} · {row.access === 'unknown' ? 'доступ неизвестен' : row.access}
                  </h3>
                  <p>
                    Идентификатор события: {row.id}; ревизия: {row.revision}
                  </p>
                  <p>
                    Источник: {row.source}; точность: {row.precision}; прогноз политики:{' '}
                    {row.prediction}; состояние: {row.state}; повторов: {row.count}
                  </p>
                  <p>Исполняемый файл: {row.executable || 'неизвестен'}</p>
                  <p>Путь: {row.pathKnown ? row.path : 'неизвестен'}</p>
                  <Show when={row.precision === 'attempted' && row.source === 'linux-seccomp'}>
                    <p>Зафиксирована попытка; это не подтверждение отказа ядром.</p>
                  </Show>
                  <Show
                    when={row.precision === 'reported-denial' && row.source === 'macos-seatbelt'}
                  >
                    <p>Наблюдатель сообщил об отказе Seatbelt.</p>
                  </Show>
                  <Show when={row.state === 'future-policy'}>
                    <p>
                      Добавлено в будущую политику (ревизия {row.futureRevision}); активный запуск
                      не изменён.
                    </p>
                  </Show>
                  <Show
                    when={
                      row.pathKnown &&
                      row.access !== 'unknown' &&
                      row.state === 'unresolved' &&
                      row.proposal
                    }
                  >
                    {(proposal) => (
                      <p>
                        Предложение: {proposal().directory} ·{' '}
                        {proposal().basis === 'parent' ? 'родительский каталог' : 'каталог'}
                        {proposal().missingTarget ? ' · целевой путь отсутствует' : ''}
                      </p>
                    )}
                  </Show>
                  <Show when={row.state === 'unresolved'}>
                    <Button disabled={working()} onClick={() => openChoice(row, 'dismiss')}>
                      Отклонить предложение
                    </Button>
                    <Show when={allowEligible(row)}>
                      <Button disabled={working()} onClick={() => openChoice(row, 'allow-ro')}>
                        Разрешить чтение в будущем
                      </Button>
                      <Button disabled={working()} onClick={() => openChoice(row, 'allow-rw')}>
                        Разрешить чтение и запись в будущем
                      </Button>
                    </Show>
                  </Show>
                </article>
              )}
            </For>
            <nav aria-label="Страницы событий">
              <Button
                disabled={loading() || working() || history().length === 0}
                onClick={goPrevious}
              >
                Предыдущая
              </Button>
              <span>
                {data().inbox.records.length === 0 ? 0 : cursor() + 1}–
                {Math.min(cursor() + data().inbox.records.length, data().inbox.total)} /{' '}
                {data().inbox.total}
              </span>
              <Button
                disabled={
                  loading() ||
                  working() ||
                  data().inbox.nextCursor <= cursor() ||
                  data().inbox.nextCursor >= data().inbox.total
                }
                onClick={goNext}
              >
                Следующая
              </Button>
            </nav>
          </>
        )}
      </Show>
      <Dialog
        open={choice() !== null}
        onClose={() => !working() && setChoice(null)}
        title={
          choice()?.decision === 'dismiss'
            ? 'Отклонить предложение доступа?'
            : 'Изменить будущую политику?'
        }
        footer={
          <>
            <Button disabled={working()} onClick={() => setChoice(null)} autofocus>
              Отмена
            </Button>
            <Button disabled={working()} onClick={() => void resolve()}>
              {working() ? 'Сохранение…' : confirmLabel()}
            </Button>
          </>
        }
      >
        <Show when={choice()}>
          {(selected) => (
            <>
              <p>
                Событие {selected().row.id}, ревизия {selected().row.revision}; текущая политика
                запуска не меняется.
              </p>
              <Show when={selected().decision !== 'dismiss'}>
                <p>
                  Будет добавлен точный предложенный путь: {selected().row.proposal?.directory}.
                </p>
                <p>
                  {selected().row.proposal?.basis === 'parent'
                    ? 'Предложен родительский каталог.'
                    : 'Предложен каталог.'}
                  {selected().row.proposal?.missingTarget ? ' Целевой путь отсутствует.' : ''}
                </p>
                <p>Изменение затронет только {futureScope()}.</p>
                <Show when={selected().decision === 'allow-rw'}>
                  <p>Чтение и запись дают более широкое разрешение, чем только чтение.</p>
                </Show>
              </Show>
            </>
          )}
        </Show>
      </Dialog>
    </section>
  )
}
