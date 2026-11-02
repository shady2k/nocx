// Package signing reads what macOS codesign reports about one executable and
// refuses a code identity this product did not choose.
//
// WHY THIS EXISTS (nocx-vh2hr.1.7). The local helper runs the pane shell on
// this machine. After an in-app update replaces nocx.app, an old helper can
// outlive the app (ADR-0104 keeps helpers and their shells alive), and macOS
// Local Network Privacy attributes the shell's non-platform children to the
// responsible code — which is then the orphaned helper. A helper left ad-hoc
// signed takes its identifier from the output file name ("a.out" today), so
// every build is a different program to LNP and no local-network grant can
// survive. The release signs the darwin local helper with the app's
// certificate and a fixed identifier, and this package is the gate that reads
// the result back: an ad-hoc helper is a refused release, not a warning.
package signing

import (
	"fmt"
	"strings"
)

// The stable identity of the local darwin helper.
//
// HelperIdentifier is deliberately its own identifier rather than the app's
// dev.nocx: the helper is a distinct executable and TCC/LNP track it by its
// designated requirement (identifier + certificate). Fixing the identifier
// and keeping the app's certificate is what makes that requirement — and any
// grant recorded for it — survive every build. The app remains the
// responsible code for every chain where the app still exists (no spawn path
// was changed to disclaim responsibility); this identity is what remains when
// the app has been replaced and the helper is the nearest surviving process.
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
