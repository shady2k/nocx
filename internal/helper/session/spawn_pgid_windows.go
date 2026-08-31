//go:build windows

package session

// processGroupOf has no kernel answer on Windows: there are no process groups
// in the POSIX sense and no setsid to establish one. The pid stands in, and
// the helper's signalling is correspondingly limited there — stated here
// rather than discovered by a caller who assumed otherwise.
func processGroupOf(pid int) int { return pid }
