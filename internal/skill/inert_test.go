package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// countingDocStore is the real document store that also says how many times
// it was WRITTEN to. Discovery is a read, and the only way to assert that is
// to count the writes rather than to assume none happened.
type countingDocStore struct {
	inner  storage.DocumentStore
	writes int
}

func (c *countingDocStore) Read(name string, into any) (bool, error) { return c.inner.Read(name, into) }

func (c *countingDocStore) Write(name string, doc any) error {
	c.writes++
	return c.inner.Write(name, doc)
}

func (c *countingDocStore) Delete(name string) error { return c.inner.Delete(name) }

// installSkillFor puts a skill under the installed root and records the
// digest the way an install does, so the skill is `approved` and the only
// thing left keeping it out of the index is that nobody has turned it on.
func installSkillFor(t *testing.T, store *Store, configDir, name, body string) {
	t.Helper()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), name, "name: "+name+"\ndescription: theirs", body)
	if err := store.Approve(name); err != nil {
		t.Fatalf("record the installed digest for %q: %v", name, err)
	}
}

func TestAnInstalledSkillIsInertUntilThePersonTurnsItOn(t *testing.T) {
	configDir := t.TempDir()
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	installSkillFor(t, store, configDir, "deploy", "installed body")

	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	deploy := listed(t, result, "deploy")
	if deploy.Status != StatusApproved {
		t.Fatalf("status = %q, want approved: the bytes are the ones recorded at installation", deploy.Status)
	}
	if deploy.Enabled {
		t.Fatal("a freshly installed skill is enabled; it must arrive inert and wait for the person")
	}
	if _, offered := indexed(store.Index(), "deploy"); offered {
		t.Fatalf("Index() = %+v, want an installed skill nobody has turned on kept out", store.Index())
	}

	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, offered := indexed(store.Index(), "deploy"); !offered {
		t.Fatalf("Index() = %+v, want the skill the person turned on", store.Index())
	}
	if !listed(t, mustList(t, store), "deploy").Enabled {
		t.Fatal("the row still says off after the person turned it on")
	}

	// And back off again, because a switch that only travels one way is not a
	// switch. This is the paired success/refusal both directions of the
	// default need.
	if err := store.SetEnabled("deploy", false); err != nil {
		t.Fatalf("SetEnabled off: %v", err)
	}
	if _, offered := indexed(store.Index(), "deploy"); offered {
		t.Fatal("the skill is still offered after the person turned it off")
	}
}

// THE SWITCH DOES NOT ADOPT BYTES, and this is where that is pinned
// (nocx-0bsa4.3). The card is now where the person reads a skill, so it was
// worth asking again whether turning one on should record the digest of what
// they were looking at. It must not, and the reason is the case below: in the
// ordinary case there is nothing to record — the install already wrote the
// digest of exactly these bytes, and if a byte had moved since, the row and
// the card would both say `changed`. The ONLY case where recording on a
// toggle would change anything is that one, and there it would convert the
// cheapest control in the product into a silent adoption of an edit nobody
// read. Approve is the control that adopts bytes, it is drawn only when there
// are bytes to adopt, and this test is what stops the switch quietly becoming
// a second one.
func TestTurningTheSwitchDoesNotAdoptChangedBytes(t *testing.T) {
	configDir := t.TempDir()
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	installSkillFor(t, store, configDir, "deploy", "installed body")
	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	path := filepath.Join(configDir, "installed-skills", "deploy", "SKILL.md")
	original, err := os.ReadFile(path) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, original...), " edited by somebody\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := listed(t, mustList(t, store), "deploy").Status; got != StatusChanged {
		t.Fatalf("status = %q, want changed: this test is not exercising what it claims", got)
	}

	// The switch, both ways, which is the cheapest thing a person can do to a
	// skill and the likeliest thing they will do to one that has stopped
	// working.
	if err := store.SetEnabled("deploy", false); err != nil {
		t.Fatalf("SetEnabled off: %v", err)
	}
	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled on: %v", err)
	}

	if got := listed(t, mustList(t, store), "deploy").Status; got != StatusChanged {
		t.Fatalf("status = %q, want changed still: flipping the switch adopted bytes nobody read", got)
	}
	if _, offered := indexed(store.Index(), "deploy"); offered {
		t.Fatal("the changed skill is offered to the assistant again after a toggle")
	}

	// And Approve — the control that IS about the bytes — still ends the
	// state, so the paired success is here beside the refusal.
	if err := store.Approve("deploy"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := listed(t, mustList(t, store), "deploy").Status; got != StatusApproved {
		t.Fatalf("status = %q, want approved after the person adopted the bytes", got)
	}
}

