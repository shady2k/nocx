# Report — nocx-zg3k3.14.1 effects client route

## Delivered

The app forwards identity-bearing helper effects from both fresh hosted opens and session readoption to the current WebSocket subscriber as `session.effect`. The notification has a closed effect-kind set and a JSON Schema contract with regenerated TypeScript types. It remains on the control plane; full `session.frame` snapshots do not carry or replay effects.

The client validates each event, deduplicates on runtime generation plus effect id for the lifetime of the session, and buffers delivery until a pane registers its handler. The renderer routes bell, notification, clipboard, title, and cwd to the existing callbacks. Clipboard delivery still uses the existing permission gate.

## Tests and red-first evidence

Added Go tests `TestPublishSessionEffect_ReachesSubscriberAsIdentityBearingNotification`, `TestPublishSessionEffect_RefusesUnknownKind`, `TestSessionEffectDTOConformsToContract`, and `TestSessionEffect_OverTheWireConformsToContract`.

Added frontend tests under `session.effect notification` for duplicate delivery, notification replay, a full frame without side effects, unknown kinds, malformed identities, and early-event buffering; `runtime effect dispatch` for all five handlers; and `runtime clipboard effects keep the existing permission gate` for denied and allowed writes.

The Go publisher test was authored before the backend route. Its first run could not reach the assertion because the pinned libghostty-vt archive was missing; `make vt-archives` initially received HTTP 504, then succeeded on retry. With dependencies available, temporarily forcing the WebSocket publisher to refuse delivery made the over-the-wire contract test fail at its publish assertion; restoring it made the test pass. The frontend tests were added after implementation, then the event subscription was temporarily disabled as a negative control: delivery and early-buffer tests failed, and passed again after restoration. This provided red-to-green behavioral evidence, but the frontend tests were not written before the implementation; that is a deviation from strict TDD chronology.

## Checks

- `make vt-archives` — passed on retry; pinned archives and headers verified.
- `gofmt` — passed on changed Go files.
- `go test -tags gtk3 -count=1 ./internal/app ./internal/transport` — passed.
- Focused Go session-effect tests — passed after adding the unknown-kind refusal test.
- `go vet -tags gtk3 ./internal/app/ ./internal/transport/` — passed.
- `cd frontend && npm run contracts:check` — passed.
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `cd frontend && npm run format:check` — passed.
- `cd frontend && npx vitest run src/ipc.test.ts src/renderers/xterm.test.ts src/terminal-content.test.ts` — passed.
- Commit hooks, including Go format/lint ratchets, both frontend lint jobs, contract checks, and TypeScript checks — passed.
- Did not run `make ci-full`, containerized jobs, or e2e.

## Commits

- `cf3cffdc6ce5f7753b70ae2c578c4382fbcfa9ba` — `feat(transport): publish identity-bearing runtime effects (nocx-zg3k3.14.1)`
- `5f6e32390fa956b05348fba4436bdb75aefa2230` — `feat(frontend): dispatch deduplicated runtime effects (nocx-zg3k3.14.1)`
