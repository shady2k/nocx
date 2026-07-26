# W5 — authenticate the local WebSocket (bead nocx-hl3, PR11-T3)

You are a worker in an Orca wave. The coordinator owns the branch, the commits and the
issue tracker. Work in `/home/dev/orca/workspaces/nocx/pr-11-boundary` (branch
`pr-11-boundary`).

## The hole

`internal/transport/ws.go` today:

```go
ws.go:88   CheckOrigin: func(r *http.Request) bool { return true }
ws.go:102  net.Listen("tcp", "127.0.0.1:0")
```

Behind `/session`, the `open` method creates a PTY. There is no authentication of any
kind and every Origin is accepted. The random port is friction for an attacker, not
authorization — any page in any browser on this machine can scan loopback, connect, and
get a shell. This is bead `nocx-x4u`, and `nocx-hl3` is the fix.

Read `bd show nocx-hl3` for the full acceptance criteria before you start. The design
below is already decided; do not redesign it, but do push back if you find it wrong.

## Design (decided, with the reasons — keep them)

**Token in `Sec-WebSocket-Protocol`.** The browser WebSocket API cannot set an
`Authorization` header; a query parameter leaks into URLs, proxy logs and devtools; a
first-frame handshake authenticates _after_ the upgrade, which is too late because the
socket already exists. 32 bytes from `crypto/rand`, unpadded base64url (`=`, `+` and `/`
are not valid in a subprotocol name), constant-time compare, and the selected subprotocol
echoed on upgrade — RFC 6455 requires the echo and a browser aborts without it.

**Fail closed.** If the entropy source errors, `Start` must fail, not serve an empty
token. An empty token would compare equal to an empty client offer.

**Origin AND Host, policy injected per runtime mode.** Origin is defence in depth: a page
cannot forge it, so a leaked token still does not let a foreign page drive the socket.
Host is what closes DNS rebinding — the request reaches our listener either way, and only
the Host header reveals that the client resolved an attacker-controlled name to loopback.

Two policies:

- **Loopback (development, and the default).** Must accept `http(s)://localhost:*` and
  `http://127.0.0.1:*`, and must accept an **absent** Origin — browsers always send one on
  a WebSocket handshake, so absence means a non-browser caller, which still has to present
  the token. This policy is not a nicety: the project's verification loop is a browser on
  a Mac reaching this VM through forwarded ports, so the real Origin there is
  `http://localhost:<forwarded port>`. If this policy is wrong, the first thing the new
  check breaks is our own ability to test anything.
- **Pinned (production).** An exact allowlist. **An empty allowlist must deny everything.**
  Treating "not configured yet" as "allow anything" would silently reinstate the hole.

**Do not guess the production Origin.** `wails://wails/` with a trailing slash would reject
the legitimate client, and the failure is indistinguishable from a token bug — both reject
before the upgrade. The real value will be captured from CI's `macos-latest` runner, which
is the only place the shipped app actually runs; nobody's laptop has this branch. Leave the
pinned list empty and fail closed until then, and say so in a comment.

## What the coordinator already wrote — treat it as a draft, not as truth

Two files exist:

- `internal/transport/ws_auth_test.go` — the failing tests. These encode the acceptance
  criteria and are the contract. **Do not weaken a test to make it pass.**
- `internal/transport/ws_auth.go` — a draft implementation. It has **never compiled**: it
  references `s.tokenSource`, `s.origins`, `s.listenAddr` and `s.token`, none of which
  exist on the `WSServer` struct yet. Correct it freely; it is a starting point, not a
  specification.

One test is known to be broken and you must fix its mechanism: `TestUpgradeRejectedForWrongHost`
sets `u.Host` on the dial URL, so the dialer would try to resolve `attacker.example` and
fail at DNS without ever reaching our server — the test would pass for the wrong reason.
gorilla's `Dialer` honours a `Host` entry in the request header; dial the real listener
address and override the header instead. Verify the request actually reaches the handler.

## The work

1. Add the fields and options to `WSServer`, mint the token in `Start`, run the
   authorization before `upgrader.Upgrade`, and echo the selected subprotocol.
2. `Token()` accessor, valid after `Start`.
3. `WithListenAddr` — the default stays `127.0.0.1:0`, deliberately: loopback keeps the
   PTY off the network, port 0 avoids a predictable port. The option exists so the
   port-forwarded dev loop can pin `127.0.0.1:9876`, which is the port
   `frontend/src/main.ts` falls back to when `GetWSPort()` throws (no Wails runtime).
4. **Existing transport tests will break** — `connectWS` in `ws_test.go` dials without a
   token. Update the helper to offer the token. Do not add a bypass.
5. Expose the token to the frontend: a bound Go method alongside `GetWSPort`, and
   **regenerate `frontend/wailsjs/go/main/WailsApp.{js,d.ts}`** — without that, `main.ts`
   will not type-check.
6. Frontend: `main.ts` / `ipc.ts` must fetch the token and offer it as the subprotocol.
7. `e2e/harness.ts` currently stubs only `GetWSPort` (lines 13-26). It needs real token
   plumbing — **no bypass, no "skip auth in tests" flag**. Add a case asserting that an
   unauthenticated connection fails.

## Verification

TDD, per `AGENTS.md`: the tests are already red. Make them green without changing what they
assert.

- `go test -race ./internal/transport/...` and `./internal/app/...`
- `gofumpt -l .` and `golangci-lint run` on what you touched.
- Do **not** run the Playwright suite. It needs a display and a working EGL, and there is a
  much cheaper headless path if you ever do need it — see
  `.internal/reports/e2e-local-csp-round2.md` and `cmd/devharness`. Ask the coordinator
  before spending time there; 13 e2e failures predate this branch (`nocx-bw2`) and are not
  yours.

## Ground rules

- No commits, no pushes, no branches. No `git stash`.
- Do not touch beads / `bd`. The coordinator owns the issue tracker.
- Do not weaken or bypass a security control to make something pass. If a criterion cannot
  be met, escalate and say why — that is a valid outcome, quietly relaxing it is not.
- Report numbers, not adjectives: tests before and after, and every file touched.
- State plainly anything you could **not** verify. In particular, say explicitly that the
  production Origin is uncaptured — do not let it read as done.

## When done

Write `.internal/reports/ws-auth.md` covering: what you changed, the test results, how you
fixed the Host test, and what remains (the production Origin capture). Then send
`worker_done` from your own terminal using the `taskId` and `dispatchId` from the dispatch
preamble.
