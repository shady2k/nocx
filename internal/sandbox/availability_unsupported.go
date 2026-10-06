//go:build !linux && !darwin

package sandbox

import "errors"

func NativeAvailable() error { return errors.New("native sandbox unavailable on this platform") }
