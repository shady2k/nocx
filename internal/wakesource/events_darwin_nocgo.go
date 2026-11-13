//go:build darwin && !(cgo && nocx_local_ssh)

package wakesource

import "errors"

// osEvents answers "no input" on a Darwin build that is not the local helper's:
// the IOKit bridge needs cgo AND the nocx_local_ssh tag, so the deployed helper
// — the bytes installed on somebody else's machine — never links it. The clock
// comparison still detects a suspend; what is missing is the precise edge and
// the dark-wake distinction, and the source says so once at construction.
func osEvents() (<-chan Event, error) {
	return nil, errors.New("this build has no IOKit power notifications (CGO_ENABLED=0 or not the local helper's build)")
}
