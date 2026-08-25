//go:build windows

package reveal

// newRevealer builds the Windows revealer: `explorer.exe /select,<path>`
// reveals the file in Explorer with the file selected.
func newRevealer() (*Revealer, error) {
	return &Revealer{run: realRunner()}, nil
}

// Reveal shows a path in Explorer via `explorer.exe /select,<path>`.
// The comma-joined form is Explorer's own syntax; the path is passed
// verbatim (no shell), so spaces and special characters survive.
func (r *Revealer) Reveal(path string) error {
	out, err := r.run("explorer.exe", "/select,"+path)
	if err != nil {
		return wrapRunErr("explorer.exe", out, err)
	}
	return nil
}
