//go:build !release

package storage

// AppDirName is the profile directory this build resolves. Without
// `-tags release` that is the development profile, kept disjoint from the one
// the installed application owns so a dev stand or an e2e run cannot read or
// clobber it. See appdir.go for why the choice is made by the build.
const AppDirName = shippedAppDirName + "-dev"
