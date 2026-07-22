package shellintegration

import (
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func testLogger() log.Logger {
	return log.NewSlogAdapter(nil)
}

func TestValidateCwd_Localhost(t *testing.T) {
	s := NewStub(testLogger())

	tests := []struct {
		name    string
		host    string
		path    string
		wantErr bool
	}{
		{"empty host, root path", "", "/", false},
		{"empty host, home path", "", "/home/user", false},
		{"localhost host", "localhost", "/tmp", false},
		{"local hostname", osHostname(t), "/var/log", false},
		{"tilde path (home)", "", "~", false},
		{"non-local host", "remote.example.com", "/tmp", true},
		{"empty path", "", "", true},
		{"relative path", "", "documents", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := s.ValidateCwd(tt.host, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %+v", info)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if info.Host != tt.host {
				t.Errorf("host: want %q, got %q", tt.host, info.Host)
			}
			if info.Path != tt.path {
				t.Errorf("path: want %q, got %q", tt.path, info.Path)
			}
		})
	}
}

func TestValidateCwd_CustomHostFunc(t *testing.T) {
	s := &Stub{
		log:    testLogger(),
		isHost: func(host string) bool { return host == "custombox" },
	}

	_, err := s.ValidateCwd("custombox", "/etc")
	if err != nil {
		t.Errorf("custom host should pass: %v", err)
	}

	_, err = s.ValidateCwd("localhost", "/etc")
	if err == nil {
		t.Error("localhost should fail with custom host func")
	}
}

func osHostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Skipf("cannot get hostname: %v", err)
	}
	return h
}
