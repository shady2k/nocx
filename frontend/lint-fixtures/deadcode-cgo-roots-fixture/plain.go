package fixture

// fixtureNotACallback carries the directive, and cgo honours it in no way: the
// go command invokes cgo with the files that import "C", and this one does not.
// The ratchet reads the same place — a cgo file's directive — so this function
// is ordinary Go called by nobody, and stays reported.
//
//export fixtureNotACallback
func fixtureNotACallback() {}
