// The environments of one collection, as a SURFACE rather than a dialog.
//
// It began as a modal with a name field and a table in it, which was wrong
// twice. An environment is not a thing you fill in and dismiss — it is a list
// you keep, compare and come back to, and the owner's reference (Bruno) makes
// it a page for exactly that reason: the environments beside each other on the
// left, the one you are editing on the right, and nothing covering the panel
// while you do it. A modal also cannot show you the OTHER environments, which
// is most of what somebody looking at this is trying to do.
//
// It holds a DRAFT while it is open — the rows being typed — and the file
// stays the truth (§6.4). Save writes and re-reads; Reset throws the draft
// away and reads again. Nothing is written on a keystroke: an environment is
// what a request goes out under, and half-typed rows on disk are half-typed
// rows in a send.
//
// WHAT A SECRET ROW MEANS HERE, and why its value is not editable: §8 leaves
// no field in the whole format where a secret value can be spelled. Marking a
// row secret declares that the value lives OUTSIDE the file, under a binding
// in the vault — and today the only writer of bindings is the Postman
// importer (internal/apibind). So the checkbox writes the NAME into
// `secretVars`, the value field goes away with the value, and the row says
// where the value has to come from instead of pretending it can be typed.

import { For, Show, createEffect } from 'solid-js'
import { showToast } from '../ui/toast'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Caption } from '../ui/caption'
import { Checkbox } from '../ui/checkbox'
import { EditableRowList } from '../ui/row-list'
import { EmptyState } from '../ui/empty-state'
import { Field } from '../ui/field'
import { Select } from '../ui/select'
import { IconButton } from '../ui/icon-button'
import { PlusIcon } from '../ui/icons'
import { SecretValueField } from '../ui/secret-value-field'
import { TextField } from '../ui/text-field'
import { environmentPath } from './api-paths'
import type { ApiConnection } from './api-client'
import type { ApiEnvironment, ApiEnvironmentRef, ApiRoute } from './api-model'

/** One row of the values table, while it is being edited.
 *
 *  A LIST, not the object the file holds: an object cannot hold a row whose
 *  name is still empty, and it silently loses a row the moment two names
 *  collide — which is exactly what happens halfway through typing the second
 *  one. The list is the editing shape and the object is the stored shape, and
 *  `toValues` below is the one place that turns one into the other. */
export interface ValueRow {
  name: string
  value: string
  secret: boolean
}

/** The rows as the file holds them: the plain values in one map, the names of
 *  the secret ones in a list beside it. A row with no name is not a variable
 *  — it is a row somebody has started typing — so it is dropped rather than
 *  written as "". A later row wins a repeated name, which is what the last
 *  thing typed means. */
export function toStored(rows: readonly ValueRow[]): {
  values: Record<string, string>
  secretVars: string[]
} {
  const values: Record<string, string> = {}
  const secretVars: string[] = []
  for (const row of rows) {
    const name = row.name.trim()
    if (name === '') continue
    if (row.secret) {
      if (!secretVars.includes(name)) secretVars.push(name)
      // A secret name is not also a plain value: one variable, one place its
      // value comes from. Leaving it in both would let a stale plain value
      // answer a send the binding was supposed to.
      delete values[name]
    } else {
      values[name] = row.value
    }
  }
  return { values, secretVars }
}

/** And back: the file as rows to edit, in a stable order so the table does
 *  not reshuffle itself between one open and the next. */
export function toRows(env: ApiEnvironment): ValueRow[] {
  const plain = Object.keys(env.values)
    .sort()
    .map((name) => ({ name, value: env.values[name], secret: false }))
  const secret = [...env.secretVars].sort().map((name) => ({ name, value: '', secret: true }))
  return [...plain, ...secret]
}

