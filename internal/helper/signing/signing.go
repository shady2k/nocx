// Package signing reads macOS codesign identity for the local helper.
// The release signs it with a stable identifier and certificate. Darwin's
// endpoint spawn gives it its own responsible code (ADR-qy0pf1va); orphaning
// alone does not do that, as the owner's procinfo evidence demonstrated.
package signing

import (
	"fmt"
	"strings"
)

// The local helper is distinct responsible code, not the app's dev.nocx.
// Its identifier and certificate remain stable across builds.
const (
	HelperIdentifier = "dev.nocx.helper"
	Authority        = "nocx Project Code Signing"
)

// Report is the part of `codesign -d --verbose=4` this product decides on.
type Report struct {
	Identifier string
	Authority  []string
	Adhoc      bool
}

// Parse reads the display output of `codesign -d --verbose=4`. It is
// deliberately line-based and forgiving about every key it does not use: the
// output has grown keys over macOS releases, and a parser that refused the
// unknown would make the gate fail on a macOS update instead of on the thing
// it guards.
func Parse(output string) Report {
	var report Report
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Identifier="):
			report.Identifier = strings.TrimPrefix(line, "Identifier=")
		case strings.HasPrefix(line, "Authority="):
			report.Authority = append(report.Authority, strings.TrimPrefix(line, "Authority="))
		case line == "Signature=adhoc":
			report.Adhoc = true
		}
	}
	return report
}

// Check refuses anything but the expected identity. Ad-hoc is named first and
// separately because it is the failure this gate was built for: an ad-hoc
// signature has no stable requirement at all, whatever its identifier says.
func Check(report Report, identifier, authority string) error {
	if report.Adhoc {
		return fmt.Errorf("helper is ad-hoc signed (Signature=adhoc, Identifier=%s); "+
			"Local Network Privacy cannot track an ad-hoc program across builds", report.Identifier)
	}
	if report.Identifier != identifier {
		return fmt.Errorf("helper Identifier is %q, want %q", report.Identifier, identifier)
	}
	if len(report.Authority) == 0 {
		return fmt.Errorf("helper has no signing Authority; want %q", authority)
	}
	for _, got := range report.Authority {
		if got == authority {
			return nil
		}
	}
	return fmt.Errorf("helper is signed by %q, want an authority %q",
		strings.Join(report.Authority, ", "), authority)
}
