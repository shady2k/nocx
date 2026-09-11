package agentdriver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

func TestAManifestThatCannotBeTrustedIsRefusedByName(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"not json", `{`, "decode manifest"},
		{"unknown field", `{"version":1,"agent":"claude","inventory":[],"entries":[],"extra":1}`, "unknown field"},
		{"wrong version", `{"version":2,"agent":"claude","inventory":[],"entries":[]}`, "version 2"},
		{"no agent", `{"version":1,"inventory":[],"entries":[]}`, "names no agent"},
		{"unknown state", `{"version":1,"agent":"claude","inventory":[],"entries":[{"capture":"claude-idle","atMs":11000,"state":"idle"}]}`, "not a driver state"},
		{"exited", `{"version":1,"agent":"claude","inventory":[],"entries":[{"capture":"claude-idle","atMs":11000,"state":"exited"}]}`, "fact about the process"},
		{"moment not in inventory", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[{"moment":"idle-120","capture":"claude-idle","atMs":11000,"state":"free_text"},{"moment":"nope","unverified":"x"}]}`, "not in the inventory"},
		{"inventory moment with no entry", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[]}`, `"idle-120" has no entry`},
		{"inventory moment twice", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[{"moment":"idle-120","capture":"claude-idle","atMs":11000,"state":"free_text"},{"moment":"idle-120","unverified":"x"}]}`, "want exactly one"},
		{"unverified with a recording", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[{"moment":"idle-120","unverified":"x","capture":"claude-idle"}]}`, "also names a recording"},
		{"recorded without a mark", `{"version":1,"agent":"claude","inventory":[],"entries":[{"capture":"claude-idle","state":"free_text"}]}`, "names no capture and mark"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			_, err := loadManifest(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadManifest error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		_, err := loadManifest(filepath.Join(t.TempDir(), "absent.json"))
		if err == nil || !strings.Contains(err.Error(), "read manifest") {
			t.Fatalf("loadManifest error = %v, want a read failure", err)
		}
	})
}

func TestAManifestThatDisagreesWithTheRuleFailsByName(t *testing.T) {
	dir := filepath.Dir(manifestPath)
	at := int64(11000)
	zero := 0
	cases := []struct {
		name  string
		dir   string
		entry manifestEntry
		want  string
	}{
		{"wrong state", dir, manifestEntry{Capture: "claude-idle", AtMs: &at, State: agentdriver.StateWorking}, `state "free_text", want "working"`},
		{"wrong branch", dir, manifestEntry{Capture: "claude-idle", AtMs: &at, State: agentdriver.StateFreeText, Branch: &zero}, "matched branch"},
		{"missing capture", dir, manifestEntry{Capture: "no-such-capture", AtMs: &at, State: agentdriver.StateFreeText}, "capture no-such-capture"},
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "broken.jsonl"), []byte("{\n"), 0o600); err != nil {
		t.Fatalf("write broken capture: %v", err)
	}
	cases = append(cases, struct {
		name  string
		dir   string
		entry manifestEntry
		want  string
	}{"unreadable capture", broken, manifestEntry{Capture: "broken", AtMs: &at, State: agentdriver.StateFreeText}, "capture broken"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := manifest{Version: 1, Agent: "claude", Entries: []manifestEntry{tc.entry}}
			errs := checkManifest(m, tc.dir, registry(t))
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("checkManifest = %v, want one error containing %q", errs, tc.want)
			}
		})
	}
	t.Run("a correct entry passes", func(t *testing.T) {
		m := manifest{Version: 1, Agent: "claude", Entries: []manifestEntry{{Capture: "claude-idle", AtMs: &at, State: agentdriver.StateFreeText}}}
		if errs := checkManifest(m, dir, registry(t)); len(errs) != 0 {
			t.Fatalf("checkManifest = %v, want none", errs)
		}
	})
}
