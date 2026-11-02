// Command check-helper-signature refuses a Mach-O whose code identity is not
// the stable one this product ships (nocx-vh2hr.1.7).
//
// The release build signs the two darwin local helpers with the app's
// certificate and the fixed identifier dev.nocx.helper, then runs this command
// on each signed binary before it is recompressed into the embed. It exists as
// a command rather than a shell grep because the rule it enforces — identifier,
// authority, and "never ad-hoc" — has a unit test (internal/helper/signing),
// and a regex in a workflow has none.
//
// macOS only in practice: codesign is the tool it reads. It builds on every
// platform, which is what lets the check itself be tested here.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/shady2k/nocx/internal/helper/signing"
)

func main() {
	identifier := flag.String("identifier", signing.HelperIdentifier,
		"the code identity the binary must carry")
	authority := flag.String("authority", signing.Authority,
		"the signing authority the binary must carry")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(),
			"usage: %s [-identifier id] [-authority name] <mach-o>\n", os.Args[0])
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := flag.Arg(0)

	// The seal first: an identity printed for a broken signature proves
	// nothing about the bytes the helper will run.
	//nolint:gosec // the binary is the operator's argument; codesign is fixed argv
	if out, err := exec.Command("codesign", "--verify", "--strict", path).CombinedOutput(); err != nil {
		fail("%s does not verify: %v\n%s", path, err, out)
	}
	out, err := exec.Command("codesign", "-d", "--verbose=4", path).CombinedOutput() //nolint:gosec // fixed argv; the binary is the operator's argument
	if err != nil {
		fail("codesign cannot read %s: %v\n%s", path, err, out)
	}
	report := signing.Parse(string(out))
	if err := signing.Check(report, *identifier, *authority); err != nil {
		fail("%s: %v", path, err)
	}
	_, _ = fmt.Printf("%s: Identifier=%s Authority=%s\n",
		path, report.Identifier, strings.Join(report.Authority, ", "))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "check-helper-signature: "+format+"\n", args...)
	os.Exit(1)
}
