# Host integration method: Auto copy

Design-only delta for ALX-204 / `nocx-oalo1.11.2`, 2026-11-17. No catalogue,
application, backend, consent, contract or protocol changes are included.

## Authority and source

The architect's ALX-18 decision at **2026-11-17T13:49:11Z** settles B2 of
ALX-195: Auto forgets only the host's saved method, not the machine's consent.
A machine that already granted helper permission continues with the helper
without a new question. This clarifies the incomplete Auto wording in
ADR-727g63tr decision 1; ADR-0105 keeps method and permission separate. AD-8
requires one owner for each fact: the editor is not a second consent decider.
No accepted ADR is edited or superseded by this copy document.

Source: `feat/nocx-oalo1-11` at
`263db122ea2ade7d3a1620dfe1c53f0906bd51df`, exported from the main checkout's
existing git objects. The requested box `oalo1-11` was stopped; it was not
started or modified. Its fetched stage ref and local stage ref both named
this revision. The source mock blob is
`d0dfd8ab3029e95ba1ac4b2dbdec781d685bd052`; the interface-rules blob is
`614bb27c61814668f3f3b8ed037b20939f446107`.

The stage's SSH-foundation spec (§5.10), ADR-0105, ADR-727g63tr, interface
rules, mock and actual editor/catalogues were read. No separate glossary
was found: use the existing catalogue terms **Integration method / Метод
интеграции**, **Auto / Авто**, **helper / хелпер**, **machine / машина**.
Do not introduce a second Russian term for the existing field.

## Final copy

The field label is unchanged: **Integration method** / **Метод интеграции**.
The option identifies the policy; the explanation spells out permission
reuse without pretending to report this particular machine's consent state.

| Key                                    | Action  | English                                                                                                                                                                                                                                                                 | Russian                                                                                                                                                                                                                                                                    |
| -------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `connections.mode.hostAuto`            | Replace | Auto — nocx chooses at connect                                                                                                                                                                                                                                          | Авто — nocx выбирает при подключении                                                                                                                                                                                                                                       |
| `connections.editor.methodAutoHint`    | Add     | nocx chooses the integration method when you connect. It asks only if this machine has no recorded answer. If the machine has already allowed the helper, it continues to use it without asking again. Auto clears only the saved method, not the machine's permission. | nocx выбирает метод интеграции при подключении. Он спрашивает, только если для этой машины ещё нет сохранённого ответа. Если машина уже разрешила хелпер, он продолжит работать без повторного вопроса. Авто сбрасывает только сохранённый метод, но не разрешение машины. |
| `connections.editor.methodUnknownHost` | Replace | Connect to this machine before changing its integration method here.                                                                                                                                                                                                    | Подключитесь к этой машине, прежде чем менять её метод интеграции здесь.                                                                                                                                                                                                   |

The last replacement removes the same unconditional promise from the
disabled-field explanation. A new saved profile need not be an unanswered
machine. The disabled state needs an enabling action, not a permission claim.

## Developer handoff

Catalogue paths on the stage are `frontend/src/i18n/catalogues/en.ts` and
`frontend/src/i18n/catalogues/ru.ts`. Replace/add only the three keys above.
The catalogue files are **not changed in this delivery**.

In the later implementation, use the identical English fallback in
`frontend/src/connections.tsx` for `HOST_MODES` and both
`methodUnknownHost` call sites. Add the new explanation through the existing
hint pattern beside `helperHint`, when the method is settable and the chosen
value is Auto. Keep it visible for an unchanged stored Auto too. It is policy
copy, so it needs no consent lookup, guessed status, badge or new control.
Associate the explanation with the select's accessible description, preserving
its existing owner description/error relationships.

Keep `methodHostDescHead/Tail`, `methodChangeHint`, `helperHint`, other method
options, loading/error text, Retry and Save semantics unchanged. The change
hint still appears only for a dirty selection. Do not show the new Auto
explanation on a disabled/loading/read-failed/address-edited field: its
existing state explanation or error owns that space.

