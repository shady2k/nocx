# Architecture decision records

One row per file in this directory, newest last. `docs/architecture.md` holds the
invariants (`AD-1`…`AD-10`); these records hold the decisions taken under them and say
why each was taken rather than the obvious alternative.

Read the file, not the row. A one-line summary of an ADR is a pointer, never the
decision.

## Numbering, as it actually is

Every number now names exactly one record, and one number was never allocated.

Four numbers used to name two records each. They were repaired on 2026-08-28 (`nocx-yjvg5`):
in each pair the **older** record kept the number, so no citation written against it broke,
and the newer one moved to a free number at the end and carries a **Formerly ADR-00NN** line
in its header.

| Was  | Kept by                                                   | Moved to | Which is now                                            |
| ---- | --------------------------------------------------------- | -------- | ------------------------------------------------------- |
| 0006 | Marker-only prompt mode                                   | 0046     | Reusable credentials for SSH connections (superseded)   |
| 0029 | A proposed keystroke is bound to what makes it meaningful | 0047     | A program may ask; it never chooses                     |
| 0033 | `auto` is the name for "not yet answered"                 | 0048     | UI state is a document, not a setting                   |
| 0035 | The AppImage carries WebKitGTK's helper processes         | 0049     | The channel we own is the carrier, not the command line |

- **0009** — never allocated; no file with that number has ever existed in this repo.

An `ADR-00NN` citation written before that date may still mean the moved record. The
**Formerly** line in each moved file, and the table above, are what resolve it.

Two files also **disagreed with their own filenames**, and the numbers their headings
claimed were already taken: `0036-an-http-upload-route-beside-the-websocket.md` was headed
`ADR-0039` and `0037-an-http-download-route-beside-the-websocket.md` was headed `ADR-0040`,
while ADR-0039 and ADR-0040 are the assistant-turn and block-tree records. The filenames
were free of conflict, so the filenames won: both headings were brought to 0036 and 0037 on
the same date, and each file records the correction in its header. Every other citation of
ADR-0039 and ADR-0040 in the repository means the assistant-turn or block-tree record and
was left alone.

**When adding a record, the heading and the filename must agree.** Two checks hold that,
and both must print nothing:

```bash
ls docs/decisions | grep -oE '^[0-9]{4}' | sort | uniq -d
for f in docs/decisions/0*.md; do n=$(basename $f | cut -c1-4); \
  h=$(head -3 $f | grep -oE 'ADR[- ]?[0-9]{4}' | head -1 | grep -oE '[0-9]{4}'); \
  [ -n "$h" ] && [ "$n" != "$h" ] && echo "file=$n heading=$h $f"; done
```

## The records

