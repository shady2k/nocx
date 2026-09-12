//go:build !cgo

package main

// run does nothing: this is the same program with the binding compiled out, so
// that the two sizes differ by the library alone.
func run() {}
