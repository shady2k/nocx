//go:build darwin

package reveal

// newRevealer builds the macOS revealer: `open -R <path>` reveals the file
// in Finder. The path is passed verbatim — no shell, no quoting — so a
// path with spaces or special characters reaches open intact.
func newRevealer() (*Revealer, error) {
	return &Revealer{run: realRunner()}, nil
}

// Reveal shows a path in Finder via `open -R`.
func (r *Revealer) Reveal(path string) error {
	out, err := r.run("open", "-R", path)
	if err != nil {
		return wrapRunErr("open", out, err)
	}
	return nil
}