| #    | Title                                                                                                                             | Status                                |
| ---- | --------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------- |
| 0001 | [xterm.js as the VT frontend](0001-xterm-js-as-vt-frontend.md)                                                                    | Accepted (amended 2026-07-26)         |
| 0002 | [Native tabs; no embedded multiplexer](0002-native-tabs-no-embedded-multiplexer.md)                                               | Accepted                              |
| 0003 | [Distribution without a Developer ID](0003-distribution-without-a-developer-id.md)                                                | Accepted (amended 2026-08-30)         |
| 0004 | [Input ownership state machine and a pluggable editor](0004-input-ownership-and-editor-abstraction.md)                            | Accepted                              |
| 0005 | [Linux/WebKitGTK: periodic forced-refresh pump](0005-linux-webkitgtk-forced-refresh-pump.md)                                      | Accepted                              |
| 0006 | [Marker-only prompt mode](0006-marker-only-prompt-mode.md)                                                                        | Accepted                              |
| 0007 | [Cross-platform auto-update via a platform abstraction](0007-cross-platform-auto-update.md)                                       | Accepted                              |
| 0008 | [Command blocks are a keyboard-first ledger, not cards](0008-command-blocks-as-a-keyboard-first-ledger.md)                        | Accepted                              |
| 0010 | [CodeMirror 6 as the editor core](0010-codemirror-6-as-the-editor-core.md)                                                        | Accepted                              |
| 0011 | [Persistence: storage capabilities, secrets as opaque references](0011-persistence-storage-capabilities-and-secret-references.md) | Accepted (§7 superseded by ADR-0027)  |
| 0012 | [SolidJS as the application UI layer](0012-solidjs-as-the-application-ui-layer.md)                                                | Accepted                              |
| 0013 | [Plain CSS with semantic custom properties](0013-plain-css-with-semantic-custom-properties.md)                                    | Accepted                              |
| 0014 | [Component kit foundation: platform-first, per-primitive](0014-component-kit-foundation.md)                                       | Accepted                              |
| 0015 | [`ssh -G` as the `~/.ssh/config` oracle](0015-ssh-g-as-the-ssh-config-oracle.md)                                                  | Accepted                              |
| 0016 | [A secret owns its name](0016-a-secret-owns-its-name.md)                                                                          | Accepted                              |
| 0017 | [A connection references a secret, not a credential](0017-a-connection-references-a-secret.md)                                    | Accepted                              |
| 0018 | [ContentDB: SQLite, encrypted at rest, with its own key](0018-contentdb-engine-and-encryption-at-rest.md)                         | Accepted (amended 2026-08-01)         |
| 0019 | [One authoritative ledger, disposable projections](0019-one-authoritative-ledger-disposable-projections.md)                       | Proposed                              |
| 0020 | [The agent gets a lane, and authority is granted per run](0020-the-agent-gets-a-lane-authority-is-granted-per-run.md)             | Proposed                              |
| 0021 | [Secrets in the prompt: mask what we keep, resolve what we can't](0021-secrets-in-the-prompt.md)                                  | Accepted                              |
| 0022 | [The ssh command line is the carrier, not a second channel](0022-the-ssh-command-line-is-the-carrier.md)                          | Superseded by ADR-0049                |
| 0023 | [A jump route is its own host-key identity](0023-a-jump-route-is-its-own-host-key-identity.md)                                    | Accepted (2026-08-06)                 |
| 0024 | [The lifecycle leaves the byte stream](0024-authenticated-shell-integration-channel.md)                                           | Accepted (amended 2026-08-20)         |
| 0025 | [domain_request carries the destination, not the user's options](0025-domain-request-carries-the-destination-not-the-options.md)  | Accepted (2026-08-09)                 |
| 0026 | [The control plane runs off the read loop under a bound](0026-control-plane-runs-off-the-read-loop.md)                            | Accepted (2026-08-08)                 |
| 0027 | [Backup and restore is one structured file, carrying no credentials](0027-structured-backup-and-restore.md)                       | Accepted                              |
| 0028 | [Eino runs the loop; the grant and the narrowing are ours](0028-eino-runs-the-loop-the-grant-is-ours.md)                          | Accepted                              |
| 0029 | [A proposed keystroke is bound to what makes it meaningful](0029-a-keystroke-is-bound-to-what-makes-it-meaningful.md)             | Proposed                              |
| 0030 | [An AI endpoint references a secret it owns](0030-an-ai-endpoint-references-a-secret-it-owns.md)                                  | Accepted                              |
| 0031 | [Vault reset counts and clears every secret-reference holder](0031-vault-reset-counts-every-secret-reference-holder.md)           | Accepted                              |
| 0032 | [The vault raises its own unlock](0032-the-vault-raises-its-own-unlock.md)                                                        | Accepted                              |
| 0033 | [`auto` is the name for "not yet answered"](0033-auto-is-the-name-for-not-yet-answered.md)                                        | Accepted (2026-08-15)                 |
| 0034 | [Consent to deploy the helper belongs to the machine](0034-consent-belongs-to-the-machine-not-the-connection.md)                  | Accepted                              |
| 0035 | [The AppImage carries WebKitGTK's helper processes](0035-appimage-carries-webkits-helper-processes.md)                            | Accepted                              |
| 0036 | [An HTTP upload route beside the WebSocket](0036-an-http-upload-route-beside-the-websocket.md)                                    | Accepted (2026-08-21)                 |
| 0037 | [An HTTP download route beside the WebSocket](0037-an-http-download-route-beside-the-websocket.md)                                | Accepted (2026-08-22)                 |
| 0038 | [A forward is a route blind to the name](0038-a-forward-is-a-route-blind-to-the-name.md)                                          | Proposed (2026-08-25)                 |
| 0039 | [An assistant turn is one ledger entry](0039-an-assistant-turn-is-one-entry.md)                                                   | Accepted (2026-08-23)                 |
| 0040 | [A block is a node in an ordered tree](0040-a-block-is-a-node-in-an-ordered-tree.md)                                              | Accepted (2026-08-23)                 |
| 0041 | [charmbracelet/x/vt is the backend's emulator, chosen by column geometry](0041-x-vt-as-the-backend-emulator.md)                   | Accepted (2026-08-25)                 |
| 0042 | [A collection file names a secret by handle](0042-a-collection-file-names-a-secret-by-handle.md)                                  | Accepted (2026-08-26)                 |
| 0043 | [One connection to the encrypted store](0043-one-connection-to-the-encrypted-store.md)                                            | Accepted (2026-08-26)                 |
| 0044 | [Request files carry the environment choice](0044-request-files-carry-environment-choice.md)                                      | Accepted (2026-08-27)                 |
| 0045 | [Declared tool calls are the assistant's only carrier](0045-declared-calls-are-the-only-carrier.md)                               | Accepted (2026-08-27)                 |
| 0046 | [Reusable credentials for SSH connections](0046-reusable-credentials.md)                                                          | Superseded by ADR-0017 (was ADR-0006) |
| 0047 | [A program may ask; it never chooses](0047-a-program-may-ask-never-choose.md)                                                     | Accepted (was ADR-0029)               |
| 0048 | [UI state is a document, not a setting](0048-ui-state-is-a-document-not-a-setting.md)                                             | Accepted (was ADR-0033)               |
| 0049 | [The channel we own is the carrier, not the command line](0049-the-channel-we-own-is-the-carrier.md)                              | Accepted 2026-08-20 (was ADR-0035)    |
| 0050 | [The OS keystore holds a key only a person can take](0050-the-keystore-holds-a-key-only-a-person-can-take.md)                     | Accepted 2026-08-29                   |
| 0051 | [An imported credential stays the person's](0051-an-imported-credential-stays-the-persons.md)                                     | Accepted 2026-08-29                   |
| 0052 | [A code identity without a publisher identity](0052-a-code-identity-without-a-publisher-identity.md)                              | Accepted 2026-08-30                   |
| 0053 | [A tool declares the classes it can reach](0053-a-tool-declares-the-classes-it-can-reach.md)                                      | Accepted 2026-08-31                   |
| 0054 | [A table is a stack of rows that carries a grid](0054-a-table-is-a-stack-of-rows-that-carries-a-grid.md)                          | Accepted 2026-08-31                   |
| 0055 | [ContentDB schema changes migrate or refuse](0055-contentdb-schema-changes-migrate-or-refuse.md)                                  | Accepted 2026-09-01                   |
| 0056 | [Supervision outlives the coordinator, not the backend](0056-supervision-outlives-the-coordinator-not-the-backend.md)             | Accepted (2026-09-04)                 |
| 0057 | [On your own machine there is no Tier A fallback](0057-on-your-own-machine-there-is-no-tier-a-fallback.md)                        | Accepted (2026-09-04)                 |

## Adding one

Take the next free number, name the file after the decision rather than the area, and
write prose: what was wrong, what is decided, why this rather than the obvious
alternative, and what the next person inherits. Add the row here in the same commit.
