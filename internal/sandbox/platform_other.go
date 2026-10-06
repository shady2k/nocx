//go:build !linux && !darwin

package sandbox

import (
	"fmt"
	"os"
)

func backendAvailable() error { return fmt.Errorf("filesystem enforcement is unsupported") }
func identity(string) (FileIdentity, error) {
	return FileIdentity{}, fmt.Errorf("filesystem enforcement is unsupported")
}

func verifyPinned(*os.File, FileIdentity, RootKind) error {
	return fmt.Errorf("filesystem enforcement is unsupported")
}

func openPinned(string) (*os.File, error) {
	return nil, fmt.Errorf("filesystem enforcement is unsupported")
}

func createRuntime(string) (RuntimePaths, error) {
	return RuntimePaths{}, fmt.Errorf("filesystem enforcement is unsupported")
}

func createProjections(*Prepared, string, string, ProfileRoots, ProfileRoots) error {
	return fmt.Errorf("filesystem enforcement is unsupported")
}

func discoverDependencies(string, string) ([]string, error) {
	return nil, fmt.Errorf("filesystem enforcement is unsupported")
}

func linkedGitRoots(string) ([]string, error) {
	return nil, fmt.Errorf("filesystem enforcement is unsupported")
}

func resolveExecutable(string, string) (string, error) {
	return "", fmt.Errorf("filesystem enforcement is unsupported")
}

func dupPinned(*os.File) (*os.File, error) {
	return nil, fmt.Errorf("filesystem enforcement is unsupported")
}
