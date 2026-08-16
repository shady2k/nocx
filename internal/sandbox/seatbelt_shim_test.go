//go:build !windows

package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestSeatbeltShim_WritesReadyByteAndExecs proves the post-profile shim writes
// the readiness byte before replacing itself with the real shell, and returns
// the shim exit code when that exec fails. It runs on every supported
// platform without invoking sandbox-exec: the exec seam is a recorder.
func TestSeatbeltShim_WritesReadyByteAndExecs(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe(fds[:]); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[0]) }()

	var gotArgv0 string
	var gotArgs []string
	code := seatbeltShimMain(fds[1], "/bin/sh", []string{"-i"}, func(argv0 string, argv []string, _ []string) error {
		gotArgv0 = argv0
		gotArgs = argv
		return errors.New("exec refused")
	})
	if code != seatbeltShimExit {
		t.Fatalf("exit code = %d, want %d", code, seatbeltShimExit)
	}

	buf := make([]byte, 1)
	n, err := unix.Read(fds[0], buf)
	if err != nil || n != 1 || buf[0] != 0 {
		t.Fatalf("ready byte = %v (%v), want single zero byte", buf[:n], err)
	}
	if gotArgv0 != "/bin/sh" {
		t.Errorf("exec argv0 = %q, want /bin/sh", gotArgv0)
	}
	want := []string{"/bin/sh", "-i"}
	if len(gotArgs) != len(want) {
		t.Fatalf("exec argv = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("exec argv[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}
}

func TestReadStatus_Ready(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := w.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := readStatus(context.Background(), r, w); err != nil {
		t.Fatalf("readStatus = %v, want nil", err)
	}
}

func TestReadStatus_EOFIsSetupFailure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	// No writer ever reports: readStatus closes the parent's write end, so the
	// read EOFs — the macOS profile-rejected path.
	err = readStatus(context.Background(), r, w)
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("readStatus = %v, want SetupError", err)
	}
}

func TestReadStatus_FailureByte(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, writeErr := w.Write([]byte{1}); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := w.Write([]byte("boom")); writeErr != nil {
		t.Fatal(writeErr)
	}
	err = readStatus(context.Background(), r, w)
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("readStatus = %v, want SetupError", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("readStatus error = %v, want the helper detail", err)
	}
}

func TestParseSeatbeltShimArgv(t *testing.T) {
	t.Run("not a shim invocation", func(t *testing.T) {
		if _, _, _, ok, _ := parseSeatbeltShimArgv([]string{"nocx"}); ok {
			t.Fatal("expected not-a-shim for argv without the marker")
		}
		if _, _, _, ok, _ := parseSeatbeltShimArgv([]string{"nocx", "other"}); ok {
			t.Fatal("expected not-a-shim for argv with a different marker")
		}
	})

	t.Run("marker with insufficient args", func(t *testing.T) {
		_, _, _, ok, code := parseSeatbeltShimArgv([]string{"nocx", seatbeltHelperArg})
		if !ok || code != seatbeltShimExit {
			t.Fatalf("ok = %v, code = %d; want ok=true, code=%d", ok, code, seatbeltShimExit)
		}
	})

	t.Run("invalid status fd", func(t *testing.T) {
		for _, fd := range []string{"x", "2", "-1"} {
			_, _, _, ok, code := parseSeatbeltShimArgv([]string{"nocx", seatbeltHelperArg, fd, "/bin/sh"})
			if !ok || code != seatbeltShimExit {
				t.Fatalf("fd %q: ok = %v, code = %d; want ok=true, code=%d", fd, ok, code, seatbeltShimExit)
			}
		}
	})

	t.Run("empty shell", func(t *testing.T) {
		_, _, _, ok, code := parseSeatbeltShimArgv([]string{"nocx", seatbeltHelperArg, "3", ""})
		if !ok || code != seatbeltShimExit {
			t.Fatalf("ok = %v, code = %d; want ok=true, code=%d", ok, code, seatbeltShimExit)
		}
	})

	t.Run("valid", func(t *testing.T) {
		fd, shell, args, ok, code := parseSeatbeltShimArgv([]string{"nocx", seatbeltHelperArg, "3", "/bin/sh", "-i"})
		if !ok || code != 0 {
			t.Fatalf("ok = %v, code = %d; want ok=true, code=0", ok, code)
		}
		if fd != 3 || shell != "/bin/sh" || len(args) != 1 || args[0] != "-i" {
			t.Fatalf("got fd=%d shell=%q args=%v", fd, shell, args)
		}
	})
}
