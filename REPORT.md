# Recovery report — nocx-zg3k3.14.2

## Recovered progress

The previous session had added the helper-side interactive intent route, the transport handler, its result/parameter schemas, the OpenRPC registration, generated TypeScript, and tests. It also updated the helper intent owner to bypass screen-target checks only for interactive input while keeping access-epoch checks at receipt and commit. Recovery found one compile error in the queued-revocation test: it called `writtenPayloads` on `blockingProcess`, which has no such method. I changed that assertion to inspect the embedded scripted process write channel and kept the test focused on proving the revoked intent wrote no bytes.

## Finished

`session.intent` is registered on the JSON-RPC control plane and routed only from the connection that owns the pane. `SessionHandle.intent` exposes the typed control-plane request to the pane renderer and returns the generated result contract. The handler forwards structured key/text/paste payloads to the helper that owns the pane; the coordinator does not encode or send PTY bytes. The helper uses the existing runtime encoder, including current terminal modes, and refuses stale pane access epochs as `refused/access_revoked` before any bytes are written. The helper protocol version is 16 for the added interactive discriminator.

## Red-first evidence and checks

- Red-first refusal probe: I temporarily disabled both interactive epoch checks (at receipt and commit), then ran `go test -tags gtk3 -count=1 -run '^TestInteractiveIntentRefusesRevokedAccessEpochWithoutWriting$' ./internal/helper/session`. It failed as expected: stale input was `executed` with 5 bytes instead of `refused/access_revoked`. I restored both checks. The test then passed.
- Program-mode gate: `go test -tags gtk3 -count=1 -run '^TestInteractiveArrowIntentFollowsProgramCursorMode$' ./internal/helper/session` passed. It asserts application-cursor and normal arrow sequences after DECCKM is enabled and disabled.
- Ordinary Enter-through: `go test -tags gtk3 -count=1 -run '^TestInteractiveIntentEncodesThroughRuntime$' ./internal/helper/session` passed; Enter is encoded to CR and one byte is written.
- Interactive queued revocation: `go test -tags gtk3 -count=1 -run '^TestInteractiveIntentRevocationWinsWhileQueued$' ./internal/helper/session` passed.
- Wire-path tests: `go test -tags gtk3 -count=1 -run 'TestSessionIntent' -v ./internal/transport` passed. They check forwarding, refusal on the real WebSocket result, connection ownership, and DTO contract conformance.
- Frontend control-plane call: `npx vitest run src/ipc.test.ts` passed (98 tests), including the new structured-intent request/result test. `npx tsc --noEmit -p tsconfig.json` and `npm run lint` from `frontend/` passed.
- `go test -tags gtk3 -count=1 ./internal/app` passed (`117.000s`).
- `go test -tags gtk3 -count=1 ./internal/helper/proto ./internal/helper/client` passed.
- `go vet -tags gtk3 ./internal/helper/session ./internal/transport ./internal/app` passed.
- `npm run contracts:check` from `frontend/` passed.
- `gofmt` ran on every touched Go file; `git diff --check` passed.

I did not run `make ci-full`, containerized tests, or e2e, as instructed. A combined full test run for helper/session, transport, and app was stopped after it continued running for about two minutes with transport consuming a CPU core and no output; the targeted session/transport tests and full app test above passed. A separate full helper/session test was already running in another session, so I did not duplicate it. The pre-commit dead-export ratchet also confirmed the generated result type is consumed by the typed `SessionHandle.intent` / `WSClient.sessionIntent` path.

## Commit

`25d74249` — `feat(transport): route pane input through session intent (nocx-zg3k3.14.2)`.
