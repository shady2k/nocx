package agenttools

import (
	"os"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestSessionToolsReplaceTheFinishedVersusLiveChoice(t *testing.T) {
	reg, err := Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got := toolNames(reg.All())
	want := []string{"files.read", "session.list", "session.read", "run", "git.status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assembled tools = %v, want %v", got, want)
	}
	if _, ok := reg.Lookup("readScreen"); ok {
		t.Fatal("readScreen remains exposed after session tool merge")
	}
	if _, ok := reg.Lookup("blocks.list"); ok {
		t.Fatal("blocks.list remains exposed after session tool merge")
	}
	if _, ok := reg.Lookup("blocks.read"); ok {
		t.Fatal("blocks.read remains exposed after session tool merge")
	}
	read, ok := reg.Lookup("session.read")
	if !ok {
		t.Fatal("session.read is not registered")
	}
	if read.Executes != Dynamic {
		t.Fatalf("session.read executes as %q, want dynamic dispatch", read.Executes)
	}
	if read.Effect != content.EffectObserve || !reflect.DeepEqual(read.Resources, []content.ResourceKind{content.ResourceSession}) {
		t.Fatalf("session.read classification = effect %q resources %v", read.Effect, read.Resources)
	}
}
