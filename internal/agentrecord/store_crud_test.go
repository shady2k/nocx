package agentrecord

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListReadsBuiltinsAndUserRecordsFromTheLiveStore(t *testing.T) {
	s, newErr := New(t.TempDir())
	if newErr != nil {
		t.Fatal(newErr)
	}
	if err := s.Save("custom", Document{DisplayName: "Custom", Command: "agent", Args: []string{"--one"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("claude", Document{DisplayName: "Claude edited", Command: "/opt/claude", Args: []string{"--fast"}}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Entry, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	if got := byID["claude"]; got.State != StateUser || !got.Record.Builtin || got.Record.Command != "/opt/claude" {
		t.Fatalf("edited built-in = %+v", got)
	}
	if got := byID["custom"]; got.State != StateUser || got.Record.Builtin || got.Record.Command != "agent" {
		t.Fatalf("custom = %+v", got)
	}
}

func TestSaveValidatesAndTheNextReadSeesTheWholeDocument(t *testing.T) {
	s, newErr := New(t.TempDir())
	if newErr != nil {
		t.Fatal(newErr)
	}
	if err := s.Save("custom", Document{Command: "agent", Args: []string{"--task", "{WORKSPACE_NAME}"}, Env: []string{"MODE=fast"}}); err != nil {
		t.Fatal(err)
	}
	entry, ok := s.Entry("custom")
	if !ok || entry.State != StateUser || entry.Record.Version != documentVersion || !reflect.DeepEqual(entry.Record.Args, []string{"--task", "{WORKSPACE_NAME}"}) || !reflect.DeepEqual(entry.Record.Env, []string{"MODE=fast"}) {
		t.Fatalf("saved record = %+v, known=%v", entry, ok)
	}
	if err := s.Save("custom", Document{Command: "  "}); err == nil {
		t.Fatal("Save accepted an empty command")
	}
	if _, ok := s.Entry("custom"); !ok {
		t.Fatal("invalid save replaced the valid record")
	}
	if err := s.Save("../escape", Document{Command: "agent"}); err == nil {
		t.Fatal("Save accepted a path traversal id")
	}
}

func TestRemoveDeletesCustomRecordAndRefusesBuiltins(t *testing.T) {
	s, newErr := New(t.TempDir())
	if newErr != nil {
		t.Fatal(newErr)
	}
	if err := s.Save("custom", Document{Command: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("claude"); err == nil {
		t.Fatal("Remove accepted a shipped agent")
	}
	if err := s.Remove("custom"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Entry("custom"); ok {
		t.Fatal("removed custom agent is still listed")
	}
	if err := s.Remove("custom"); err != nil {
		t.Fatalf("removing absent custom record: %v", err)
	}
}

func TestListReportsUnreadableDirectoryAndDocument(t *testing.T) {
	root := t.TempDir()
	s, newErr := New(root)
	if newErr != nil {
		t.Fatal(newErr)
	}
	if err := s.Save("custom", Document{Command: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, "custom.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == "custom" {
			found = row.State == StateUnreadable && row.Problem != ""
		}
	}
	if !found {
		t.Fatalf("List omitted unreadable record: %+v", rows)
	}
}

func TestSaveSurfacesFilesystemWriteFailure(t *testing.T) {
	root := t.TempDir()
	s, newErr := New(root)
	if newErr != nil {
		t.Fatal(newErr)
	}
	// Replace the document directory with a regular file so the atomic writer
	// cannot create its temporary document there.
	if err := os.RemoveAll(filepath.Join(root, DirName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("custom", Document{Command: "agent"}); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save error = %v, want the actual write failure", err)
	}
}

func TestUnreadableBuiltinStillCarriesBuiltinIdentityForSettingsGuards(t *testing.T) {
	root := t.TempDir()
	s, newErr := New(root)
	if newErr != nil {
		t.Fatal(newErr)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, "claude.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, ok := s.Entry("claude")
	if !ok || entry.State != StateUnreadable || !entry.Builtin {
		t.Fatalf("unreadable built-in = %+v, known=%v", entry, ok)
	}
	if entry.Record.Command != "" || entry.Record.ID != "" || entry.Record.Builtin {
		t.Fatalf("unreadable launch record leaked: %+v", entry.Record)
	}
	if err := s.Remove("claude"); err == nil {
		t.Fatal("unreadable built-in was removable")
	}
}
