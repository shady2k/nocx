//go:build !unix

package hostsvc

import "os"

// lockExclusive has no advisory lock to take on this platform, so it answers
// success and the sweep treats every predecessor directory as abandoned. The
// shipped helper runs on unix, where the answer is real; this is the same
// platform split write_other.go already states for the rest of the write path,
// and it is stated here rather than hidden so nobody reads the missing lock as
// an oversight.
func lockExclusive(*os.File) error { return nil }
