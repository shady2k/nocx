package discovery_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDiscoveryDependencyGraphUsesTheFarHelper(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, tags := range []string{"", "nocx_local_ssh"} {
			t.Run(goos+"/"+tags, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./internal/discovery")
				cmd.Dir = "../.."
				cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=-tags="+tags)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("listing discovery dependencies: %v\n%s", err, out)
				}
				for _, dep := range strings.Fields(string(out)) {
					path := strings.TrimPrefix(dep, "github.com/shady2k/nocx/")
					switch path {
					case "internal/ssh", "internal/helper/sshsvc", "internal/remoteprobe":
						t.Errorf("discovery reaches %s (C24)", dep)
					}
				}
			})
		}
	}
}
