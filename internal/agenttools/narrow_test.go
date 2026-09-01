package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/hashline"
)

func skillFenceFixture(t *testing.T) (string, []string, string, string) {
	t.Helper()
	configDir := t.TempDir()
	skillRoots := []string{
		filepath.Join(configDir, "skills"),
		filepath.Join(configDir, "managed-skills"),
	}
	for _, root := range skillRoots {
		if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
			t.Fatalf("MkdirAll %s: %v", root, err)
		}
	}
	otherDir := filepath.Join(configDir, "other")
	if err := os.MkdirAll(otherDir, 0o750); err != nil {
		t.Fatalf("MkdirAll other: %v", err)
	}
	return configDir, skillRoots, filepath.Join(skillRoots[0], "deploy", "SKILL.md"), filepath.Join(otherDir, "ordinary.txt")
}

func configPathGrant(configDir string) content.Grant {
	return content.Grant{Scopes: []content.GrantScope{{Kind: content.ResourcePath, ID: configDir}}}
}

func TestNarrowFilesReadRefusesSkillRoot(t *testing.T) {
	configDir, skillRoots, skillPath, otherPath := skillFenceFixture(t)
	if err := os.WriteFile(skillPath, []byte("skill instructions\n"), 0o600); err != nil {
		t.Fatalf("WriteFile skill: %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("ordinary file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile ordinary: %v", err)
	}
	grant := configPathGrant(configDir)
	narrow := narrowFilesReadWithSkillRoots(skillRoots)
	if _, err := narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: skillPath}}, RunContext{}); err == nil || !strings.Contains(err.Error(), "skills.read") {
		t.Fatalf("skill read error = %v, want refusal naming skills.read", err)
	}
	capability, err := narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: otherPath}}, RunContext{})
	if err != nil {
		t.Fatalf("ordinary read narrow: %v", err)
	}
	reader, ok := capability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", capability)
	}
	if got, err := reader.Read(context.Background(), otherPath, 100); err != nil || got.Text != "ordinary file\n" {
		t.Fatalf("ordinary read = %+v, %v", got, err)
	}
}

func TestNarrowFilesEditRefusesSkillRoot(t *testing.T) {
	configDir, skillRoots, skillPath, otherPath := skillFenceFixture(t)
	if err := os.WriteFile(skillPath, []byte("skill instructions\n"), 0o600); err != nil {
		t.Fatalf("WriteFile skill: %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("ordinary file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile ordinary: %v", err)
	}
	grant := configPathGrant(configDir)
	narrow := narrowFilesEditWithSkillRoots(skillRoots)
	if _, err := narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: skillPath}}, RunContext{}); err == nil || !strings.Contains(err.Error(), "skills.update") {
		t.Fatalf("skill edit error = %v, want refusal naming skills.update", err)
	}
	capability, err := narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: otherPath}}, RunContext{})
	if err != nil {
		t.Fatalf("ordinary edit narrow: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	snapshot, err := hashline.Read(otherPath, 100)
	if err != nil {
		t.Fatalf("hashline.Read: %v", err)
	}
	if _, err := editor.Edit(context.Background(), otherPath, snapshot.Revision, "PUT 1.=1:\n+changed"); err != nil {
		t.Fatalf("ordinary edit: %v", err)
	}
}

func TestNarrowFilesCreateRefusesSkillRoot(t *testing.T) {
	configDir, skillRoots, skillPath, otherPath := skillFenceFixture(t)
	grant := configPathGrant(configDir)
	narrow := narrowFilesCreateWithSkillRoots(skillRoots)
	if _, err := narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: skillPath}}, RunContext{}); err == nil || !strings.Contains(err.Error(), "skills.create") {
		t.Fatalf("skill create error = %v, want refusal naming skills.create", err)
	}
	capability, err := narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: otherPath}}, RunContext{})
	if err != nil {
		t.Fatalf("ordinary create narrow: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	if _, err := editor.Create(context.Background(), otherPath, "ordinary file\n"); err != nil {
		t.Fatalf("ordinary create: %v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("ordinary create did not create %s: %v", otherPath, err)
	}
}

func TestNarrowSkillsWriteRetainsOnlyGrantedSkillNames(t *testing.T) {
	grant := content.Grant{Scopes: []content.GrantScope{{
		Kind: content.ResourceContent,
		ID:   "skill/deploy",
	}}}
	capability, err := narrowSkillsWrite(grant, []ResourceRef{
		{Kind: content.ResourceContent, ID: "skill/deploy"},
		{Kind: content.ResourceContent, ID: "skill/other"},
	}, RunContext{})
	if err != nil {
		t.Fatalf("narrowSkillsWrite: %v", err)
	}
	scope, ok := capability.(*SkillWriteScope)
	if !ok {
		t.Fatalf("capability = %T, want *SkillWriteScope", capability)
	}
	if !scope.Allows("deploy") {
		t.Fatal("granted skill was not retained")
	}
	if scope.Allows("other") {
		t.Fatal("ungranted skill escaped the narrowed capability")
	}
	if scope.Allows("skill/deploy") {
		t.Fatal("Allows expects a skill name, not a canonical resource id")
	}
}
