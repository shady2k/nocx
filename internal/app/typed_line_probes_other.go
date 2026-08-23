//go:build !unix

package app

// A multiplex master is an OpenSSH concept carried over a unix-domain socket.
// On a platform without one there is no master to observe, so both
// observations answer the safe way: no socket, no process, and the typed path
// never proves ownership.

func socketPresent(string) bool { return false }

func processAlive(int) bool { return false }
