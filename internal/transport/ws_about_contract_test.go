package transport

// The app.about wire contract, from both ends (AGENTS.md rule 5): the DTO
// marshals to something the schema accepts, and the REAL result off the REAL
// socket satisfies it too. The second is the one that matters — a test that
// validates a payload it constructed itself proves the struct is well-formed,
// not that the server sends it. That is the exact defect this directory exists
// for: vault.status had never sent defaultProvider while both suites were green.

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/version"
)

func TestAppAbout_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "app.about.schema.json")

	cases := map[string]version.BuildInfo{
		"a stamped release": {
			Version:  "0.2.0",
			Commit:   "9f1c2b7d",
			Date:     "2026-08-20T09:41:00Z",
			Go:       "go1.25.0",
			Wails:    "v3.0.0-beta.9",
			Platform: "darwin/arm64",
		},
		"an unstamped development build": {
			Version:     "dev",
			Commit:      "none",
			Date:        "unknown",
			Go:          "go1.25.0",
			Wails:       "v3.0.0-beta.9",
			Platform:    "linux/amd64",
			Development: true,
		},
		// The case the comment on wireAbout makes a claim about, asserted
		// rather than trusted: a server built without WithBuildInfo. Every
		// field must still be a word, because `minLength: 1` is what lets the
		// renderer draw the row without deciding anything.
		"a server nobody told what build it is": {},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(wireAbout(build))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "app.about result ("+name+")")
		})
	}
}

func TestAppAbout_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "app.about.schema.json")
	build := version.BuildInfo{
		Version:  "1.2.3",
		Commit:   "cafebabe",
		Date:     "2026-08-20T09:41:00Z",
		Go:       "go1.25.0",
		Wails:    "v3.0.0-beta.9",
		Platform: "darwin/arm64",
	}
	ws, stop := newAboutWSServer(t, build)
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "app.about", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "app.about result")

	// AND IT IS THIS BUILD'S ANSWER, not merely a well-shaped one. A handler
	// that ignored its descriptor and sent six "unknown"s would satisfy the
	// schema perfectly, which is precisely how vault.status stayed green while
	// never sending the field the page read.
	var got aboutInfo
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := wireAbout(build)
	if got != want {
		t.Fatalf("app.about answered %+v, want the injected %+v", got, want)
	}
}

// The method answers with no domain wired at all. It has no store, no gate and
// no operation queue, and the moment a person opens the About page is the
// moment something else has gone wrong — so "answerable while everything else
// is broken" is the property, not an accident of this test's setup.
func TestAppAbout_AnswersWithNothingElseWired(t *testing.T) {
	ws, stop := newAboutWSServer(t, version.BuildInfo{})
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "app.about", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("app.about refused on a bare server: %+v", resp.Error)
	}
	var got aboutInfo
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != version.Unknown {
		t.Fatalf("version = %q, want %q on a server that was told nothing", got.Version, version.Unknown)
	}
}

func newAboutWSServer(t *testing.T, build version.BuildInfo) (*WSServer, func()) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger), WithBuildInfo(build))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}
