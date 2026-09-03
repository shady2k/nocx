//go:build !linux && !darwin

package session

// NewInspector answers nothing on a platform whose process-inspection API
// nobody has written here yet; see the linux sibling for the shape.
//
// A nil Inspector is a supported state rather than a hole: Service treats it
// as "this helper offers no OS evidence", and the inventory then carries the
// launch record with `observed: null` — which decodes as "nobody could be
// asked", not as "we looked and there is nothing". A helper for such a host
// would still hold sessions and still be listable, which is the property that
// matters.
//
// It is NOT how macOS answers, and that was the defect nocx-k6p18.10 fixed:
// the shipped platform silently landing here is what left a tab showing the
// directory a shell started in.
func NewInspector() Inspector { return nil }
