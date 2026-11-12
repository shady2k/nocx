//go:build darwin && cgo

package endpoint

/*
#include <spawn.h>
#include <fcntl.h>
#include <stdlib.h>
#include <unistd.h>
#include <dlfcn.h>
#include <errno.h>

// Private libSystem API used by terminal emulators to give a child its own
// responsibility record. setsid alone changes no responsible-code attribution.
typedef int (*disclaim_fn)(posix_spawnattr_t *, int);

static int spawn_helper(const char *path, char **env, pid_t *pid) {
 posix_spawnattr_t attr;
 posix_spawn_file_actions_t files;
 int err = posix_spawnattr_init(&attr);
 if (err) return err;
 disclaim_fn disclaim = (disclaim_fn)dlsym(RTLD_DEFAULT, "responsibility_spawnattrs_setdisclaim");
 if (!disclaim) { err = ENOSYS; goto attr_done; }
 err = disclaim(&attr, 1);
 if (err) goto attr_done;
 err = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETSID | POSIX_SPAWN_CLOEXEC_DEFAULT);
 if (err) goto attr_done;
 err = posix_spawn_file_actions_init(&files);
 if (err) goto attr_done;
 for (int fd = 0; fd < 3; fd++) {
  err = posix_spawn_file_actions_addopen(&files, fd, "/dev/null", fd == 0 ? O_RDONLY : O_WRONLY, 0);
  if (err) goto files_done;
 }
 char *argv[] = {(char *)path, "serve", NULL};
 err = posix_spawn(pid, path, &files, &attr, argv, env);
files_done:
 posix_spawn_file_actions_destroy(&files);
attr_done:
 posix_spawnattr_destroy(&attr);
 return err;
}
*/
import "C"

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// startHelper is the existing endpoint start boundary, with Darwin's separate
// responsibility record (ADR references the reason). Never inherit the app's
// responsible code: its on-disk executable can be replaced while this daemon
// and its shells remain alive.
func startHelper(exe string) (func() error, error) {
	path := C.CString(exe)
	defer C.free(unsafe.Pointer(path))
	env := os.Environ()
	memory := C.malloc(C.size_t(len(env)+1) * C.size_t(unsafe.Sizeof(uintptr(0))))
	defer C.free(memory)
	entries := unsafe.Slice((**C.char)(memory), len(env)+1)
	for i, value := range env {
		entries[i] = C.CString(value)
		defer C.free(unsafe.Pointer(entries[i]))
	}
	entries[len(env)] = nil
	var pid C.pid_t
	if code := C.spawn_helper(path, (**C.char)(memory), &pid); code != 0 {
		return nil, syscall.Errno(code)
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		return nil, err
	}
	return func() error {
		state, err := process.Wait()
		if err != nil {
			return err
		}
		if !state.Success() {
			return &exec.ExitError{ProcessState: state}
		}
		return nil
	}, nil
}
