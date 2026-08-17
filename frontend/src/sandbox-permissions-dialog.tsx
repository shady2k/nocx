/**
 * Sandbox permissions dialog — the one pre-launch modal (ADR-0036 §4).
 *
 * An imperative dialog, built on `Dialog` like every other modal in the app.
 * It shows the mandatory workspace (always read-write) and the two persisted
 * baselines — read-only and read & write folders. Each baseline entry is
 * checked by default and unchecking one records an exact removal for that
 * class in this tab only. Ephemeral additions are picked through the native
 * directory picker and can be removed row-by-row; a pick that would place the
 * same directory in both classes is refused with visible feedback rather than
 * emitting a contradictory delta.
 *
 * The result is the four class-scoped permission DELTAS — `addWritable`,
 * `removeWritable`, `addReadOnly`, `removeReadOnly` — never a baseline itself
 * or any effective policy root. The backend is the sole policy author; this
 * surface only reports what the user changed.
 */
import { createSignal, For, type Component } from 'solid-js'
import { render } from 'solid-js/web'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { Field } from './ui/field'
import { Section } from './ui/section'
import { Stack } from './ui/stack'
import { EditableRowList } from './ui/row-list'
import { showToast } from './ui/toast'

/** The confirmed permission deltas: four class-scoped arrays (possibly empty). */
export interface SandboxPermissionsResult {
  readonly addWritable: string[]
  readonly removeWritable: string[]
  readonly addReadOnly: string[]
  readonly removeReadOnly: string[]
}

/** The dialog's inputs: the mandatory workspace, the two persisted baselines,
 *  and the native folder picker for ephemeral additions. */
export interface SandboxPermissionsOptions {
  readonly workspace: string
  readonly baselineWritable: readonly string[]
  readonly baselineReadOnly: readonly string[]
  readonly openDirectory: () => Promise<{ path: string }>
}

const MAX_PERMISSION_PATHS = 32

/** One permission class's rendered section: the persisted baseline (checked by
 *  default), the ephemeral additions, and one native picker action. */
interface ClassSectionProps {
  readonly title: string
  readonly hint: string
  /** Singular noun, e.g. "read-only folder", for the picker/remove labels. */
  readonly noun: string
  readonly baseline: readonly string[]
  readonly removed: () => readonly string[]
  readonly additions: () => readonly string[]
  readonly picking: () => boolean
  readonly onToggle: (path: string, checked: boolean) => void
  readonly onRemove: (index: number) => void
  readonly onAdd: () => void
}

const ClassSection: Component<ClassSectionProps> = (props) => (
  <Section title={props.title}>
    <p class="sandbox-permissions-hint">{props.hint}</p>
    <div class="sandbox-permissions-baseline">
      <For each={props.baseline}>
        {(path) => (
          <Checkbox
            checked={!props.removed().includes(path)}
            onChange={(checked) => props.onToggle(path, checked)}
            label={path}
          />
        )}
      </For>
    </div>
    <EditableRowList
      rows={props.additions()}
      ariaLabel={`${props.title} added for this tab`}
      addLabel={`Add ${props.noun} for this tab`}
      emptyLabel={`No ${props.noun}s added for this tab.`}
      removeLabel={(i) => `Remove added ${props.noun} ${i + 1}`}
      disabled={props.picking()}
      onRemove={props.onRemove}
      onAdd={props.onAdd}
      renderRow={(path) => <span class="sandbox-permissions-path">{path()}</span>}
    />
  </Section>
)

const SandboxPermissionsDialog: Component<
  SandboxPermissionsOptions & { onResolve: (result: SandboxPermissionsResult | null) => void }
