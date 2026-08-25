// Separate module ON PURPOSE (nocx-szb40.1).
//
// This is a measurement harness, not product code. Three candidate VT
// libraries have to be built and run to be compared, and exactly one of
// them — at most — should ever enter the product's go.mod. A nested module
// keeps all three out of `go list ./...`, out of the deadcode ratchet, and
// out of the dependency graph anything ships from.
module nocx/spike/vt-agreement

go 1.24

require github.com/creack/pty v1.1.24
