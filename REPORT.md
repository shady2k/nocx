# nocx-2b0mq implementation report

## Design

Read `.internal/specs/2026-08-31-the-generation-daemon-lifecycle-design.md`, especially D2. EOF and last detach are connection events and do not stop the daemon. A daemon that never owns a session has one measured startup grace. The session admission/drain transition is atomic. A daemon that has owned a session drains when its last session process exits.

The design places generation lifetime locks and `probe-prunable` in D3/D7. They are not present in this tree and are outside this leaf. This change does not add a retirement lock system; the regression test probes that the endpoint is gone after last-session shutdown. The D3/D7 pointer is also recorded in the commit body.

## Slices and tests

1. Added a lifecycle gate with admission reservations, startup-deadline handling, active-process accounting, and a one-shot drain callback. The daemon composition root wires the callback to cancel `endpoint.Serve`, so it closes the listener and connections. Spawn and spawn-ssh reserve admission through session registration/failure. Process exit and explicit session close release active-process accounting exactly once.
2. Added deterministic state tests for the empty grace, admission-vs-deadline race, and last-session drain. The new tests were run red first: `go test -tags gtk3 -run TestDaemonLifecycle -count=1 ./internal/helper/session` failed because the lifecycle API was not yet defined. They passed after the gate was implemented. The race test was also run with `-race` and passed.
3. Extended real socket daemon tests. They assert EOF does not drain the daemon, last detach leaves the session process running and its output advancing, and the last session process exit drains the daemon, unlinks the endpoint, and makes a probe fail. An initial type mismatch in the new detach assertion failed to compile; the type was corrected and the targeted tests passed.
4. Added `TestMeasureDaemonLaunchToFirstSpawn`, an opt-in harness. It builds and starts the real helper 30 times per run in isolated homes, performs the authenticated hello, then requests a real shell spawn. It records every latency and calculates nearest-rank p99. Three runs measured p99 values **27.73 ms, 26.12 ms, 25.14 ms** (range **2.59 ms**). The production grace is **250 ms**, over 9x the worst observed p99. Reproduce with:

   ```bash
   RUN_DAEMON_LIFECYCLE_MEASUREMENT=1 go test -tags gtk3 -run TestMeasureDaemonLaunchToFirstSpawn -count=3 -v ./cmd/nocx-helper
   ```

   This calibration is for this VM; rerun it on a new machine before changing the bound.

## Checks

- `gofmt -l` on all changed Go files: clean.
- `go vet -tags gtk3 ./internal/helper/... ./cmd/nocx-helper/...`: passed.
- `go test -tags gtk3 -count=1 ./internal/helper/session ./internal/helper/endpoint ./cmd/nocx-helper`: passed.
- `go test -race -tags gtk3 -count=1 -run TestDaemonLifecycle ./internal/helper/session`: passed.
- Calibration harness (`count=3`, 90 real helper starts/spawns): passed; see p99 values above.
- Did not run `make ci-full`, containerized jobs, or e2e, as instructed.

## Commit

e2417c3d (implementation commit; this report is committed immediately after it).
