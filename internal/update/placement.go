package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveInstallTarget separates the running executable from the update destination.
// probe prepares a candidate directory and checks whether installation is permitted.
func ResolveInstallTarget(goos, executable, appImage, home string, probe func(string) error) (string, error) {
	switch goos {
	case "darwin":
		// THE BUNDLE THAT IS RUNNING IS THE ONE AN UPDATE REPLACES
		// (nocx-vh2hr.1.19). This used to try /Applications first and never
		// look at `executable`, so a nocx started from ~/Applications — or
		// anywhere else writable — updated a DIFFERENT bundle. The macOS
		// update proof installed its previous nightly under the disposable
		// home, ran it, and the applier swapped /Applications/nocx.app; the
		// bundle the proof had installed and launched was untouched, which is
		// exactly "the installed bundle is not the candidate's bytes". For a
		// person it is worse than a failed proof: their own copy keeps running
		// the old build and a newer one appears somewhere they did not ask for.
		//
		// A bundle that cannot be written where it lies still falls back to the
		// canonical locations: that is the DMG case, where the copy being run
		// is a read-only mounted image and /Applications is where it belongs.
		if bundle := runningBundle(executable); bundle != "" {
			if err := probe(filepath.Dir(bundle)); err == nil {
				return bundle, nil
			}
		}
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

// runningBundle is the .app that contains this executable, or "" when the
// path is not the macOS bundle layout.
//
// The layout is nocx.app/Contents/MacOS/nocx — the same fact
// serverbin.SiblingPath reads one directory down — so the bundle is three
// levels above the executable and its name ends in .app. Anything else is not
// a bundle this updater can exchange, and the caller keeps its canonical
// fallback.
func runningBundle(executable string) string {
	if executable == "" {
		return ""
	}
	if filepath.Base(filepath.Dir(executable)) != "MacOS" {
		return ""
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if !strings.HasSuffix(strings.ToLower(bundle), ".app") {
		return ""
	}
	return bundle
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
