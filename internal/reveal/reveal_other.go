//go:build !darwin && !linux && !windows

package reveal

// newRevealer returns ErrRevealUnavailable on platforms nocx does not
// ship a file-manager reveal for. The composition root passes nil to
// the transport, files.reveal answers -32601 — and files.open answers
// revealAvailable:false, which is the field the renderer gates the
// "Show in Finder" menu item on (nocx-ngf3u): a capability the UI
// offers and the backend refuses is the defect, and the gate removes
// the offer on exactly the platforms the backend refuses.
func newRevealer() (*Revealer, error) {
	return nil, ErrRevealUnavailable
}

// Reveal is never called on an unsupported platform: the constructor
// returns nil and the transport's nil guard answers -32601 before
// reaching here. The method exists so *Revealer satisfies the
// transport.FilesRevealer interface on every build.
func (r *Revealer) Reveal(path string) error {
	return ErrRevealUnavailable
}
