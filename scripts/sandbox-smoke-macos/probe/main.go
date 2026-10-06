package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func fail(name string) {
	fmt.Printf("NATIVE_FAIL %s\n", name)
	os.Exit(2)
}

func pass(name string) { fmt.Printf("NATIVE_PASS %s\n", name) }

func permissionDenied(err error) bool { return errors.Is(err, fs.ErrPermission) }

func main() {
	if len(os.Args) == 3 && os.Args[1] == "child" {
		_, err := os.ReadFile(os.Args[2])
		if !permissionDenied(err) {
			os.Exit(3)
		}
		return
	}
	if len(os.Args) != 12 {
		fail("probe_arguments")
	}
	work, readOnly, outside := os.Args[1], os.Args[2], os.Args[3]
	hostSocket, currentHelper, oldHelper, coordinator := os.Args[4], os.Args[5], os.Args[6], os.Args[7]
	tcpAddress, runtimeRoot, runtimeHome, projectedWorkspace := os.Args[8], os.Args[9], os.Args[10], os.Args[11]

	fds := 0
	leaked := false
	for fd := 3; fd < 512; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			continue
		}
		fds++
		var metadata unix.Stat_t
		if unix.Fstat(fd, &metadata) == nil && (metadata.Mode&unix.S_IFMT == unix.S_IFDIR || metadata.Mode&unix.S_IFMT == unix.S_IFREG) {
			leaked = true
		}
		target, err := os.Readlink(fmt.Sprintf("/dev/fd/%d", fd))
		if err == nil && (strings.Contains(target, "sandbox-runners") || strings.Contains(target, "sandbox-runtimes") || strings.Contains(target, "launch-plan")) {
			leaked = true
		}
		if _, err := unix.Getsockname(fd); err == nil {
			leaked = true
		}
	}
	if leaked {
		fail("private_or_socket_descriptor_leak")
	}
	pass(fmt.Sprintf("descriptor_inventory_%d_no_private_or_socket_fds", fds))

	// #nosec G304 -- deliberate read of the parent harness's sandbox boundary fixture.
	if data, err := os.ReadFile(filepath.Join(readOnly, "readable")); err != nil || string(data) != "read-only-fixture" {
		fail("read_only_read")
	}
	if err := os.WriteFile(filepath.Join(readOnly, "write-denied"), []byte("x"), 0o600); !permissionDenied(err) {
		fail("read_only_write_denied")
	}
	// #nosec G304 -- deliberately exercises forbidden access; success is a proof failure.
	if _, err := os.ReadFile(filepath.Join(outside, "denied")); !permissionDenied(err) {
		fail("outside_read_denied")
	}
	if err := os.WriteFile(filepath.Join(outside, "write-denied"), []byte("x"), 0o600); !permissionDenied(err) {
		fail("outside_write_denied")
	}
	pass("outside_read_write_denied")

	created := filepath.Join(work, "write-allowed")
	if err := os.WriteFile(created, []byte("allowed"), 0o600); err != nil {
		fail("workspace_write")
	}
	moved := filepath.Join(work, "renamed")
	if err := os.Rename(created, moved); err != nil {
		fail("workspace_rename")
	}
	if err := os.Remove(moved); err != nil {
		fail("workspace_unlink")
	}
	pass("workspace_rw_operations")

	alias := filepath.Join(work, "escape-link")
	if err := os.Symlink(filepath.Join(outside, "denied"), alias); err != nil {
		fail("symlink_fixture")
	}
	// #nosec G304 -- deliberately exercises the symlink escape fixture; success is a proof failure.
	if _, err := os.ReadFile(alias); !permissionDenied(err) {
		fail("symlink_escape_denied")
	}
	if err := os.Rename(filepath.Join(readOnly, "readable"), filepath.Join(work, "ro-renamed")); !permissionDenied(err) {
		fail("read_only_rename_denied")
	}
	pass("symlink_and_rename_escape_denied")

	self, err := os.Executable()
	// #nosec G204 -- self is os.Executable; fixed child mode tests inherited restrictions.
	if err != nil || exec.Command(self, "child", filepath.Join(outside, "denied")).Run() != nil {
		fail("fork_exec_inherits_policy")
	}
	pass("fork_exec_inherits_policy")

	for name, address := range map[string]string{
		"host_workspace_socket_denied": hostSocket,
		"current_helper_socket_denied": currentHelper,
		"old_helper_socket_denied":     oldHelper,
		"coordinator_discovery_denied": coordinator,
	} {
		conn, dialErr := net.DialTimeout("unix", address, time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		if !permissionDenied(dialErr) {
			fail(name)
		}
		pass(name)
	}

	runtimeSocket := filepath.Join(runtimeRoot, "probe.sock")
	listener, err := net.Listen("unix", runtimeSocket)
	if err != nil {
		fail("runtime_unix_bind")
	}
	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		accepted <- acceptErr
	}()
	conn, err := net.DialTimeout("unix", runtimeSocket, time.Second)
	if err != nil {
		_ = listener.Close()
		fail("runtime_unix_connect")
	}
	_ = conn.Close()
	if acceptErr := <-accepted; acceptErr != nil {
		_ = listener.Close()
		fail("runtime_unix_accept")
	}
	_ = listener.Close()
	pass("runtime_unix_bind_connect_accept")

	conn, err = net.DialTimeout("tcp", tcpAddress, time.Second)
	if err != nil {
		fail("tcp_unchanged")
	}
	_ = conn.Close()
	pass("tcp_unchanged")

	if os.Getenv("HOME") != runtimeHome || os.Getenv("NOCX_SANDBOX") != "filesystem" || os.Getenv("XDG_CONFIG_HOME") == "" || !strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), runtimeRoot) || os.Getenv("TMPDIR") == "" || !strings.HasPrefix(os.Getenv("TMPDIR"), runtimeRoot) {
		fail("isolated_home_environment")
	}
	if target, err := os.Readlink(filepath.Join(runtimeHome, filepath.Base(projectedWorkspace))); err != nil || target != projectedWorkspace {
		fail("explicit_workspace_projection")
	}
	if _, err := os.Stat(filepath.Join(runtimeHome, ".host-secret")); !errors.Is(err, fs.ErrNotExist) {
		fail("host_dotfiles_not_copied")
	}
	pass("isolated_home_and_projection")
	fmt.Println("NATIVE_KERNEL_SMOKE_COMPLETE")
}