export interface EnvironmentViewProps {
  /** Every environment in the collection, as the listing names them. */
  environments: readonly ApiEnvironmentRef[]
  /** Which one is being edited, by path. '' while a new one is being made. */
  editing: string
  /** Which one a send currently goes out under — the tick in the list. */
  active: string
  creating: boolean
  name: string
  relPath: string
  rows: readonly ValueRow[]
  dirty: boolean
  busy: boolean
  /** The backend's reason the last save was refused, or ''. */
  error: string
  onPick: (relPath: string) => void
  onNew: () => void
  onName: (value: string) => void
  onRelPath: (value: string) => void
  onRows: (rows: readonly ValueRow[]) => void
  onSave: () => void
  onReset: () => void
  /**
   * WHERE A SEND UNDER THIS ENVIRONMENT LEAVES FROM (§6.5).
   *
   * The route lives on the environment beside the address it belongs with,
   * so switching environment moves the two together and a production URL
   * cannot go out around its bastion. The backend has carried it since the
   * sender was written — apisend leases the named profile's pooled SSH
   * connection, the same one a tab uses, authorized the same way — and this
   * is the half that was missing: nothing in the product could choose one,
   * so every environment anybody made was direct.
   */
  route: ApiRoute
  onRoute: (route: ApiRoute) => void
  /** The connections that can be named. Empty where this build has no
   *  profile store, and then the choice is not offered: a picker over
   *  nothing is a control that governs nothing. */
  connections: readonly ApiConnection[]
  /**
   * GIVE A SECRET VARIABLE ITS VALUE — the write that did not exist.
   *
   * A row marked secret says its value lives outside the file, and until
   * this there was no way to put one there: only an IMPORT could mint a
   * binding, so a variable a person declared secret in this editor stayed
   * unresolved for ever and the send it belonged to could never go out.
   *
   * The value goes straight out and is never held here: it is typed, sent
   * and dropped, so this surface never has a signal a credential sits in.
   *
   * ABSENT rather than rejecting where this build has no binding store —
   * the same rule the folder pickers keep, and the same reason: a control
   * that fails when pressed is worse than one that was never drawn.
   */
  onBindSecret?: (variable: string, value: string) => Promise<void>
}

