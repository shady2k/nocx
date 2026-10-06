package session_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// The helper receives the agent set the CALLER read (nocx-t5e7d), and it
// receives it as a LIST rather than as a profile directory: this daemon is
// built without the release tag, so a directory it resolved itself would be the
// development one under a release app. Asserted at the wire's own seam, so
// "the caller filled it" and "the daemon carried it" are one test rather than
// two halves that agree only by inspection.
func TestSpawnCarriesTheAgentSetTheCallerRead(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})

	given := []string{"claude", "myagent"}
	set := map[string]bool{}
	for _, name := range given {
		set[name] = true
	}
	res := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{
		Cols: 80, Rows: 24, Agents: set,
	})
	if res.Entry.Session.Session == "" {
		t.Fatalf("the spawn answered no session: %+v", res)
	}
	if len(spawner.reqs) != 1 {
		t.Fatalf("the spawner saw %d requests, want one", len(spawner.reqs))
	}
	got := spawner.reqs[0].Agents
	if len(got) != len(given) {
		t.Fatalf("the spawn request carried %v, want the caller's own set %v", got, given)
	}
	for i := range given {
		if got[i] != given[i] {
			t.Fatalf("the spawn request carried %v, want the caller's own set in a stable order %v", got, given)
		}
	}
	// And a caller that has no record to read is not a caller that offers
	// nothing: the request says so by saying nothing, and the local spawner
	// then renders this BUILD's own set.
	empty := &fakeSpawner{}
	svc2 := newService(t, newSink(), empty, session.Limits{})
	spawnOne(t, svc2)
	if len(empty.reqs) != 1 || len(empty.reqs[0].Agents) != 0 {
		t.Fatalf("a spawn that named no agents carried %v", empty.reqs)
	}
}
