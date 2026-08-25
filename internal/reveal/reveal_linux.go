//go:build linux

package reveal

import (
	"os/exec"
	"path/filepath"
)

// newRevealer builds the Linux revealer. There is no cross-desktop
// "reveal this file" standard — `xdg-open` opens the containing
// directory in whatever file manager the desktop registers, which is
// the best the platform offers. When xdg-open is absent the reveal
// fails by name (ErrNoFileManager), never as a silent success.
func newRevealer() (*Revealer, error) {
	return &Revealer{run: realRunner()}, nil
}

// Reveal opens the containing directory via xdg-open. The file itself
// cannot be "revealed" — no cross-desktop standard exists — so the
// parent directory is what the user gets. A path at root opens root.
//
// xdg-open is looked up before running: a machine with no xdg-open on
// PATH has no file-manager opener, and the refusal names that (via
// ErrNoFileManager) rather than surfacing an opaque exec error.
func (r *Revealer) Reveal(path string) error {
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return ErrNoFileManager
	}
	dir := filepath.Dir(path)
	out, err := r.run("xdg-open", dir)
	if err != nil {
		return wrapRunErr("xdg-open", out, err)
	}
	return nil
}
