//go:build !unix

package hostsvc

import "os"

// fileIdentity has no dev:ino to report on this platform. An empty string is
// the honest answer: the wire shape has the field, and a caller must read
// emptiness as "this host cannot tell an append from a replacement" rather
// than as an identity two reads could share.
func fileIdentity(info os.FileInfo) string { return "" }
