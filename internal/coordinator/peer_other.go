//go:build !linux && !darwin

package coordinator

import "errors"

// errUnsupportedPlatform is the honest answer for a platform nocx does not
// ship a daemon on: without peer credentials the socket's trust boundary
// does not exist, so it refuses to serve rather than serving unchecked.
var errUnsupportedPlatform = errors.New("peer credentials are not implemented on this platform")

func peerUID(uintptr) (uint32, error) { return 0, errUnsupportedPlatform }

func ownerUID(string) (uint32, error) { return 0, errUnsupportedPlatform }
