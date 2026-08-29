//go:build nocx_dev_bind

package transport

import (
	"fmt"
	"os"
	"strconv"
)

// THE DEV STAND'S HALF. `-tags nocx_dev_bind` is a build saying: something in
// this build may be told which port to listen on, because it is a stand
// somebody is watching over an ssh forward and the forward must survive a
// restart.
//
// It exists because the alternative is worse in a way that is easy to
// undercount. `make dev-web` prints an `ssh -L` line, and with an OS-chosen
// port that line is stale the moment the stand restarts — so every
// edit-restart cycle costs re-making the forward on the far side. That is the
// cost the coordinator cutover (nocx-gpyxp) introduced without meaning to; it
// removed the old NOCX_WS_PORT along with cmd/devharness.
//
// WHAT IS SETTABLE HERE IS A NUMBER, NEVER AN ADDRESS. §6's objection is that
// a coordinator which can be told where to bind can be told to bind off
// loopback; the host stays a literal in ws.go and is not a parameter of
// anything. A port cannot move the listener off 127.0.0.1, and
// TestDevBindNeverLeavesLoopback asserts that under both builds.
//
// THE TAG CARRIES NO NUMBER OF ITS OWN, and that is not tidiness. A default
// here would apply to EVERY listener this build constructs, so two servers in
// one process would fight over one port — measured, not feared: it failed ten
// tests in internal/transport that start a second WSServer and never asked for
// a port at all. The number belongs to the stand that wants it, and lives in
// scripts/dev-web.sh, which is the only thing that sets this variable.
//
// The variable is NOT the old `NOCX_WS_PORT`: e2e/preflight.ts refuses a run
// with that name set, on the ground that nothing reads it any more, and a run
// that thinks it is driving a backend of its own is measuring the wrong
// process. Reviving the name would make that guard lie.
const (
	devBindEnabled = true
	devBindPortEnv = "NOCX_DEV_WS_PORT"
)

// devBindPort is the port to ask the OS for; zero means "you pick", which is
// what an unset variable answers.
//
// A value that is set and unusable is an ERROR rather than a fallback to zero.
// Falling back would restore, silently, the churn this tag exists to remove —
// the stand would come up on some other port and print an ssh line that is
// already wrong, which is the failure the whole change is about.
func devBindPort() (int, error) {
	raw, ok := os.LookupEnv(devBindPortEnv)
	if !ok || raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s=%q is not a port between 1 and 65535", devBindPortEnv, raw)
	}
	return port, nil
}
