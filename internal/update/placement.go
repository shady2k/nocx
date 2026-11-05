package update

import (
	"errors"
	"os"
	"path/filepath"
)

// ResolveInstallTarget separates the running executable from the update destination.
// probe prepares a candidate directory and checks whether installation is permitted.
func ResolveInstallTarget(goos, executable, appImage, home string, probe func(string) error) (string, error) {
	switch goos {
	case "darwin":
		candidates := []string{"/Applications", filepath.Join(home, "Applications")}
		for _, dir := range candidates {
			if err := probe(dir); err != nil {
				continue
			}
			return filepath.Join(dir, "nocx.app"), nil
		}
		return "", errors.New("cannot install the update; allow writes to Applications and try again")
	case "linux":
		if appImage != "" {
			return appImage, nil
		}
	}
	return executable, nil
}

// ProbeInstallDirectory creates the user Applications directory if needed and
// checks actual write access rather than inferring it from permission bits.
func ProbeInstallDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".nocx-install-probe-")
	if err != nil {
		return err
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	return errors.Join(closeErr, removeErr)
}
