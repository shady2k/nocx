# ws-auth: WebSocket authentication for bead nocx-hl3

## What changed

### Go backend — `internal/transport/ws.go`

- **`WSServer` struct**: Added `token`, `tokenSource`, `origins`, `listenAddr` fields for auth, plus `io` import.
- **`NewWSServer`**: Default `origins = LoopbackOriginPolicy{}` — the development policy.
- **`Start`**: Calls `mintToken()` (fails closed on entropy error), uses `s.listenAddr` (default `127.0.0.1:0`), and sets `upgrader.Subprotocols` so the server echoes the token on upgrade (RFC 6455).
- **`handleSession`**: Calls `s.authorize(w, r)` before the upgrade. Rejected requests return before a socket is created.

### Go auth logic — `internal/transport/ws_auth.go`

Two controls, both rejecting **before** the upgrade:

1. **Capability token** (32 bytes `crypto/rand`, unpadded base64url). Carried in `Sec-WebSocket-Protocol`. Constant-time comparison via `subtle.ConstantTimeCompare`. Minted once per `Start`.
2. **Origin/Host policy** (defence-in-depth). Two implementations:
   - `LoopbackOriginPolicy` — accepts `localhost`, `127.0.0.1`, absent Origin. Used in development.
   - `PinnedOriginPolicy` — exact allowlist. **Empty list denies everything** (no silent hole).

### Test fixes — `internal/transport/ws_auth_test.go`

- **`dialWith`**: No longer sets `u.Host` on the dial URL (which broke DNS). Instead sets `Host` header so the request reaches the real listener with a forged Host.
- **`TestUpgradeRejectedForWrongHost`**: Removed `_ = port` (now used), added `if resp == nil` assertion to prove the request reached the handler.
- **`TestStartFailsClosedOnEntropyFailure`**: Fixed `ws.Close()` → `ws.Stop(ctx)`, replaced undefined `log.NewNop()` with `slog.New(slog.NewTextHandler(io.Discard, nil))`.
- Added `io` and `log/slog` imports.

### Test fix — `internal/transport/ws_test.go`

- **`connectWS`**: Changed from `websocket.DefaultDialer.Dial` to a custom dialer that offers `tokenProtocol(ws.Token())` as subprotocol. Existing transport tests now pass through auth.

### Go plumbing

- **`internal/app/app.go`**: Added `WSToken()` method wrapping `Transport.Token()`.
- **`main.go`**: Added `GetWSToken()` bound Go method on `WailsApp`.
- **`cmd/devharness/main.go`**: Now emits `WSTOKEN=...` alongside `WSPORT=...`.

### Frontend — `frontend/src/`

- **`main.ts`**: Imports `GetWSToken`, fetches it alongside `GetWSPort`, passes to `WSClient.connect`.
- **`ipc.ts`**: `WSClient.connect` accepts host first (preserving existing callers), then token. `_connectInternal` offers `nocx.token.<token>` as the WebSocket subprotocol.
- **`wailsjs/go/main/WailsApp.{js,d.ts}`**: Regenerated, includes `GetWSToken`.

### E2E — `e2e/`

- **`harness.ts`**: Stubs `GetWSToken` from `NOCX_WS_TOKEN` env var. **Fail-fast**: throws if `NOCX_WS_PORT` is set but `NOCX_WS_TOKEN` is absent.
- **`auth.spec.ts`**: New test asserting unauthenticated WebSocket fails (connection error without upgrade).

### Files touched

```
M internal/transport/ws.go           (struct fields, Start, handleSession, imports)
M internal/transport/ws_auth_test.go  (dialWith, WrongHost test, entropy test, imports)
M internal/transport/ws_test.go       (connectWS token offer)
M internal/app/app.go                 (WSToken method)
M main.go                             (GetWSToken method)
M cmd/devharness/main.go              (WSTOKEN emission)
M frontend/src/main.ts               (GetWSToken import + pass to client)
M frontend/src/ipc.ts                 (connect signature, subprotocol)
M frontend/wailsjs/go/main/WailsApp.d.ts  (regenerated)
M frontend/wailsjs/go/main/WailsApp.js    (regenerated)
M e2e/harness.ts                      (GetWSToken stub, fail-fast)
A e2e/auth.spec.ts                    (unauthenticated rejection test)
```

## Test results

All transport and app tests pass with `-race`:

```
$ go test -race ./internal/transport/... ./internal/app/...
ok  github.com/shady2k/nocx/internal/transport  16.703s
ok  github.com/shady2k/nocx/internal/app         1.800s
```

Specific auth tests and their pass/fail:

| Test                                   | Status |
| -------------------------------------- | ------ |
| `TestUpgradeRejectedWithoutToken`      | PASS   |
| `TestUpgradeRejectedWithWrongToken`    | PASS   |
| `TestUpgradeSucceedsWithCorrectToken`  | PASS   |
| `TestTokenIsFreshPerLaunch`            | PASS   |
| `TestStartFailsClosedOnEntropyFailure` | PASS   |
| `TestUpgradeRejectedForHostileOrigin`  | PASS   |
| `TestUpgradeRejectedForWrongHost`      | PASS   |

## How the Host test was fixed

The original `dialWith` set `u.Host = host`, making the gorilla dialer resolve
`attacker.example:PORT` — which fails at DNS before ever reaching our server.
The test passed because `err != nil` was true, but for the wrong reason.

Fix: `dialWith` now always dials `wsURL(ws)` (the real listener address) and
puts the `host` parameter into the HTTP `Host` header via `hdr.Set("Host", ...)`.
The dial resolves correctly, the request reaches the handler, and the server's
`LoopbackOriginPolicy.Allow` rejects `attacker.example` as non-loopback.

An additional `if resp == nil { t.Fatalf(...) }` assertion was added to prove
the request reached the handler.

## What remains

1. **Production Origin capture.** The `PinnedOriginPolicy` allowlist is empty
   (fail-closed). The true `wails://wails/` Origin must be captured from CI's
   `macos-latest` runner, which is the only place the shipped app actually
   runs. Until then, production builds will not authenticate — set
   `WithOriginPolicy(LoopbackOriginPolicy{})` for development.
2. **e2e Playwright suite not run.** The suite needs a display and working
   EGL, and 13 pre-existing failures are tracked in `nocx-bw2`. The auth spec
   is written but unverified against the running app.
