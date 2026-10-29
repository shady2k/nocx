//go:build nocx_local_ssh

package app

// The lane's failure block lives behind the same constraint as its only
// caller: live_sshd_dial_test.go is the file that waits over the lane, and
// on a build without nocx_local_ssh the helpers would be dead code that
// golangci-lint's unused check fails (ci-mac compiles this package with no
// tags). Nothing here is product code.

import (
	"fmt"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// lifecycleName is the readable name of a lane lifecycle. LifecycleState is a
// uint8 with no String method — the kernel never prints it — and a failure
// block that says '\x02' where the reader needs "prompt_ready" is the one thing
// a stranger cannot use.
func lifecycleName(st lifecycle.LifecycleState) string {
	switch st {
	case lifecycle.LifecycleNative:
		return "native"
	case lifecycle.LifecyclePromptReady:
		return "prompt_ready"
	case lifecycle.LifecycleRunning:
		return "running"
	case lifecycle.LifecycleDesynchronized:
		return "desynchronized"
	case lifecycle.LifecycleLost:
		return "lost"
	}
	return fmt.Sprintf("unknown(%d)", uint8(st))
}

// laneReport is the failure block the live-ssh journeys print when a wait over
// the lane times out (nocx-1ebrs.66.1.18). It answers, at the moment of the
// timeout and from the kernel's own read model, the questions the old message
// ("timed out waiting for lane back at PromptReady") left open: which lane, what
// the kernel says its lifecycle/domain/attempt are, which transport it rides,
// and what the last frames were and what became of them.
func (r *recordingKernel) laneReport() string {
	r.mu.Lock()
	lane, transport, minted := r.lane, r.transport, r.minted
	frames := append([]kernelFrame(nil), r.frames...)
	r.mu.Unlock()

	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "kernel.lane = %q (domains minted: %d)\n", lane, minted)
	fmt.Fprintf(&b, "lane transport = %q\n", transport)
	if lane == "" {
		b.WriteString("kernel.State: no lane was ever minted, so there is nothing to read\n")
	} else if st, err := r.State(lane); err != nil {
		fmt.Fprintf(&b, "kernel.State(%q) failed: %v\n", lane, err)
	} else {
		fmt.Fprintf(&b, "kernel.State(%q): lifecycle %s domain %q attempt %q\n",
			lane, lifecycleName(st.Lifecycle), st.Domain, st.Attempt)
		fmt.Fprintf(&b, "  open attempts %v, stack %v, recovery sighted %v\n",
			st.OpenAttempts, st.Stack, st.RecoverySighted)
	}

	shown := 0
	for _, f := range frames {
		if f.Lane == "" || f.Lane == lane {
			shown++
		}
	}
	fmt.Fprintf(&b, "pane lifecycle frames the kernel saw (%d in the tail, showing %d; accepted/rejected/dropped):\n",
		len(frames), shown)
	if shown == 0 {
		b.WriteString("  none — no frame reached the kernel for this lane\n")
	}
	for _, f := range frames {
		if f.Lane != "" && f.Lane != lane {
			continue
		}
		fmt.Fprintf(&b, "  -%8s %-9s %s", now.Sub(f.At).Round(time.Millisecond), f.Disposition, f.What)
		if f.Reason != "" {
			fmt.Fprintf(&b, " — %s", f.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}
