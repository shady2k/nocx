//go:build !darwin

package endpoint

import "os/exec"

func startHelper(exe string) (func() error, error) {
	cmd := exec.Command(exe, ServeCommand) // #nosec G204 — the installed helper binary
	// No inherited streams: the carrier can end while the daemon keeps serving.
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}