func TestAuthoredAndManagedSkillsAreLiveOnArrival(t *testing.T) {
	configDir := t.TempDir()
	roots := installedRoots(t, configDir)
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "mine", "name: mine\ndescription: mine", "authored body")
	writeExistingSkill(t, filepath.Join(configDir, "managed-skills"), "drafted", "name: drafted\ndescription: drafted", "managed body")
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	if err := store.Approve("drafted"); err != nil {
		t.Fatalf("record the managed digest: %v", err)
	}

	result := mustList(t, store)
	for _, name := range []string{"mine", "drafted"} {
		if !listed(t, result, name).Enabled {
			t.Fatalf("skill %q arrived disabled; only the installed root arrives inert", name)
		}
		if _, offered := indexed(store.Index(), name); !offered {
			t.Fatalf("skill %q is not offered; the person wrote it or asked for it", name)
		}
	}
}

func TestChangingAnEnabledInstalledSkillTakesItOutOfTheIndexAndRestoringItPutsItBack(t *testing.T) {
	configDir := t.TempDir()
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	installSkillFor(t, store, configDir, "deploy", "installed body")
	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, offered := indexed(store.Index(), "deploy"); !offered {
		t.Fatal("this test is not exercising what it claims: the skill was never in the index")
	}

	path := filepath.Join(configDir, "installed-skills", "deploy", "SKILL.md")
	original, err := os.ReadFile(path) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, original...), " edited\n"...), 0o600); err != nil {
		t.Fatal(err)
	}

	row := listed(t, mustList(t, store), "deploy")
	if row.Status != StatusChanged {
		t.Fatalf("status = %q, want changed once a byte under the skill moved", row.Status)
	}
	if !row.Enabled {
		t.Fatal("the switch was turned off by a byte moving; the effective state is computed, not written")
	}
	if _, offered := indexed(store.Index(), "deploy"); offered {
		t.Fatal("a changed skill is still offered to the assistant")
	}

	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := listed(t, mustList(t, store), "deploy").Status; got != StatusApproved {
		t.Fatalf("status = %q, want approved once the bytes came back", got)
	}
	if _, offered := indexed(store.Index(), "deploy"); !offered {
		t.Fatal("restoring the bytes byte-for-byte did not put the skill back; the same bytes carry the same review")
	}
}

func TestDiscoveryNeverWritesTheDocument(t *testing.T) {
	configDir := t.TempDir()
	roots := installedRoots(t, configDir)
	docs := &countingDocStore{inner: storage.NewDocumentStore(configDir)}
	store := NewStore(OSFileSystem{}, roots, docs)
	installSkillFor(t, store, configDir, "deploy", "installed body")
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "mine", "name: mine\ndescription: mine", "authored body")
	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	// Everything above is a write and is allowed to be. From here on nothing
	// may be.
	before := docs.writes
	path := filepath.Join(configDir, "installed-skills", "deploy", "SKILL.md")
	original, err := os.ReadFile(path) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		mustList(t, store)
		store.Index()
		if _, readErr := store.Read("deploy", ""); readErr != nil {
			t.Fatalf("Read: %v", readErr)
		}
	}
	// The changed case too, because that is the state a writing implementation
	// would be tempted to record.
	if err := os.WriteFile(path, append(append([]byte{}, original...), " edited\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	mustList(t, store)
	store.Index()
	if docs.writes != before {
		t.Fatalf("discovery wrote the document %d times; listing the skills must be a read", docs.writes-before)
	}
}

// Removing a skill forgets that it was on, so a later install of the same
// name arrives inert like any other. Without this the switch is a row for a
// name that is not on disk, and the next install of that name reads somebody
// else's decision as its own review.
func TestRemovingAnInstalledSkillForgetsThatItWasTurnedOn(t *testing.T) {
	configDir := t.TempDir()
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	installSkillFor(t, store, configDir, "deploy", "installed body")
	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if err := store.Remove("deploy"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	installSkillFor(t, store, configDir, "deploy", "installed body")
	if listed(t, mustList(t, store), "deploy").Enabled {
		t.Fatal("the reinstalled skill is on because the removed one was; nobody has looked at these bytes")
	}
	if _, offered := indexed(store.Index(), "deploy"); offered {
		t.Fatal("the reinstalled skill reached the index without anybody turning it on")
	}
}
