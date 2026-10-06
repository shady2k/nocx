package app

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

func TestToolAdmissionRefusesEnforcedUnknownAndRetiredSessions(t *testing.T) {
	for _, mode := range []string{"enforce", "unknown", "retired"} {
		t.Run(mode, func(t *testing.T) {
			logger := log.NewSlogAdapter(nil)
			reg := session.New(logger, workerAuthPTYFactory{log: logger})
			binding := session.LaunchBinding{}
			if mode != "retired" {
				binding.Mode = mode
			}
			sess, err := reg.Open(context.Background(), session.Config{
				Kind: session.KindLocal, Cols: 80, Rows: 24, LaunchBinding: binding,
			})
			if err != nil {
				t.Fatalf("open session: %v", err)
			}
			defer func() { _ = reg.Close(sess.ID()) }()
			const ownedPID = 4242
			if err = reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
				t.Fatalf("record process pid: %v", err)
			}
			grid := paneviewtest.NewViews(logger)
			defer grid.Withdraw(string(sess.ID()))
			if err = grid.Watch(string(sess.ID()), 80, 24); err != nil {
				t.Fatalf("watch session: %v", err)
			}
			if mode == "retired" {
				ref := session.Ref{ID: sess.ID(), Identity: sess.Identity()}
				if err = reg.FenceInput(context.Background(), ref, func() error { return nil }, nil); err != nil {
					t.Fatalf("retire source: %v", err)
				}
			}
			root := peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)}
			pinner := &workerAuthPinner{root: root, member: map[int]bool{9001: true}}
			auth := mustToolAuthorizer(t, pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, allowWorkerApproval{})
			published := false
			_, _, err = auth.Admit(toolendpoint.Peer{UID: 1000, PID: 9001}, func(string, toolendpoint.AdmissionEpoch) bool {
				published = true
				return true
			})
			if err == nil || published {
				t.Fatalf("session mode %q admitted worker tools (err=%v, published=%v)", mode, err, published)
			}
		})
	}
}
