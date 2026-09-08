package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
)

func TestSkillsReadRecordsAUseOnlyWhenItSucceeded(t *testing.T) {
	t.Run("a successful read is a use", func(t *testing.T) {
		library := &skillsReadSource{content: skill.Content{
			Path: "SKILL.md", Bytes: []byte("---\nname: deploy\ndescription: d\n---\nbody\n"),
		}}
		cap := agenttools.NewContentScope([]agenttools.ResourceRef{{Kind: content.ResourceContent, ID: "content"}})
		if _, err := executeSkillsRead(context.Background(), cap, []byte(`{"name":"deploy"}`), toolSeams{skills: library}); err != nil {
			t.Fatalf("executeSkillsRead: %v", err)
		}
		if len(library.recorded) != 1 || library.recorded[0] != "deploy" {
			t.Fatalf("recorded = %v, want exactly one use of deploy", library.recorded)
		}
	})

	t.Run("a refused read is not a use", func(t *testing.T) {
		library := &skillsReadSource{readErr: errors.New("no such skill")}
		cap := agenttools.NewContentScope([]agenttools.ResourceRef{{Kind: content.ResourceContent, ID: "content"}})
		if _, err := executeSkillsRead(context.Background(), cap, []byte(`{"name":"deploy"}`), toolSeams{skills: library}); err == nil {
			t.Fatal("the read succeeded, so this subtest proves nothing")
		}
		if len(library.recorded) != 0 {
			t.Fatalf("recorded = %v after a failed read, want none", library.recorded)
		}
	})
}
