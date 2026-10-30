//go:build !linux && !darwin

package serverbin

import "errors"

func (osFS) RenameNew(string, string) error {
	return errors.New("serverbin: atomic runtime tree promotion is unsupported on this platform")
}
