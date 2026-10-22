package shellintegration

// The generated agent wrappers (nocx-t5e7d, the owner's shell-bundle decision):
// a connected shell offers a wrapper per ENABLED agent of the record, and what
// travels to a host is the NAME and nothing else.
//
// The security half is the reason this file exists and is not a formality. The
// bundle is published to hosts nobody here controls, so an argument or an
// environment line that reached it would be a person's own configuration — a
// key, a path, a credential — copied onto somebody else's machine. The record
// is read LOCALLY; the host receives a list of names.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentrecord"
)

// recordStore writes a record set under a fresh app directory and opens it.
func recordStore(t *testing.T, docs map[string]string) *agentrecord.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, agentrecord.DirName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for id, body := range docs {
		p := filepath.Join(dir, agentrecord.DirName, id+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	s, err := agentrecord.New(dir)
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}
	return s
}

// THE SECURITY HALF. A person's own record — a key in an environment line and
// arguments nobody else has — changes the record nocx reads where nocx runs,
// and leaves the bundle byte-identical: the host is told the name, never the
// record.
func TestAPersonsRecordDoesNotReachTheBundle(t *testing.T) {
	shipped := recordStore(t, nil)
	edited := recordStore(t, map[string]string{
		"claude": `{
  "version": 1,
  "command": "/opt/my-claude",
  "args": ["--dangerously-skip-permissions", "--model", "secret-model-name"],
  "env": ["ANTHROPIC_API_KEY=sk-ant-not-a-real-key"],
  "resume": {"resumeCwdArgs": ["--continue"]}
}`,
	})

	// The record really did change locally, or the rest of this proves nothing.
	if len(edited.EnabledNames()) != len(shipped.EnabledNames()) {
		t.Fatalf("the edit changed the enabled set: %v vs %v", edited.EnabledNames(), shipped.EnabledNames())
	}

	plain := launchBundle(shipped.EnabledNames())
	personal := launchBundle(edited.EnabledNames())

	if plain.Version != personal.Version {
		t.Fatalf("version moved on an edit that changed no name: %q vs %q", plain.Version, personal.Version)
	}
	if plain.generation() != personal.generation() {
		t.Fatalf("generation moved on an edit that changed no name: %q vs %q", plain.generation(), personal.generation())
	}
	for _, a := range plain.Files {
		if string(a.Data) != string(fileOf(t, personal, a.Name)) {
			t.Fatalf("%s differs after an edit that changed no agent NAME, so the person's record reached the bundle", a.Name)
		}
	}
	for _, a := range personal.Files {
		for _, secret := range []string{
			"sk-ant-not-a-real-key", "ANTHROPIC_API_KEY",
			"--dangerously-skip-permissions", "secret-model-name", "/opt/my-claude",
		} {
			if strings.Contains(string(a.Data), secret) {
				t.Fatalf("%s carries %q, which is the person's own record travelling to a host", a.Name, secret)
			}
		}
	}
}

// Adding an agent a person owns changes what the shell offers and moves the
// bundle's identity, so a host already connected is published a new generation
// rather than keeping the old set — and what changed is the NAME list and
// nothing beside it.
func TestAnAddedAgentMovesTheGenerationAndOnlyTheNameList(t *testing.T) {
	shipped := recordStore(t, nil)
	added := recordStore(t, map[string]string{
		"myagent": `{"version": 1, "command": "my-agent", "args": ["--fast"], "env": ["MYAGENT_KEY=nope"]}`,
	})

	plain := launchBundle(shipped.EnabledNames())
	grown := launchBundle(added.EnabledNames())

	if plain.generation() == grown.generation() {
		t.Fatalf("the generation did not move when the enabled set did: %q", plain.generation())
	}
	if got := grown.Agents; !sameNames(got, []string{"claude", "myagent"}) {
		t.Fatalf("the bundle's agents = %v, want the shipped agent and the person's", got)
	}
	for _, f := range grown.Files {
		for _, secret := range []string{"MYAGENT_KEY", "--fast"} {
			if strings.Contains(string(f.Data), secret) {
				t.Fatalf("%s carries %q: an added agent's arguments and environment must not travel", f.Name, secret)
			}
		}
	}
}

// shippedBundle is what THIS BUILD publishes for its own agent set — the bundle
// every test that is not about the agent set itself means.
func shippedBundle() Bundle { return launchBundle(shippedAgentNames()) }

// fileOf is the named file's bytes, and it fails rather than answering empty:
// a missing file would make every assertion above pass vacuously.
func fileOf(t *testing.T, b Bundle, name string) []byte {
	t.Helper()
	f, ok := b.file(name)
	if !ok {
		t.Fatalf("the bundle carries no %s", name)
	}
	return f.Data
}

// A host already carrying this build's bundle is republished when the agent set
// changes, and NOT republished when nothing about it has — which is the whole
// of "an agent list that changes must be a different generation" (nocx-t5e7d).
// It is asserted at the publisher, because that is where the skip is decided.
func TestAChangedAgentSetIsPublishedAndTheSameOneIsNot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	fsys := NewOSFS()

	first, err := NewPublisher(testLogger(), fsys, root).Publish(launchBundle([]string{"claude"}))
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if !first.Published {
		t.Fatalf("the first publish reported %+v, want a publish", first)
	}

	same, err := NewPublisher(testLogger(), fsys, root).Publish(launchBundle([]string{"claude"}))
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if same.Published {
		t.Fatalf("the same build and the same agent set published again: %+v", same)
	}
	if same.Reason != "already-installed" {
		t.Fatalf("second publish reason = %q, want already-installed", same.Reason)
	}

	// One more enabled agent. Same build, same version — and the host must be
	// told, because the scripts it holds would wrap one agent and this machine
	// offers two.
	grown, err := NewPublisher(testLogger(), fsys, root).Publish(launchBundle([]string{"claude", "codex"}))
	if err != nil {
		t.Fatalf("publish with an added agent: %v", err)
	}
	if !grown.Published {
		t.Fatalf("an added agent did not republish: %+v", grown)
	}
	if grown.Generation == first.Generation {
		t.Fatalf("both sets published under generation %q", grown.Generation)
	}
	if grown.Version != first.Version {
		t.Fatalf("version moved with the agent set (%q -> %q); the version is the BUILD's, and the set is the generation's", first.Version, grown.Version)
	}
}
