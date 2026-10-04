# Report — nocx-hk9vm

The one-shot write contract test now waits until the fixture shell's sentinel-bearing prompt appears in the snapshot. The fixture emits initial output, pauses, then starts an interactive shell with the sentinel as its prompt. The timeout is only a failure bound. The 5 ms snapshot-pair success condition is gone.

## TDD evidence

With the old two-matching-snapshot wait temporarily substituted, `TestTheOneShotWritePathConformsToItsContractOverTheWire` failed in 0.03 s: `snapshot returned before the shell printed its startup sentinel`. Restoring the sentinel wait made the package pass.

## Checks

- `gofmt` — passed.
- `go vet -tags gtk3 ./internal/helper/client/` — passed.
- `go test -tags gtk3 -count=1 ./internal/helper/client/` — passed (`ok`, 5.738 s).
- Pre-commit static checks — passed (gofumpt, golangci-lint, ratchets, formatting, ESLint, backlog link check).
- The first Go checks could not find `ghostty/vt.h`; `make vt-archives` fetched and verified the pinned headers and archives. No repository source files were changed by that setup.

## Commit and handoff

- `de8dbc0f` — `test(helper): wait for shell startup sentinel (nocx-hk9vm)`
- Pushed to `origin/w/hk9vm-state-wait`.
- No pull request opened.
- Did not run `make ci-full`, containerized jobs or e2e, as directed.
