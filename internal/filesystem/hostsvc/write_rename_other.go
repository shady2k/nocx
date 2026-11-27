//go:build !linux && !darwin

package hostsvc

// renameNoReplace installs the temporary only if the target name is still
// free. This platform has no atomic form of that, so the portable pair is the
// whole answer: a hard link refuses an existing name, and removing the
// temporary afterwards leaves the target with one link.
func renameNoReplace(oldpath, newpath string) error {
	return renameNoReplaceFallback(oldpath, newpath)
}