Do not copy the updated mock's illustrative JavaScript into product code.
Update stale comments describing Auto as an unconditional question in the
same implementation, without changing resolver behavior. Implementation
remains behind ALX-203 and the remaining architecture-acceptance blockers;
this design delivery does not release them.

## Mock delta and placement

`host-integration-method-auto-copy.patch` targets the existing stage files:

- `docs/design/ssh-foundation/host-integration-method.html`: frame 1 starts
  at Auto, shows the policy explanation and still lets a reader switch the
  method; its key table records the replacement and new key. Frames 2 and 3
  use the new Auto option wherever the disabled select displays it. Frame 2
  uses the revised enabling-action description. Frame 5 shows Auto and the
  complete explanation at the existing 360 px window / 312 px panel width.
- `docs/design/interface-rules.md`: adds “Auto describes a policy, not an
  unanswered permission”. Existing rules remain unchanged.

Apply from the stage checkout with `git apply --check` first, then
`git apply`, using the delivered patch. These source files do not exist at
this task branch's older baseline, so they are not imported wholesale here.
The full updated self-contained HTML is attached to ALX-204 as a viewing
copy; the patch is the integration artifact. Both preserve the source mock's
embedded project styles and existing kit identities. No new component,
spacing, typography, color or repaint rule is introduced.

## User walk and states

1. Open an existing connection, select Advanced, find Integration method.
   The host-owner sentence still says whose setting this is.
2. Choose Auto. Read who chooses and when, the conditional question, reuse
   of prior helper permission, and that permission is not cleared.
3. Save Connection: only the saved method is cleared. Cancel discards the
   draft. Neither action introduces a consent control.
4. Open a new tab to a machine that previously allowed the helper: no new
   question is promised; the helper continues. An already open shell keeps
   its method under the existing lifecycle rule.

| State                                            | Required presentation / interaction                                                                                                                      |
| ------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Known host, stored Auto                          | New option and full Auto explanation, no dirty-change hint                                                                                               |
| Known host, another stored method; Auto selected | Same explanation plus the existing dirty-change hint; Save/Cancel unchanged                                                                              |
| No recorded machine answer                       | Explanation says a question is conditional on this absence; it does not claim the editor has read consent                                                |
| Prior helper grant                               | Same policy copy; no promise of a new question or clearing permission                                                                                    |
| Prior refusal                                    | Same policy copy; do not imply Auto revokes or upgrades a refusal                                                                                        |
| New/unresolved host or edited address            | Disabled effective Auto, state-specific description; no Auto explanation                                                                                 |
| Loading                                          | Disabled Loading… placeholder; no Auto explanation                                                                                                       |
| Read failure                                     | Disabled select, existing error and Retry; no Auto explanation                                                                                           |
| Write failure / store permission denied          | Existing split-outcome error, retained choice and Save retry; if known/settable Auto is selected, its policy explanation remains                         |
| Narrow screen / long host / many connections     | Short option, complete wrapped explanation below the control; existing host wrapping and layout, no tooltip-only explanation; no new list or aggregation |

There is no new role/permission state. A settings-store denial uses the
existing error state, not a disabled permission toggle.

## Acceptance and evidence limits

- The old option and its “auto means not answered” table note are absent
  from the revised mock. All disabled Auto examples use the same new label.
- Switching frame 1 to Auto shows its explanation even when not dirty;
  switching away hides it. The ordinary dirty hint remains independent.
- The architect's regression walk is the later product acceptance check:
  choose Helper and give permission, save Auto in the editor, open again,
  observe no permission question and a working helper. Changing text must
  not mutate consent or the backend ladder.
- Local checks for this delivery: exact patch application against the two
  source blobs and static HTML/content consistency. No browser rendering,
  screenshots, product tests or end-to-end run is claimed. A real narrow
  screenshot remains an implementation/acceptance check, not a reason to
  alter kit styles in this text-only delta.

No owner question is open. The architect's decision is authoritative.
