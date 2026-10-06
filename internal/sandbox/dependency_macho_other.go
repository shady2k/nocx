//go:build !darwin

package sandbox

import "fmt"

func discoverMachODependencies(string) ([]string, error) {
	return nil, fmt.Errorf("Mach-O discovery is unsupported")
}
