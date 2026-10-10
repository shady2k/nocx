package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/workers"
)

func TestResumeArgumentsUseEachAgentRecordAdapterAndTaskIdentity(t *testing.T) {
	tests := []struct {
		name      string
		record    agentrecord.Record
		wantByID  []string
		wantByCwd []string
	}{
		{
			name: "claude",
			record: agentrecord.Record{ID: "claude", Document: agentrecord.Document{Resume: agentrecord.Resume{
				ResumeIDArgs:  []string{"--resume", "{UUID}"},
				ResumeCwdArgs: []string{"--continue"},
			}}},
			wantByID: []string{"--resume", "session-1"}, wantByCwd: []string{"--continue"},
		},
		{
			name: "codex",
			record: agentrecord.Record{ID: "codex", Document: agentrecord.Document{Resume: agentrecord.Resume{
				ResumeIDArgs:  []string{"resume", "{UUID}"},
				ResumeCwdArgs: []string{"resume", "--last"},
			}}},
			wantByID: []string{"resume", "session-1"}, wantByCwd: []string{"resume", "--last"},
		},
		{
			name: "omp",
			record: agentrecord.Record{ID: "omp", Document: agentrecord.Document{Resume: agentrecord.Resume{
				ResumeIDArgs:  []string{"--resume={UUID}"},
				ResumeCwdArgs: []string{"--continue"},
			}}},
			wantByID: []string{"--resume=session-1"}, wantByCwd: []string{"--continue"},
		},
		{
			name: "prime-agent",
			record: agentrecord.Record{ID: "prime-agent", Document: agentrecord.Document{Resume: agentrecord.Resume{
				ResumeIDArgs:  []string{"-r", "{UUID}"},
				ResumeCwdArgs: []string{"-c"},
			}}},
			wantByID: []string{"-r", "session-1"}, wantByCwd: []string{"-c"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shared := workers.Worktree{}
			one := workers.ResumeIdentityFor(shared, "session-1")
			two := workers.ResumeIdentityFor(shared, "session-2")
			got, err := resumeArgumentsFor(tc.record, one)
			if err != nil {
				t.Fatalf("first shared-checkout resume args: %v", err)
			}
			if !reflect.DeepEqual(got, tc.wantByID) {
				t.Fatalf("first shared-checkout args = %q, want %q", got, tc.wantByID)
			}
			got, err = resumeArgumentsFor(tc.record, two)
			if err != nil {
				t.Fatalf("second shared-checkout resume args: %v", err)
			}
			wantSecond := append([]string(nil), tc.wantByID...)
			for i := range wantSecond {
				switch wantSecond[i] {
				case "session-1":
					wantSecond[i] = "session-2"
				case "--resume=session-1":
					wantSecond[i] = "--resume=session-2"
				}
			}
			if !reflect.DeepEqual(got, wantSecond) {
				t.Fatalf("second shared-checkout args = %q, want %q", got, wantSecond)
			}

			worktree := workers.Worktree{Path: "/worktrees/one", Branch: "task-one"}
			cwd := workers.ResumeIdentityFor(worktree, "ignored-session")
			got, err = resumeArgumentsFor(tc.record, cwd)
			if err != nil {
				t.Fatalf("worktree resume args: %v", err)
			}
			if !reflect.DeepEqual(got, tc.wantByCwd) {
				t.Fatalf("worktree args = %q, want %q", got, tc.wantByCwd)
			}
		})
	}
}

func TestResumeArgumentsRefuseALazySharedCheckoutID(t *testing.T) {
	record := agentrecord.Record{ID: "prime-agent", Document: agentrecord.Document{Resume: agentrecord.Resume{
		ResumeIDArgs:  []string{"-r", "{UUID}"},
		ResumeCwdArgs: []string{"-c"},
	}}}
	shared := workers.Worktree{}
	identity := workers.ResumeIdentityFor(shared, "")
	if identity != (workers.ResumeIdentity{Mode: workers.ResumeNone}) {
		t.Fatalf("identity = %+v, want explicit non-resumable state", identity)
	}
	if _, err := resumeArgumentsFor(record, identity); err == nil {
		t.Fatal("a shared-checkout task with a lazy ID produced resume args instead of refusing")
	}
}

func TestAgentProbeBuildsTheClaudeWorktreeResumeArgs(t *testing.T) {
	store, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}
	worktree := workers.Worktree{Path: "/worktrees/task-one", Branch: "task-one"}
	identity := workers.ResumeIdentityFor(worktree, "ignored-session")
	args, err := (agentProbe{store: store}).Resume(context.Background(), "claude", worktree, identity)
	if err != nil {
		t.Fatalf("worktree resume: %v", err)
	}
	want := []string{"--continue"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("worktree resume args = %q, want %q", args, want)
	}
}
