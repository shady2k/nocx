package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewOSPaths_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-specific test")
	}

	home := "/home/testuser"

	t.Run("respects XDG overrides", func(t *testing.T) {
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
		t.Setenv("XDG_DATA_HOME", "/xdg/data")
		t.Setenv("XDG_CACHE_HOME", "/xdg/cache")

		p, err := newOSPaths("nocx")
		if err != nil {
			t.Fatalf("newOSPaths() error: %v", err)
		}

		if p.ConfigDir() != filepath.Join("/xdg/config", "nocx") {
			t.Errorf("ConfigDir: got %q, want %q", p.ConfigDir(), filepath.Join("/xdg/config", "nocx"))
		}
		if p.DataDir() != filepath.Join("/xdg/data", "nocx") {
			t.Errorf("DataDir: got %q, want %q", p.DataDir(), filepath.Join("/xdg/data", "nocx"))
		}
		if p.CacheDir() != filepath.Join("/xdg/cache", "nocx") {
			t.Errorf("CacheDir: got %q, want %q", p.CacheDir(), filepath.Join("/xdg/cache", "nocx"))
		}
	})

	t.Run("falls back to HOME defaults", func(t *testing.T) {
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("XDG_CACHE_HOME", "")

		p, err := newOSPaths("nocx")
		if err != nil {
			t.Fatalf("newOSPaths() error: %v", err)
		}

		if p.ConfigDir() != filepath.Join(home, ".config", "nocx") {
			t.Errorf("ConfigDir: got %q, want %q", p.ConfigDir(), filepath.Join(home, ".config", "nocx"))
		}
		if p.DataDir() != filepath.Join(home, ".local", "share", "nocx") {
			t.Errorf("DataDir: got %q, want %q", p.DataDir(), filepath.Join(home, ".local", "share", "nocx"))
		}
		if p.CacheDir() != filepath.Join(home, ".cache", "nocx") {
			t.Errorf("CacheDir: got %q, want %q", p.CacheDir(), filepath.Join(home, ".cache", "nocx"))
		}
	})

	t.Run("errors when HOME and all XDG vars are unset", func(t *testing.T) {
		// Clear everything — including HOME, which os.UserConfigDir/os.UserCacheDir need.
		// We preserve PATH so the test binary can still find itself.
		toUnset := []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"}
		for _, k := range toUnset {
			t.Setenv(k, "")
			_ = os.Unsetenv(k)
		}

		p, err := newOSPaths("nocx")
		if err == nil {
			t.Errorf("expected error, got nil (paths: config=%q data=%q cache=%q)", p.ConfigDir(), p.DataDir(), p.CacheDir())
		}
		// The error must name the failing role.
		errStr := err.Error()
		if !strings.Contains(errStr, "config") && !strings.Contains(errStr, "data") && !strings.Contains(errStr, "cache") {
			t.Errorf("error should name the failing role, got: %v", err)
		}
	})

	t.Run("errors when only data dir cannot resolve", func(t *testing.T) {
		// Config/Cache resolve via stdlib; data needs HOME or XDG_DATA_HOME.
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "/tmp/cfg")
		t.Setenv("XDG_CACHE_HOME", "/tmp/cch")
		t.Setenv("XDG_DATA_HOME", "")
		_ = os.Unsetenv("HOME")
		_ = os.Unsetenv("XDG_DATA_HOME")

		p, err := newOSPaths("nocx")
		if err == nil {
			t.Errorf("expected error for unresolvable data dir, got nil (paths: config=%q data=%q cache=%q)", p.ConfigDir(), p.DataDir(), p.CacheDir())
		}
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "data") {
			t.Errorf("error should name the failing 'data' role, got: %v", err)
		}
	})

	t.Run("all three methods return distinct roles", func(t *testing.T) {
		t.Setenv("HOME", home)
		p, err := newOSPaths("myapp")
		if err != nil {
			t.Fatalf("newOSPaths() error: %v", err)
		}

		c, d, ch := p.ConfigDir(), p.DataDir(), p.CacheDir()

		if c == d {
			t.Errorf("ConfigDir and DataDir should differ on Linux, both are %q", c)
		}
		if c == ch {
			t.Errorf("ConfigDir and CacheDir should differ on Linux, both are %q", c)
		}
		if d == ch {
			t.Errorf("DataDir and CacheDir should differ on Linux, both are %q", d)
		}

		// All should contain the app name.
		for _, dir := range []string{c, d, ch} {
			if !strings.Contains(dir, "myapp") {
				t.Errorf("path %q should contain app name 'myapp'", dir)
			}
		}
	})
}

func TestNewOSPaths_macOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific test")
	}

	home := "/Users/testuser"

	t.Run("config and data are the same Application Support dir", func(t *testing.T) {
		t.Setenv("HOME", home)
		p, err := newOSPaths("nocx")
		if err != nil {
			t.Fatalf("newOSPaths() error: %v", err)
		}

		wantCD := filepath.Join(home, "Library", "Application Support", "nocx")
		if p.ConfigDir() != wantCD {
			t.Errorf("ConfigDir: got %q, want %q", p.ConfigDir(), wantCD)
		}
		if p.DataDir() != wantCD {
			t.Errorf("DataDir: got %q, want %q", p.DataDir(), wantCD)
		}
	})

	t.Run("cache is under Caches", func(t *testing.T) {
		t.Setenv("HOME", home)
		p, err := newOSPaths("nocx")
		if err != nil {
			t.Fatalf("newOSPaths() error: %v", err)
		}

		wantCh := filepath.Join(home, "Library", "Caches", "nocx")
		if p.CacheDir() != wantCh {
			t.Errorf("CacheDir: got %q, want %q", p.CacheDir(), wantCh)
		}
	})
}
