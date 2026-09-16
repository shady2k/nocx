// Package failing is a fixture for internal/log/logtest's own tests, not a
// production package: it is under testdata/, which `go build`/`go vet`/`go
// test ./...` at the repo root skip by convention, so a test in here that
// deliberately fails never breaks the real suite. logtest's own test file
// runs it as a child `go test` process and reads its output.
package failing

import (
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

func TestDeliberatelyFails(t *testing.T) {
	ctx, lg := logtest.New(t)
	ctx, _ = log.StartTrace(ctx, log.DeterministicTraceID("logtest-fixture"))
	ctx = log.WithRequestID(ctx, "req-1")
	lg = log.From(ctx)

	lg.Debug("about to fail", "step", 1)
	lg.Warn("something looked wrong", "code", 42)
	t.Fatal("deliberate failure for logtest's own test")
}

func TestPasses(t *testing.T) {
	_, lg := logtest.New(t)
	lg.Debug("quiet success")
}
