//go:build release

package storage

// AppDirName is the profile directory this build resolves. With `-tags release`
// that is the profile the installed application owns. See appdir.go for why the
// choice is made by the build and why this is the side that carries the tag.
const AppDirName = shippedAppDirName