> = (props) => {
  // Baseline entries the user unchecked — exact canonical removals per class.
  const [removedWritable, setRemovedWritable] = createSignal<readonly string[]>([])
  const [removedReadOnly, setRemovedReadOnly] = createSignal<readonly string[]>([])
  // Ephemeral directories added for this tab, per class.
  const [addWritable, setAddWritable] = createSignal<readonly string[]>([])
  const [addReadOnly, setAddReadOnly] = createSignal<readonly string[]>([])
  // While a native picker is open, the list affordances are inert.
  const [picking, setPicking] = createSignal(false)

  // A path is ACTIVE in a class when it is a checked baseline entry or an
  // ephemeral addition — exactly what that class grants for this tab.
  const activeWritable = (path: string) =>
    (props.baselineWritable.includes(path) && !removedWritable().includes(path)) ||
    addWritable().includes(path)
  const activeReadOnly = (path: string) =>
    (props.baselineReadOnly.includes(path) && !removedReadOnly().includes(path)) ||
    addReadOnly().includes(path)

  // Shared picker machinery for one class: refuse a cross-class duplicate,
  // re-enable an unchecked baseline entry, otherwise append up to the bound.
  const pickDirectory = async (args: PickDirectoryArgs) => {
    setPicking(true)
    try {
      const picked = await props.openDirectory()
      // Empty path = cancelled: a no-op.
      if (!picked.path) return
      // A path already granted by the other class cannot be granted again
      // here: the pick is refused with visible feedback, never a
      // contradictory delta.
      if (args.otherActive(picked.path)) {
        showToast({
          level: 'danger',
          message: `"${picked.path}" is already in the other folders list. Remove it there first to change its access.`,
        })
        return
      }
      // Re-picking an unchecked baseline entry means re-enable that grant. Do
      // not manufacture an add/remove conflict for the backend to reject.
      if (args.baseline.includes(picked.path)) {
        args.setRemoved((prev) => prev.filter((path) => path !== picked.path))
        return
      }
      if (args.additions().length >= MAX_PERMISSION_PATHS) {
        showToast({
          level: 'danger',
          message: `At most ${MAX_PERMISSION_PATHS} folders can be added for one tab.`,
        })
        return
      }
      args.setAdditions((prev) => (prev.includes(picked.path) ? prev : [...prev, picked.path]))
    } catch (err) {
      // An unavailable native runtime is visible rather than silent: the
      // surface cannot type a path by hand (ADR-0036 §4), so it says why.
      showToast({
        level: 'danger',
        message: `Could not open the folder picker: ${
          err instanceof Error ? err.message : String(err)
        }`,
      })
    } finally {
      setPicking(false)
    }
  }

  const toggleBaseline = (path: string, checked: boolean) => {
    setRemovedWritable((prev) => (checked ? prev.filter((p) => p !== path) : [...prev, path]))
  }

  const removeAdditionWritable = (index: number) => {
    setAddWritable((prev) => prev.filter((_, i) => i !== index))
  }

  const addWritableDirectory = () => {
    void pickDirectory({
      baseline: props.baselineWritable,
      additions: addWritable,
      removed: removedWritable,
      otherActive: activeReadOnly,
      setAdditions: setAddWritable,
      setRemoved: setRemovedWritable,
    })
  }

  const toggleBaselineReadOnly = (path: string, checked: boolean) => {
    setRemovedReadOnly((prev) => (checked ? prev.filter((p) => p !== path) : [...prev, path]))
  }

  const removeAdditionReadOnly = (index: number) => {
    setAddReadOnly((prev) => prev.filter((_, i) => i !== index))
  }

  const addReadOnlyDirectory = () => {
    void pickDirectory({
      baseline: props.baselineReadOnly,
      additions: addReadOnly,
      removed: removedReadOnly,
      otherActive: activeWritable,
      setAdditions: setAddReadOnly,
      setRemoved: setRemovedReadOnly,
    })
  }

  const confirm = () => {
    props.onResolve({
      addWritable: [...addWritable()],
      removeWritable: [...removedWritable()],
      addReadOnly: [...addReadOnly()],
      removeReadOnly: [...removedReadOnly()],
    })
  }

  const cancel = () => props.onResolve(null)

  const workspaceId = 'sandbox-permissions-workspace'

  return (
    <Dialog
      open
      onClose={cancel}
      title="Sandbox permissions"
      footer={
        <>
          <Button variant="default" onClick={cancel}>
            Cancel
          </Button>
          <Button variant="primary" onClick={confirm}>
            Open sandboxed tab
          </Button>
        </>
      }
    >
      <Stack gap="loose">
        <Field for={workspaceId} label="Workspace" orientation="vertical">
          <span id={workspaceId} class="sandbox-permissions-workspace">
            {props.workspace}
          </span>
          <p class="sandbox-permissions-hint">{'Read & write (required)'}</p>
        </Field>
        <ClassSection
          title="Read-only folders"
          hint="Checked folders are read-only in this tab. Uncheck one to remove it from this tab only."
          noun="read-only folder"
          baseline={props.baselineReadOnly}
          removed={removedReadOnly}
          additions={addReadOnly}
          picking={picking}
          onToggle={toggleBaselineReadOnly}
          onRemove={removeAdditionReadOnly}
          onAdd={addReadOnlyDirectory}
        />
        <ClassSection
          title="Read & write folders"
          hint="Checked folders are read & write in this tab. Uncheck one to remove it from this tab only — the workspace is always writable."
          noun="read & write folder"
          baseline={props.baselineWritable}
          removed={removedWritable}
          additions={addWritable}
          picking={picking}
          onToggle={toggleBaseline}
          onRemove={removeAdditionWritable}
          onAdd={addWritableDirectory}
        />
      </Stack>
    </Dialog>
  )
}

/** Shared picker machinery for one class: refuse a cross-class duplicate,
 *  re-enable an unchecked baseline entry, otherwise append up to the bound. */
interface PickDirectoryArgs {
  readonly baseline: readonly string[]
  readonly additions: () => readonly string[]
  readonly removed: () => readonly string[]
  readonly otherActive: (path: string) => boolean
  readonly setAdditions: (fn: (prev: readonly string[]) => readonly string[]) => void
  readonly setRemoved: (fn: (prev: readonly string[]) => readonly string[]) => void
}

/**
 * Imperative sandbox permissions dialog — resolves to the confirmed deltas or
 * null when cancelled (Escape, outside click, or the Cancel button).
 *
 * Settles and disposes exactly once, and restores focus through `Dialog`'s own
 * overlay cleanup (the same deferred-dispose pattern as `showConfirm`).
 */
export function showSandboxPermissions(
  options: SandboxPermissionsOptions,
): Promise<SandboxPermissionsResult | null> {
  // Promise.withResolvers needs ES2024 and this project targets ES2021, so the
  // resolver is captured via the executor form (the codebase pattern).
  return new Promise((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)

    let dispose: (() => void) | null = null
    let settled = false

    const finish = (result: SandboxPermissionsResult | null) => {
      // Escape fires the cancel path and the disposer can run again on unmount;
      // the promise must resolve exactly once.
      if (settled) return
      settled = true
      // Deferred so Dialog's own cleanup — popOverlay and focus restore — runs
      // against a live root before it is torn down.
      queueMicrotask(() => {
        dispose?.()
        host.remove()
      })
      resolve(result)
    }

    dispose = render(
      () => (
        <SandboxPermissionsDialog
          workspace={options.workspace}
          baselineWritable={options.baselineWritable}
          baselineReadOnly={options.baselineReadOnly}
          openDirectory={options.openDirectory}
          onResolve={finish}
        />
      ),
      host,
    )
  })
}