export function EnvironmentView(props: EnvironmentViewProps) {
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)
  // The outcome of a Save, in a toast. The path field carries the refusal
  // while one is being made (it is the control to fix); an edit of an
  // existing environment has no field the refusal belongs to, so it is said
  // where the kit says outcomes are said. Edge-triggered on the refusal
  // itself, so a re-render cannot stack a second sticky toast for the same
  // error.
  let lastRefused = ''
  createEffect(() => {
    const err = props.error
    if (err === '' || props.creating) {
      lastRefused = ''
      return
    }
    if (err === lastRefused) return
    lastRefused = err
    showToast({ level: 'danger', message: err })
  })
  const editingSomething = () => props.creating || props.editing !== ''
  const path = () =>
    props.relPath.trim() !== '' ? props.relPath.trim() : environmentPath(props.name)
  const savable = () => props.name.trim() !== '' && path() !== '' && !props.busy

  const patchRow = (index: number, over: Partial<ValueRow>): void => {
    props.onRows(props.rows.map((r, i) => (i === index ? { ...r, ...over } : r)))
  }

  return (
    <section class="api-environments" aria-label="Environments">
      <div class="api-environments__body">
        {/* The list. It is the reason this is a surface and not a dialog:
            what somebody opens this for is usually the OTHER environment. */}
        <div class="api-environments__list">
          <div class="api-environments__list-head">
            <Caption>In this collection</Caption>
            <IconButton
              id="api-environment-add"
              size="sm"
              title="New environment"
              ariaLabel="New environment"
              onClick={props.onNew}
            >
              <PlusIcon />
            </IconButton>
          </div>
          <Show
            when={props.environments.length > 0}
            fallback={
              <p class="api-environments__note">
                None yet. A URL written in <code>{'{{baseUrl}}'}</code> resolves against one of
                these.
              </p>
            }
          >
            <For each={props.environments}>
              {(env) => (
                // A ghost Button with `selected` — the kit's own answer to
                // "the current choice in a list", which is what the settings
                // rail and Tabs' rows already are. A hand-rolled row here
                // would be a second vocabulary for one concept.
                <Button
                  variant="ghost"
                  selected={env.relPath === props.editing}
                  onClick={() => props.onPick(env.relPath)}
                >
                  {env.name}
                  {/* "This is what a send goes out under" is a different
                      question from "this is what I am editing", and the two
                      are legitimately different rows. */}
                  <Show when={env.relPath === props.active}>
                    <Badge tone="neutral">sending</Badge>
                  </Show>
                </Button>
              )}
            </For>
          </Show>
        </div>

        <div class="api-environments__editor">
          <Show
            when={editingSomething()}
            fallback={
              <EmptyState
                title="Nothing selected"
                description="Pick an environment on the left, or make one."
              />
            }
          >
            <TextField
              id="api-environment-name"
              label="Name"
              description="What the picker beside Send will call it — the name in the file, not the file's name."
              placeholder="Production"
              value={props.name}
              onInput={props.onName}
              required
            />
            {/* The file, and only while one is being made: an edit writes
                back to the file it read, because moving a file is a different
                act from editing one and a form that did both quietly would do
                the wrong one eventually. */}
            <Show when={props.creating}>
              <TextField
                id="api-environment-path"
                label="File"
                description="Inside the collection, under environments/. Safe to commit: no secret value is ever written into it."
                placeholder={environmentPath(props.name) || 'environments/production.json'}
                value={props.relPath}
                error={refusal()}
                onInput={props.onRelPath}
              />
            </Show>

            {/* THE ROUTE. Two controls, and the second only when the first
                asks for it: "direct" names nothing, and a profile picker
                standing beside it would be a control governing an answer
                that had not been given. */}
            <div class="api-environments__route">
              <Field for="api-environment-route" label="Sends from">
                <div data-api-field="route-kind">
                  <Select
                    ariaLabel="Where a send under this environment leaves from"
                    value={props.route.kind}
                    onChange={(kind) =>
                      props.onRoute(
                        kind === 'connection'
                          ? { ...props.route, kind: 'connection' }
                          : { ...props.route, kind: 'direct', profileId: '' },
                      )
                    }
                    options={
                      props.connections.length > 0
                        ? [
                            { value: 'direct', label: 'This machine' },
                            { value: 'connection', label: 'Through a connection' },
                          ]
                        : [{ value: 'direct', label: 'This machine' }]
                    }
                  />
                </div>
              </Field>
              <Show when={props.route.kind === 'connection'}>
                <Field for="api-environment-profile" label="Connection">
                  <div data-api-field="route-profile">
                    <Select
                      ariaLabel="The connection a send goes through"
                      value={props.route.profileId}
                      onChange={(profileId) =>
                        props.onRoute({ ...props.route, kind: 'connection', profileId })
                      }
                      options={props.connections.map((c) => ({ value: c.id, label: c.name }))}
                      placeholder="Choose a connection"
                      placeholderValue=""
                    />
                  </div>
                </Field>
              </Show>
              {/* THE CHECK IT TURNS OFF, never one refusal it forgives.
                  This read "Accept self-signed certificates" and sets
                  InsecureSkipVerify, which forgives all of them — so a person
                  refused for an authority this machine does not know read the
                  only switch on offer, saw a case that was not theirs, and
                  asked for a second switch beside it (nocx-6hg2w.25). A second
                  switch would have been a second owner of one input; the words
                  were what was wrong.

                  The list is under the control and not inside the warning,
                  because the person reading it is holding a refusal and asking
                  "is this my case?" — a list that appears only once the switch
                  is on cannot answer that. Per environment, so dev can have it
                  and production cannot inherit it. */}
              <Checkbox
                label="Do not verify the server's certificate"
                checked={props.route.insecureTls}
                onChange={(insecureTls) => props.onRoute({ ...props.route, insecureTls })}
              />
              <p class="api-environments__note">
                Covers every refusal a certificate can draw: self-signed, signed by an authority
                this machine does not know, expired, or issued for another name.
              </p>
              <Show when={props.route.insecureTls}>
                <p class="api-environments__warning">
                  Sends under this environment do not check that the server is who it says it is. It
                  is written into the collection file, and every run that goes out under it says so.
                </p>
              </Show>

              {/* Both ends of what this promises, in the product's own words:
                  a send that cannot lease the connection FAILS. It is never
                  quietly downgraded to a local dial, which would put a
                  production request on this machine's own interface, around
                  the bastion this control just named. */}
              <Show when={props.route.kind === 'connection'}>
                <p class="api-environments__note">
                  Requests under this environment are dialled from that host, through the same
                  pooled connection a terminal tab uses. A connection that cannot be reached fails
                  the send rather than falling back to this machine.
                </p>
              </Show>
            </div>

            {/* The kit's row list in its table shape — the same component
                the request's params and headers use, so the three tables in
                this feature are one vocabulary rather than three. */}
            <EditableRowList
              variant="table"
              ariaLabel="Environment values"
              columns={[{ label: 'Name' }, { label: 'Value' }, { label: 'Secret' }]}
              rows={props.rows}
              addLabel="Add variable"
              emptyLabel="No variables yet. A URL written in {{baseUrl}} resolves against one of these."
              removeLabel={(i) => `Remove variable ${i + 1}`}
              renderRow={(row, i) => (
                <>
                  <td>
                    <TextField
                      id={`api-environment-var-name-${i}`}
                      ariaLabel={`Variable ${i + 1} name`}
                      placeholder="baseUrl"
                      value={row().name}
                      onInput={(v) => patchRow(i, { name: v })}
                    />
                  </td>
                  <td>
                    <Show
                      when={!row().secret}
                      fallback={
                        <Show
                          when={props.onBindSecret && row().name.trim() !== ''}
                          fallback={
                            <p class="api-environments__note">
                              Bound in the vault — there is no field in this file it could be typed
                              into.
                            </p>
                          }
                        >
                          {/* The kit's field, placed — never a second one
                              built here. It owns the value while it is on
                              screen and drops it the moment the write is
                              accepted; this surface only says which variable
                              the value is for. */}
                          <SecretValueField
                            id={`api-environment-var-secret-${i}`}
                            ariaLabel={`Variable ${i + 1} value, stored in the vault`}
                            placeholder="Paste the value — it goes to the vault, not to the file"
                            title={`Store the value for ${row().name.trim()} in the vault`}
                            onSubmit={(value) => props.onBindSecret?.(row().name.trim(), value)}
                          />
                        </Show>
                      }
                    >
                      <TextField
                        id={`api-environment-var-value-${i}`}
                        ariaLabel={`Variable ${i + 1} value`}
                        placeholder="https://api.example.com"
                        value={row().value}
                        onInput={(v) => patchRow(i, { value: v })}
                      />
                    </Show>
                  </td>
                  <td>
                    <Checkbox
                      ariaLabel={`Variable ${i + 1} is secret`}
                      checked={row().secret}
                      onChange={(v) => patchRow(i, { secret: v })}
                    />
                  </td>
                </>
              )}
              onRemove={(i) => props.onRows(props.rows.filter((_, j) => j !== i))}
              onAdd={() => props.onRows([...props.rows, { name: '', value: '', secret: false }])}
            />

            {/* One Add control, and it is the row list's own at the foot of
                the table. A second beside Save was the same act said twice,
                two inches apart. */}
            <div class="api-environments__actions">
              <Button variant="primary" disabled={!savable()} onClick={props.onSave}>
                Save
              </Button>
              {/* Reset is the draft's undo, so it is offered only when there
                  IS a draft to throw away — and never while making a new one,
                  where "reset" would mean discarding the whole thing, which is
                  what Back does. */}
              <Show when={props.dirty && !props.creating}>
                <Button onClick={props.onReset}>Reset</Button>
              </Show>
            </div>
          </Show>
        </div>
      </div>
    </section>
  )
}
