//go:build !unix

package hostsvc

import "os"

// processUmask has no process-wide umask to read on this platform, so the
// ordinary 022 is assumed: a new file is created 0644, which is what a stock
// account's editor would produce. A write that must be narrower says so with
// its target's own mode instead (applyMode).
var processUmask = os.FileMode(0o022)

func newFileMode() os.FileMode { return 0o666 &^ processUmask }

// chownToTarget cannot give a file away on this platform; the temporary keeps
// the account that created it.
func chownToTarget(string, os.FileInfo) error { return nil }

// syncDir has no directory to flush here; the file's own sync is the whole of
// what the platform offers.
func syncDir(string) error { return nil }
