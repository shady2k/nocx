# Report — nocx-xn63t.6.21

## Mechanism

`internal/helper/session/hostSession.enqueueRowEmission` charged every batch's full `emissionBytes` to the wire FIFO, then charged the same `em.rows` backing slices again to the resend window. The FIFO and `retainedRowSpan` hold references to the same cells, not separate cell copies. At wide geometries this doubled accounting exhausted the default 20 MiB budget during the flood. The helper then queued its incomplete marker and entered `rowsDropping`; an interval end arriving before the marker was delivered was rejected by the `rowsDropping` branch, so the end never reached the coordinator. The overflow was the trigger; the end loss was its downstream effect, not an artifact-cap or frame-size failure.

On the pre-fix `-count=3` run, 200x50 reported no end marker after 135 and 138 frames. The helper overflow logs named rows 2461 and 2516. The brief also records the reproduced 120x40 failure at baseline.

## Fix

The row pool now charges shared cell data once to the resend window. While a nonempty row batch is queued, the FIFO is charged only for its separate emission record. Other emissions retain their full FIFO charge. Dequeue and shutdown-drain release the same FIFO-only amount. Accounting tests now assert shared backing data is charged once, and the wedged-sink test sizes its queue against the actual owners.

A helper resend test also stopped sampling the transient internal `resendDue` flag after attach; the pump can complete the replay before attach returns. It now asserts the user-visible condition directly: the replacement reader receives the unconfirmed rows.

## Evidence

- Red: `go test -tags gtk3 -count=3 -run TestTheThreeOutputBoundsHoldTogetherAtRealGeometry ./internal/transport/` failed before the fix, with the missing end and helper-buffer overflow described above.
- Green: the same command passed after the fix, three repetitions across 80x24, 120x40 and 200x50 (9 geometry runs, 0 failures).
- Green: one verbose acceptance run reported these flood metrics: 80x24 — 248 frames, 32 max rows/frame, 263,917 artifact bytes, 1,853 stored rows, 2,150 dropped; 120x40 — 187 frames, 32 max rows/frame, 264,104 bytes, 1,440 stored, 2,562 dropped; 200x50 — 264 frames, 32 max rows/frame, 265,313 bytes, 1,008 stored, 2,994 dropped. All three reached the paired burst and under-cap checks.
- Green: `go vet -tags gtk3 ./internal/transport/ ./internal/helper/session/`.
- Green: `go test -tags gtk3 -count=1 ./internal/transport/` and `go test -tags gtk3 -count=1 ./internal/helper/session/`.
- Not run: `make ci-full` and containerized/e2e suites, as requested.

## Commit

Pending.
