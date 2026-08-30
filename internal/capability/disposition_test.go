package capability

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

func TestOperationDispositionsMatchMap(t *testing.T) {
	gate := func(name string) control.Admission {
		return Gate(name, 1, 8, time.Second)
	}
	db := content.NewStub(log.NewSlogAdapter(nil))
	targetOp, err := NewSessionOperations(gate(GateSession), gate("lane"), dispositionRegistry{}, nil).ForSessionTarget("s1")
	if err != nil {
		t.Fatalf("ForSessionTarget: %v", err)
	}

	cases := []struct {
		name string
		op   AssistantOperation
		kind DispositionKind
	}{
		{"AgentOperation", NewAgentOperation(gate(GateContent), gate("lane"), db), DispositionDirect},
		{"APICollectionOperation", NewAPICollectionOperation(gate(GateAPI), gate("lane"), nil), DispositionDirect},
		{"APIImportOperation", NewAPIImportOperation(gate(GateAPI), gate(GateVault), gate("lane"), nil, nil, nil), DispositionDirect},
		{"BackupOperation", NewBackupOperation(gate(GateConfig), gate("lane"), nil), DispositionDirect},
		{"CaptureSaveOperation", NewCaptureSaveOperation(gate(GateVault), gate(GateContent), gate("lane"), nil, nil), DispositionDirect},
		{"TabbyImportOperation", NewTabbyImportOperation(gate(GateConfig), gate(GateVault), gate("lane"), nil, nil, nil, nil, nil), DispositionDirect},
		{"ConfigOperation", NewConfigOperation(gate(GateConfig), gate(GateVault), gate("lane"), nil, nil, nil, nil, nil, nil, nil, nil), DispositionDirect},
		{"ContentOperation", NewContentOperation(gate(GateContent), gate("lane"), db), DispositionDirect},
		{"FilesystemOpenOperation", NewFilesystemOpenOperation(gate(GateSession), gate(GateFilesystem), gate("lane"), nil, nil, nil), DispositionAdapted},
		{"FilesystemBindingOperation", NewFilesystemBindingOperation(gate(GateFilesystem), gate("lane"), nil), DispositionAdapted},
		{"GitOpenOperation", NewGitOpenOperation(gate(GateSession), gate(GateGit), gate("lane"), nil, nil, nil), DispositionAdapted},
		{"GitBindingOperation", NewGitBindingOperation(gate(GateGit), gate("lane"), nil), DispositionAdapted},
		{"LayoutOperation", NewLayoutOperation(gate(GateContent), gate("lane"), db), DispositionDirect},
		{"LedgerOperation", NewLedgerOperation(gate(GateContent), gate("lane"), db), DispositionExcluded},
		{"NoteOperation", NewNoteOperation(gate(GateConfig), gate("lane"), nil), DispositionDirect},
		{"OpenOperation", NewOpenOperation(gate(GateConfig), gate(GateSession), gate("lane"), nil, nil), DispositionAdapted},
		{"SecretOperation", NewSecretOperation(gate(GateConfig), gate(GateVault), gate("lane"), nil, nil, nil, nil), DispositionDirect},
		{"SessionOperation", NewSessionOperation(gate(GateSession), gate("lane"), nil, nil), DispositionAdapted},
		{"SessionTargetOperation", targetOp, DispositionAdapted},
		{"SnippetOperation", NewSnippetOperation(gate(GateConfig), gate("lane"), nil), DispositionDirect},
		{"UIStateOperation", NewUIStateOperation(gate(GateConfig), gate("lane"), nil), DispositionExcluded},
		{"VaultOperation", NewVaultOperation(gate(GateVault), gate("lane"), nil), DispositionDirect},
		{"VaultResetOperation", NewVaultResetOperation(gate(GateConfig), gate(GateVault), gate("lane"), nil), DispositionDirect},
	}

	if len(cases) != 23 {
		t.Fatalf("operation map has %d rows, want 23", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.op.Disposition()
			if got.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", got.Kind, tc.kind)
			}
			switch got.Kind {
			case DispositionDirect:
				if strings.TrimSpace(got.Metadata) == "" {
					t.Fatal("direct operation has no metadata")
				}
			case DispositionAdapted:
				if strings.TrimSpace(got.AgentOperationName) == "" || strings.TrimSpace(got.Reason) == "" {
					t.Fatalf("adapted operation lacks agent name or reason: %+v", got)
				}
			case DispositionExcluded:
				if strings.TrimSpace(got.Reason) == "" {
					t.Fatal("excluded operation has no reason")
				}
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("valid disposition rejected: %v", err)
			}
		})
	}
}

func TestDispositionMissingRefusesAssembly(t *testing.T) {
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("missing operation disposition assembled without refusal")
		}
		if !strings.Contains(fmt.Sprint(got), "invalid operation disposition") {
			t.Fatalf("panic = %v, want invalid operation disposition", got)
		}
	}()
	_ = newOperation[any](Disposition{}, Gate("test", 1, 1, time.Second), &guard{}, nil)
}

// This proves operation-owned domain-plus-lane acquisition, direct or wrapped.
// It does NOT prove that a transport binding avoids acquiring the same gate a
// second time; that assertion lives in nocx-o9era.3.
func TestOperationOwnsDomainGate(t *testing.T) {
	log := make([]string, 0, 2)
	contentGate := &recordingDispositionAdmission{name: GateContent, log: &log}
	lane := &recordingDispositionAdmission{name: "lane", log: &log}
	op := NewContentOperation(contentGate, lane, nil)

	if err := op.Run(context.Background(), func(context.Context, ContentService) error { return nil }); err != nil {
		t.Fatalf("direct operation Run: %v", err)
	}
	direct := strings.Join(log, ",")

	log = log[:0]
	wrapper := func(ctx context.Context) error {
		return op.Run(ctx, func(context.Context, ContentService) error { return nil })
	}
	if err := wrapper(context.Background()); err != nil {
		t.Fatalf("wrapper operation Run: %v", err)
	}
	wrapped := strings.Join(log, ",")

	want := "acquire:content,acquire:lane,release:lane,release:content"
	if direct != want || wrapped != want {
		t.Fatalf("operation traces = direct %q, wrapped %q, want %q", direct, wrapped, want)
	}
}

type recordingDispositionAdmission struct {
	name string
	log  *[]string
}

func (a *recordingDispositionAdmission) Name() string { return a.name }

func (a *recordingDispositionAdmission) TryAcquire(context.Context) (control.Permit, *control.Rejection) {
	*a.log = append(*a.log, "acquire:"+a.name)
	return dispositionPermit{release: func() { *a.log = append(*a.log, "release:"+a.name) }}, nil
}

type dispositionPermit struct {
	release func()
}

func (p dispositionPermit) Release() { p.release() }

type dispositionRegistry struct{}

func (dispositionRegistry) Open(context.Context, session.Config) (session.Session, error) {
	return nil, nil
}

func (dispositionRegistry) Get(session.ID) (session.Session, error) {
	return nil, nil
}

func (dispositionRegistry) Close(session.ID) error { return nil }

func (dispositionRegistry) List() []session.Session { return nil }

// A registry stamps every session it opens with the instance it belongs to
// (nocx-oevq4), and a double that opens nothing still has to say which one —
// this test is about dispositions, so any stable value serves.
func (dispositionRegistry) InstanceID() session.InstanceID {
	return session.InstanceID("disposition-test")
}
