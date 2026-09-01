package skill

import (
	"errors"
	"log/slog"
)

var errUnavailable = errors.New("skill library is unavailable")

// Library is the assistant's filesystem-backed skill source. It owns the
// ordered roots so discovery and reads use one precedence definition.
type Library struct {
	roots []Root
}

func NewLibrary(roots []Root) *Library {
	copyRoots := append([]Root(nil), roots...)
	return &Library{roots: copyRoots}
}

func (l *Library) Index() []Skill {
	if l == nil {
		return nil
	}
	index := Discover(l.roots)
	if len(index) > MaxIndexed {
		slog.Warn("skill: index cap reached", "cap", MaxIndexed)
		index = index[:MaxIndexed]
	}
	return index
}

func (l *Library) Read(name, relPath string) (Content, error) {
	if l == nil {
		return Content{}, errUnavailable
	}
	return Read(l.roots, name, relPath)
}
