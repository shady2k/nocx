// Command hello isolates what libghostty-vt costs a helper artifact: the same
// program with and without the binding, selected by the cgo build tag, so the
// size difference is the library and nothing else. §4 of README.md quotes it.
package main

func main() { run() }
