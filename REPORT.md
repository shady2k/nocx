# Report — nocx-zg3k3.3.2 helper effect carrier

## Wired

The helper now carries each runtime effect as its own `TypeSessionEffect` frame, separate from `TypeScreenFrame`. The frame preserves the session and subscriber ids, runtime generation, effect id, kind, and body. The helper session drain sends each effect identity once per subscriber drain. The client delivers it only to the matching attachment. Unknown kinds and malformed frames are refused. Full screen snapshots remain effect-free.

This is the helper-carrier slice only. Effects are not yet forwarded by the app to the WebSocket client, no `contracts/` schema or generated frontend type was added, and the renderer handlers, permission flow, and replay/reconnect deduplication are not wired. Those are required follow-up work; this commit does not complete the bead's end-to-end acceptance. No schema was added because this commit only changes the helper's binary frame protocol.

## Tests and red-first evidence

Added `TestEffectFrameRoundTripsIdentityAndBody`, `TestEffectFrameRefusesUnknownKind`, `TestEffectFrameRejectsShortHeader`, `TestTheEffectFrameTypeIsInTheClosedSet`, `TestASubscriberReceivesAnIdentityBearingEffect`, and `TestAnEffectFrameReachesItsNamedAttachment`.

The first test attempt was blocked because the pinned libghostty-vt archive was missing. After `make vt-archives`, I temporarily removed the new codec implementation and reran the new codec tests; they failed on the missing `EffectFrame`/codec symbols. Restoring the implementation made them pass.

## Checks

- `make vt-archives` — passed.
- `gofmt` on all touched Go files — passed.
- `go test ./internal/helper/session ./internal/helper/proto ./internal/helper/host ./internal/helper/client -count=1` — passed.
- `go vet -tags gtk3 ./internal/sessionruntime/` — passed.
- `go test -tags gtk3 -count=1 ./internal/sessionruntime/` — passed.
- `go vet ./internal/helper/session ./internal/helper/proto ./internal/helper/host ./internal/helper/client` — passed.
- `git diff --check` — passed.

## Commit

Helper carrier commit: `5932cbf8`. The app/WebSocket/renderer work is not included.
