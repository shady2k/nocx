//go:build linux

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
	"syscall"
	"time"
)

func fail(name string) {
	fmt.Printf("LINUX_NATIVE_FAILURE %s\n", name)
	os.Exit(2)
}

func pass(name string) { fmt.Printf("LINUX_NATIVE_PASS %s\n", name) }

func denied(err error) bool { return errors.Is(err, fs.ErrPermission) }

func main() {
	if len(os.Args) == 3 && os.Args[1] == "child" {
		if _, err := os.ReadFile(os.Args[2]); !denied(err) {
			os.Exit(3)
		}
		return
	}
	if len(os.Args) != 12 {
		fail("arguments")
	}
	work, readOnly, outside := os.Args[1], os.Args[2], os.Args[3]
	hostSocket, currentHelper, oldHelper, coordinator := os.Args[4], os.Args[5], os.Args[6], os.Args[7]
	tcpAddress, runtimeRoot, runtimeHome, projectedWorkspace := os.Args[8], os.Args[9], os.Args[10], os.Args[11]

	// #nosec G304 -- explicit fixture path supplied by the owning smoke harness.
	if data, err := os.ReadFile(filepath.Join(readOnly, "readable")); err != nil || string(data) != "read-only-fixture" {
		fail("workspace_ro_read")
	}
	if err := os.WriteFile(filepath.Join(readOnly, "write-denied"), []byte("x"), 0o600); !denied(err) {
		fail("workspace_ro_write_denied")
	}
	if err := os.WriteFile(filepath.Join(work, "write-allowed"), []byte("ok"), 0o600); err != nil {
		fail("workspace_rw_write")
	}
	if err := os.Rename(filepath.Join(work, "write-allowed"), filepath.Join(work, "renamed")); err != nil {
		fail("workspace_rw_rename")
	}
	if err := os.Remove(filepath.Join(work, "renamed")); err != nil {
		fail("workspace_rw_unlink")
	}
	// #nosec G304 -- deliberately attempts access outside the native grant.
	if _, err := os.ReadFile(filepath.Join(outside, "denied")); !denied(err) {
		fail("outside_read_denied")
	}
	if err := os.WriteFile(filepath.Join(outside, "write-denied"), []byte("x"), 0o600); !denied(err) {
		fail("outside_write_denied")
	}
	pass("workspace_ro_rw_and_outside_denied")

	link := filepath.Join(work, "outside-link")
	if err := os.Symlink(filepath.Join(outside, "denied"), link); err != nil {
		fail("symlink_fixture")
	}
	// #nosec G304 -- deliberately tests the fixture's escaping symlink.
	if _, err := os.ReadFile(link); !denied(err) {
		fail("symlink_escape_denied")
	}
	if err := os.Rename(filepath.Join(readOnly, "readable"), filepath.Join(work, "ro-renamed")); !denied(err) {
		fail("ro_rename_denied")
	}
	if err := os.Link(filepath.Join(readOnly, "hardlink-source"), filepath.Join(work, "hardlink-escape")); !denied(err) && !errors.Is(err, syscall.EXDEV) {
		fail("hardlink_boundary_denied")
	}
	if _, err := os.Lstat(filepath.Join(work, "hardlink-escape")); !errors.Is(err, fs.ErrNotExist) {
		fail("hardlink_boundary_created_destination")
	}
	pass("symlink_rename_hardlink_boundaries")

	self, err := os.Executable()
	if err != nil {
		fail("fork_exec_inherits_policy")
	}
	child := exec.Command(self, "child", filepath.Join(outside, "denied")) // #nosec G204 -- self is the private proof probe itself.
	if childErr := child.Run(); childErr != nil {
		fail("fork_exec_inherits_policy")
	}
	pass("fork_exec_inherits_policy")

	for label, address := range map[string]string{
		"workspace_socket": hostSocket, "current_helper_socket": currentHelper,
		"old_helper_socket": oldHelper, "coordinator_socket": coordinator,
	} {
		conn, dialErr := net.DialTimeout("unix", address, time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		if !denied(dialErr) {
			fail(label + "_ipc_boundary")
		}
	}
	pass("host_and_old_current_ipc_denied")

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
	if err = <-accepted; err != nil {
		_ = listener.Close()
		fail("runtime_unix_accept")
	}
	_ = listener.Close()
	conn, err = net.DialTimeout("tcp", tcpAddress, time.Second)
	if err != nil {
		fail("tcp_unchanged")
	}
	_ = conn.Close()
	pass("runtime_unix_and_tcp_unchanged")

	if os.Getenv("HOME") != runtimeHome || os.Getenv("NOCX_SANDBOX") != "filesystem" ||
		!strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), runtimeRoot) || !strings.HasPrefix(os.Getenv("TMPDIR"), runtimeRoot) {
		fail("isolated_home_environment")
	}
	if target, err := os.Readlink(filepath.Join(runtimeHome, filepath.Base(projectedWorkspace))); err != nil || target != projectedWorkspace {
		fail("explicit_workspace_projection")
	}
	if _, err := os.Stat(filepath.Join(runtimeHome, ".host-secret")); !errors.Is(err, fs.ErrNotExist) {
		fail("host_dotfiles_not_copied")
	}
	pass("isolated_home_and_projection")
	fmt.Println("LINUX_NATIVE_PROBE_COMPLETE")
}
