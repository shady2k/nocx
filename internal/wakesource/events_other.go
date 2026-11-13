//go:build !linux && !darwin

package wakesource

import "errors"

// osEvents answers "no input" on a platform this product does not ship to. The
// clock comparison is the whole detector there, and the source says so once.
func osEvents() (<-chan Event, error) {
	return nil, errors.New("this platform has no sleep/wake input")
}
