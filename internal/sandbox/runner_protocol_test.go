package sandbox

import (
	"os"
	"strings"
	"testing"
)

func runnerTestPlan() RunnerPlan {
	policy := Policy{
		Version: PolicyVersion, Backend: LinuxLandlock, BackendVersion: 9,
		WorkspaceID: "workspace", WorkspaceRoot: "/work", Shell: "/bin/sh", Runner: "/app/nocx-sandbox-runner",
		Runtime: RuntimePaths{Root: "/run/private", Home: "/run/private/home", Config: "/run/private/config", Data: "/run/private/data", Cache: "/run/private/cache", State: "/run/private/state", Temp: "/run/private/tmp"},
		Roots:   []Root{{Path: "/work", Access: ReadWrite, Kind: DirectoryRoot, Provenance: WorkspaceRoot, Identity: FileIdentity{Device: 1, Inode: 2}}},
	}
	_, digest, _ := EncodePolicy(policy)
	return RunnerPlan{Version: RunnerPlanVersion, Policy: policy, Digest: digest, Args: []string{"/bin/sh", "-i"}, RootFDs: []int{8}, WorkspaceFD: 9, KeepFDs: []int{3, 4}}
}

func TestRunnerPlanRoundTripAndRejectsAuthorityTransportChanges(t *testing.T) {
	plan := runnerTestPlan()
	if _, err := EncodeRunnerPlan(plan); err != nil {
		t.Fatalf("valid bounded plan: %v", err)
	}
	mutations := []struct {
		name   string
		change func(*RunnerPlan)
	}{
		{"digest", func(p *RunnerPlan) { p.Digest = strings.Repeat("0", 64) }},
		{"unsupported-backend-version", func(p *RunnerPlan) {
			p.Policy.Backend = LinuxLandlock
			p.Policy.BackendVersion = 3
			_, p.Digest, _ = EncodePolicy(p.Policy)
		}},
		{"duplicate-root-fd", func(p *RunnerPlan) { p.RootFDs = []int{8}; p.WorkspaceFD = 8 }},
		{"fd-before-reserved", func(p *RunnerPlan) { p.RootFDs = []int{7} }},
		{"unlisted-keep-fd", func(p *RunnerPlan) { p.KeepFDs = []int{10} }},
		{"nul-argument", func(p *RunnerPlan) { p.Args[1] = "bad\x00arg" }},
		{"shell-argument-mismatch", func(p *RunnerPlan) { p.Args[0] = "/bin/bash" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			p := runnerTestPlan()
			tc.change(&p)
			if _, err := EncodeRunnerPlan(p); err == nil {
				t.Fatal("accepted malformed or changed runner authority")
			}
		})
	}
}

func TestRunnerPlanAcceptsOnlyFixedDarwinBackendV1(t *testing.T) {
	p := runnerTestPlan()
	p.Policy.Backend = MacOSSeatbelt
	p.Policy.BackendVersion = MacOSBaselineVersion
	_, p.Digest, _ = EncodePolicy(p.Policy)
	p.ProbePath = "/private/tmp/seatbelt-probe"
	p.ProbeSocket = p.Policy.WorkspaceRoot + "/host-probe.sock"
	if _, err := EncodeRunnerPlan(p); err != nil {
		t.Fatalf("valid macOS v1 plan rejected: %v", err)
	}
	p.Policy.BackendVersion++
	_, p.Digest, _ = EncodePolicy(p.Policy)
	if _, err := EncodeRunnerPlan(p); err == nil {
		t.Fatal("accepted unknown macOS backend version")
	}
	p = runnerTestPlan()
	p.Policy.Backend = LinuxLandlock
	p.Policy.BackendVersion = 3
	_, p.Digest, _ = EncodePolicy(p.Policy)
	if _, err := EncodeRunnerPlan(p); err == nil {
		t.Fatal("weakened fixed Linux ABI9 runner plan")
	}
}

func TestReadRunnerPlanRejectsUnknownTrailingAndOversizedDocuments(t *testing.T) {
	plan, err := EncodeRunnerPlan(runnerTestPlan())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"unknown-field", append(append([]byte{}, plan[:len(plan)-1]...), []byte(`,"unexpected":true}`)...)},
		{"trailing-value", append(append([]byte{}, plan...), []byte(` {}`)...)},
		{"duplicate-field", []byte(strings.Replace(string(plan), `"version":1`, `"version":1,"version":1`, 1))},
		{"oversized", []byte(strings.Repeat("x", MaxRunnerPlanBytes+1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "runner-plan-*")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(tc.data); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			_, readErr := ReadRunnerPlan(int(f.Fd()))
			_ = f.Close()
			if readErr == nil {
				t.Fatal("accepted malformed runner envelope")
			}
		})
	}
}

func TestRunnerPlanBoundsArgumentCountAndPayload(t *testing.T) {
	p := runnerTestPlan()
	p.Args = make([]string, MaxRunnerArgs+1)
	p.Args[0] = p.Policy.Shell
	if _, err := EncodeRunnerPlan(p); err == nil {
		t.Fatal("accepted excessive argv count")
	}
	p = runnerTestPlan()
	p.Args = []string{p.Policy.Shell, strings.Repeat("a", MaxRunnerArgsBytes)}
	if _, err := EncodeRunnerPlan(p); err == nil {
		t.Fatal("accepted excessive argv bytes")
	}
}
