package transport

// app.about — what this build is (nocx-8bbp).
//
// The one method with no domain behind it: no store, no gate, no operation
// queue. Its answer is link-time constants and process facts, fixed for the
// life of the process, so there is nothing it can conflict with and nothing it
// can be refused for. It rides the plain lane for that reason and is
// deliberately answerable while every other domain is broken — the moment a
// person needs this page is the moment something else has gone wrong.

import (
	"context"

	"github.com/shady2k/nocx/internal/version"
)

// aboutInfo is the wire DTO. Hand-written beside the schema rather than a
// re-export of version.BuildInfo, per the contracts README: the domain type
// stays free to change shape without silently changing the wire.
type aboutInfo struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Date        string `json:"date"`
	Go          string `json:"go"`
	Wails       string `json:"wails"`
	Platform    string `json:"platform"`
	Development bool   `json:"development"`
}

// wireAbout fills the DTO, spelling out anything the descriptor does not carry.
//
// Every field is `minLength: 1` in the schema and that is the contract's point:
// the renderer never has to decide what to draw for a missing value. The
// descriptor a wired composition root supplies is already complete, so this
// matters for exactly one case — a server built without WithBuildInfo, which
// the dev-web harness and most of this package's own tests are. That case sends
// "unknown" six times rather than an empty row or a schema violation.
//
// The word is version.Unknown and not a literal here: one owner for what an
// absent value reads as.
func wireAbout(b version.BuildInfo) aboutInfo {
	return aboutInfo{
		Version:     version.OrUnknown(b.Version),
		Commit:      version.OrUnknown(b.Commit),
		Date:        version.OrUnknown(b.Date),
		Go:          version.OrUnknown(b.Go),
		Wails:       version.OrUnknown(b.Wails),
		Platform:    version.OrUnknown(b.Platform),
		Development: b.Development,
	}
}

// aboutHandlers answers app.about from the descriptor it was given.
//
// INJECTED, NOT READ. The build metadata is package state in internal/version,
// and reaching for it here would put a second reader of that state inside the
// transport — which is both the DI rule (AGENTS.md: depend on abstractions,
// wire at one composition root) and the only reason a test can assert what this
// method sends for a build nobody can produce on demand.
type aboutHandlers struct {
	build version.BuildInfo
	r     Responder
}

func (h aboutHandlers) handleAbout(_ context.Context, req jsonrpcRequest) {
	_ = h.r.TryResult(req.ID, mustMarshal(wireAbout(h.build)))
}

// aboutSpecs declares the method. Its own group of one, so the thing it does
// not have — a submission queue, a gate, a wired flag — is visible rather than
// inferred from a neighbour.
func (s *WSServer) aboutSpecs() []methodSpec {
	return []methodSpec{
		regResponder(s.lane, "app.about", noParams(), func(r Responder) handlerFunc {
			h := aboutHandlers{build: s.build, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAbout(ctx, req) }
		}),
	}
}
