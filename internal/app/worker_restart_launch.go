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

// relaunchWorkerRecords is the launch half of startup restore (ADR-0079).
// It opens the existing pane through the ordinary session path, then applies
// the saved command and the resume arguments resolved by agentProbe. The
// layout chain remains the only owner of tabs; panes.ts adopts this live
// session when it reads that chain.
func relaunchWorkerRecords(ctx context.Context, restorations []workers.Restoration,
	opener sessionOpenerSeam, integration integrationAwaiterSeam,
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
			lg.Warn("worker restart: the agent could not be relaunched in its saved pane", "pane_id", req.PaneID, "agent", req.Agent, "error", err)
			if opened.Session != nil {
				writeRestoreFailure(ctx, opened.Session, req, fmt.Sprint(err))
			} else {
				reportWorkerRestoreFailure(ctx, opener, integration, req, fmt.Sprint(err))
			}
			continue
		}
		lg.Info("worker restart: the configured agent resume invocation was queued", "pane_id", req.PaneID, "agent", req.Agent, "resume_mode", string(req.Resume.Mode))
	}
}

// reportWorkerRestoreFailure opens only the existing pane's terminal session
// and prints the reason there. A missing or unsupported agent never becomes a
// successful empty-shell restore.
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
		lg.Warn("worker restart: could not open the pane to report why resume failed", "pane_id", req.PaneID, "error", err)
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
