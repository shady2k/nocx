//go:build linux

package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// NewInspector is the composition root's per-OS choice of evidence source,
// the shape internal/contentkey uses for readMachineID: one set of reporting
// rules (inspect.go), one leaf per platform, chosen by the BUILD rather than
// by a runtime switch nothing can typecheck.
//
// Linux reads /proc.
func NewInspector() Inspector {
	return &osInspector{src: &procfsSource{root: "/proc"}}
}

// procfsSource answers from /proc. It reports what it finds and returns the
// read's error otherwise; whether a failure counts as a missing diagnostic is
// inspect.go's decision and not this file's.
type procfsSource struct {
	// root is the procfs mount, "/proc" in production and a temporary
	// directory in tests. A field rather than a constant so the reads are
	// testable without the OS agreeing to cooperate.
	root string
}

func (p *procfsSource) source() string { return "procfs" }

func (p *procfsSource) dir(pid int) string {
	return filepath.Join(p.root, strconv.Itoa(pid))
}

// alive asks whether /proc still has an entry for the process. A pid that
// exited leaves none, which is how the inspector knows to say nothing at all.
func (p *procfsSource) alive(pid int) bool {
	_, err := os.Stat(p.dir(pid))
	return err == nil
}

// cwd resolves /proc/<pid>/cwd. It is refused often enough to matter — a
// process that changed uid, a hardened container, a pid that exited between
// the stat above and this read — and a refusal here becomes an explicitly
// named missing diagnostic rather than a blank the reader mistakes for "the
// shell has not moved".
func (p *procfsSource) cwd(pid int) (string, error) {
	return os.Readlink(filepath.Join(p.dir(pid), "cwd"))
}

// argv reads /proc/<pid>/cmdline: NUL-separated arguments and nothing else.
func (p *procfsSource) argv(pid int) ([]byte, error) {
	// #nosec G304 — the path is this source's own procfs root joined with an
	// integer pid the helper itself recorded at spawn. No caller-supplied
	// string reaches it, and the root is a constant outside tests.
	return os.ReadFile(filepath.Join(p.dir(pid), "cmdline"))
}

func (p *procfsSource) argvFormat() argvFormat { return argvPlain }

// comm reads /proc/<pid>/comm, which the kernel truncates — evidence, and
// evidence of the same shape darwin's p_comm has.
func (p *procfsSource) comm(pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join(p.dir(pid), "comm")) // #nosec G304 — see argv
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
