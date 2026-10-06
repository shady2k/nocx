//go:build darwin

package main

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func main() {
	for _, path := range []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty"} {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Printf("NATIVE_PIN_DIAGNOSTIC path=%q stat=%v\n", path, err)
			continue
		}
		identity, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			panic("native stat identity unavailable")
		}
		for _, mode := range []struct {
			name string
			flag int
		}{{"read-only", unix.O_RDONLY}, {"event-only", unix.O_EVTONLY}} {
			fd, openErr := unix.Open(path, mode.flag|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if openErr != nil {
				fmt.Printf("NATIVE_PIN_DIAGNOSTIC path=%q mode=%s open=%v\n", path, mode.name, openErr)
				continue
			}
			var stat syscall.Stat_t
			statErr := syscall.Fstat(fd, &stat)
			fmt.Printf("NATIVE_PIN_DIAGNOSTIC path=%q mode=%s fstat=%v identity_equal=%t character_device=%t\n", path, mode.name, statErr, stat.Dev == identity.Dev && stat.Ino == identity.Ino, stat.Mode&syscall.S_IFMT == syscall.S_IFCHR)
			_ = unix.Close(fd)
		}
	}
}
