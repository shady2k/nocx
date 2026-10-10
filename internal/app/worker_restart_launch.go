package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// workerRestoreEnroller owns the worker-to-pane rendezvous that a resumed
// agent's authenticated enrolment completes. Keeping that seam narrow leaves
// the lifecycle publisher as the only enrolment authority (AD-1, AD-6, AD-8).
type workerRestoreEnroller interface {
	armForResume(workers.ParticipantID, string)
	Withdraw(context.Context, workers.ParticipantID) error
}

// relaunchWorkerRecords is the launch half of startup restore (ADR-0079). It
// opens each saved pane through the normal session path, then types the resume
// invocation resolved from the durable identity and the agent's launch record.
// The layout chain remains the only owner of tabs; the renderer adopts these
// sessions when it reads that chain.
func relaunchWorkerRecords(ctx context.Context, restorations []workers.Restoration,
	opener sessionOpenerSeam, integration integrationAwaiterSeam, enrolments workerRestoreEnroller,
) {
	lg := log.From(ctx)
	for _, restored := range restorations {
		req := restored.Record.Request()
		if !restored.Restorable() {
			reportWorkerRestoreFailure(ctx, opener, integration, req,
				fmt.Sprintf("%s: %s", restored.Failure.Reason, restored.Failure.Detail))
			continue
		}
		req = *restored.Request
		if enrolments == nil {
			reportWorkerRestoreFailure(ctx, opener, integration, req, "worker enrolment tracking is unavailable")
			continue
		}
		// Arm worker identity before opening the session, but keep the initial
		// resumed command on the configured agent-launch path. Fresh worker
		// spawns use literal commands; a restart must use the agent record so
		// its executable, environment and resume invocation stay authoritative.
		enrolments.armForResume(req.Participant, req.PaneID)
		opened, err := opener.OpenSession(ctx, transport.OpenSpec{
			PaneID: req.PaneID, Cwd: req.Cwd, Cols: 80, Rows: 24,
		})
		if err == nil {
			if integration == nil {
				err = fmt.Errorf("shell integration is unavailable")
			} else {
				var outcome transport.IntegrationOutcome
				outcome, err = integration.AwaitIntegration(ctx, opened.Session.ID())
				if err == nil && outcome.Registered && outcome.Status != transport.IntegrationIntegrated {
					err = fmt.Errorf("the restored pane shell did not integrate (%s: %s)", outcome.Status, outcome.Reason)
				}
			}
		}
		if err == nil {
			argv, splitErr := shellintegration.SplitAgentCommand(req.Command)
			if splitErr != nil {
				err = fmt.Errorf("the saved agent command is invalid: %w", splitErr)
			} else if len(req.ResumeArgs) == 0 {
				err = fmt.Errorf("the saved resume configuration produced no arguments")
			} else {
				argv = append(argv, req.ResumeArgs...)
				line := shellintegration.QuoteAgentArgv(argv) + "\n"
				if !opened.Session.EnqueueWrite([]byte(line)) {
					err = fmt.Errorf("the restored pane refused the agent launch")
				}
			}
		}
		if err != nil {
			if withdrawErr := enrolments.Withdraw(ctx, req.Participant); withdrawErr != nil {
				lg.Warn("worker restart: could not withdraw the failed pane enrolment",
					"pane_id", req.PaneID, "participant", req.Participant, "error", withdrawErr)
			}
			lg.Warn("worker restart: the agent could not be relaunched in its saved pane",
				"pane_id", req.PaneID, "agent", req.Agent, "error", err)
			if opened.Session != nil {
				writeRestoreFailure(ctx, opened.Session, req, fmt.Sprint(err))
			} else {
				reportWorkerRestoreFailure(ctx, opener, integration, req, fmt.Sprint(err))
			}
			continue
		}
		lg.Info("worker restart: the configured agent resume invocation was queued",
			"pane_id", req.PaneID, "agent", req.Agent, "resume_mode", string(req.Resume.Mode))
	}
}

// reportWorkerRestoreFailure opens only the existing pane's terminal session
// and prints why resume failed. An unknown or unsupported agent never becomes
// a successful empty-shell restore.
func reportWorkerRestoreFailure(ctx context.Context, opener sessionOpenerSeam,
	integration integrationAwaiterSeam, req workers.RestoreRequest, reason string,
) {
	lg := log.From(ctx)
	if opener == nil {
		return
	}
	opened, err := opener.OpenSession(ctx, transport.OpenSpec{
		PaneID: req.PaneID, Cwd: req.Cwd, Cols: 80, Rows: 24,
	})
	if err != nil && req.Cwd != "" {
		// A missing worktree must not prevent the reason from reaching the
		// restored pane. Retry in the shell's normal fallback directory only
		// after the saved directory was refused.
		opened, err = opener.OpenSession(ctx, transport.OpenSpec{PaneID: req.PaneID, Cols: 80, Rows: 24})
	}
	if err != nil {
		lg.Warn("worker restart: could not open the pane to report why resume failed",
			"pane_id", req.PaneID, "error", err)
		return
	}
	if integration == nil {
		reason += "; terminal integration is unavailable"
	} else if outcome, waitErr := integration.AwaitIntegration(ctx, opened.Session.ID()); waitErr != nil {
		reason += "; terminal integration is unavailable: " + fmt.Sprint(waitErr)
	} else if outcome.Registered && outcome.Status != transport.IntegrationIntegrated {
		reason += fmt.Sprintf("; terminal integration is %s (%s)", outcome.Status, outcome.Reason)
	}
	writeRestoreFailure(ctx, opened.Session, req, reason)
}

func writeRestoreFailure(ctx context.Context, sess session.Session, req workers.RestoreRequest, reason string) {
	lg := log.From(ctx)
	text := fmt.Sprintf("nocx could not resume %s: %s", req.Agent, strings.TrimSpace(reason))
	line := shellintegration.QuoteAgentArgv([]string{"printf", "%s\\n", text}) + "\n"
	if !sess.EnqueueWrite([]byte(line)) {
		lg.Warn("worker restart: the pane refused the visible resume failure", "pane_id", req.PaneID)
	}
}
