//go:build linux

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/deploy/artifacts"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/version"
	"golang.org/x/sys/unix"
)

const oldHelperRevision = "7e60042546581fde39035dce29110cd3e6a684ac"

func must(err error, operation string) {
	if err != nil {
		var refusal *client.RefusalError
		if errors.As(err, &refusal) {
			var detail sandbox.BuildError
			_ = json.Unmarshal(refusal.Details, &detail)
			fmt.Printf("LINUX_NATIVE_FAILURE operation=%q code=%q field=%q index=%d\n", operation, refusal.Code, detail.Field, detail.Index)
		}
		panic(operation)
	}
}

func report(name string) { fmt.Println("LINUX_NATIVE_PASS " + name) }

func main() {
	if runtime.GOOS != "linux" || os.Getenv("NOCX_SANDBOX_SMOKE_MANDATORY") != "1" {
		panic("mandatory Linux sandbox smoke must run through make sandbox-smoke-linux")
	}
	if len(os.Args) != 2 || (os.Args[1] != "source" && os.Args[1] != "packaged") {
		panic("mandatory Linux smoke requires an explicit source or packaged lane")
	}
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, 1)
	if errno != 0 || abi < 9 {
		panic(fmt.Sprintf("mandatory Linux smoke requires Landlock ABI 9; detected ABI %d errno %v", abi, errno))
	}
	lane := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	fmt.Printf("LINUX_NATIVE_PROOF_LANE %s\nLINUX_NATIVE_LANDLOCK_ABI %d\n", lane, abi)
	root, err := os.MkdirTemp("/tmp", "nxsl-")
	must(err, "create private proof root")
	root, err = filepath.EvalSymlinks(root)
	must(err, "canonicalize private proof root")
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			panic("private Linux proof cleanup failed")
		}
	}()
	hostHome := filepath.Join(root, "host-home")
	work := filepath.Join(hostHome, "workspace")
	readOnly := filepath.Join(hostHome, "read-only")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{hostHome, work, readOnly, outside, filepath.Join(hostHome, ".nocx")} {
		must(os.MkdirAll(path, 0o700), "create isolated proof fixture")
	}
	must(os.WriteFile(filepath.Join(readOnly, "readable"), []byte("read-only-fixture"), 0o600), "create read-only sentinel")
	must(os.WriteFile(filepath.Join(readOnly, "hardlink-source"), []byte("hardlink-fixture"), 0o600), "create hardlink sentinel")
	must(os.WriteFile(filepath.Join(outside, "denied"), []byte("outside-fixture"), 0o600), "create outside sentinel")
	must(os.WriteFile(filepath.Join(hostHome, ".host-secret"), []byte("not projected"), 0o600), "create host home sentinel")

	probe := filepath.Join(work, ".native-probe")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", probe, "./scripts/sandbox-smoke-linux/probe") // #nosec G204 -- fixed proof package and private output.
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		panic(fmt.Sprintf("build Linux native probe: %s", output))
	}
	currentBinary := filepath.Join(root, lane+"-local-helper")
	var helperHash string
	if lane == "source" {
		build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-linkmode=external", "-tags", "nocx_local_ssh", "-o", currentBinary, "./cmd/nocx-helper") // #nosec G204 -- fixed source helper package and private output.
		build.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if output, buildErr := build.CombinedOutput(); buildErr != nil {
			panic(fmt.Sprintf("build source helper: %s", output))
		}
	} else {
		helperHash = extractPackagedHelper(currentBinary, true)
		verifyRemoteOnlyArtifact(ctx, root, work)
	}
	oldBinary := filepath.Join(root, "old-helper")
	buildOldHelper(ctx, oldBinary)
	currentClient, currentSocket, stopCurrent := startHelper(ctx, currentBinary, hostHome, helperHash)
	defer stopCurrent()
	oldHome := filepath.Join(root, "old-home")
	must(os.MkdirAll(oldHome, 0o700), "create old helper home")
	oldClient, oldSocket, stopOld := startHelper(ctx, oldBinary, oldHome, "")
	defer stopOld()
	oldSession, err := oldClient.Spawn(ctx, proto.SpawnParams{Workspace: "old-helper", Cwd: work, Cols: 80, Rows: 24})
	must(err, "spawn old-generation ordinary session")
	defer closeSession(oldClient, oldSession.HostSessionID)

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err, "listen host TCP control")
	defer func() { _ = tcpListener.Close() }()
	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		conn, acceptErr := tcpListener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	workspaceSocket := filepath.Join(work, "host-created.sock")
	workspaceListener, err := net.Listen("unix", workspaceSocket)
	must(err, "listen host-created workspace socket")
	defer func() { _ = workspaceListener.Close() }()
	token := make([]byte, 32)
	_, err = rand.Read(token)
	must(err, "generate coordinator test token")
	server, err := coordinator.NewServer(coordinator.Config{
		Dir: filepath.Join(work, "coordinator-runtime"), Build: coordinator.Build{Version: version.Version, Commit: version.Commit},
		Backend: proofBackend{address: tcpListener.Addr().String(), token: hex.EncodeToString(token)},
		Peers:   coordinator.SystemPeerCredentials{}, Owner: coordinator.SystemPathOwner{}, SelfUID: coordinator.SelfUID(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	must(err, "configure production coordinator discovery endpoint")
	must(server.Start(), "start production coordinator discovery endpoint")
	defer func() { _ = server.Close() }()
	coordinatorSocket := server.SocketPath()
	discovery, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: coordinatorSocket,
		Self:   coordinator.ClientIdentity{Version: version.Version, Commit: version.Commit, Protocol: coordinator.ProtocolVersion},
		Dialer: coordinator.SystemDialer{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	must(err, "configure production coordinator discovery client")
	if _, err = discovery.Hello(ctx); err != nil {
		panic("host coordinator discovery did not answer before sandbox launch")
	}

	source, err := currentClient.Spawn(ctx, proto.SpawnParams{Workspace: "ws-linux-proof", Cwd: work, Cols: 80, Rows: 24})
	must(err, "spawn ordinary current helper source session")
	defer closeSession(currentClient, source.HostSessionID)
	key := make([]byte, 32)
	_, err = rand.Read(key)
	must(err, "generate ephemeral ContentDB key")
	db, err := content.Open(ctx, content.Config{
		Path: filepath.Join(root, "content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 16 << 20, DiskCeilingBytes: 64 << 20, CompactionFloor: .8},
		Logger: nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil))),
	})
	must(err, "open isolated production ContentDB")
	defer func() { _ = db.Close() }()
	_, err = db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-linux-proof", Name: "Linux smoke"},
		content.Tab{ID: "tab-linux-proof", WorkspaceID: "ws-linux-proof", Layout: content.LayoutRow},
		content.Pane{ID: "pane-linux-proof", TabID: "tab-linux-proof", Cwd: work, Kind: content.PaneLocal, SizeShare: 1})
	must(err, "persist production workspace")
	identity := content.HelperIdentity{Generation: source.HostSessionID.Generation, SessionID: source.HostSessionID.Session}
	must(db.Ledger().CreateSession(ctx, content.Session{
		ID: identity.SessionID, WorkspaceID: "ws-linux-proof", Host: identity.Host,
		Account: identity.Account, Generation: identity.Generation, PaneID: "pane-linux-proof",
	}), "persist source helper identity")

	invalid, err := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "linux-invalid-prepare", LaunchID: "linux-invalid-launch", Mode: proto.SandboxEnforce,
		Workspace: "ws-linux-proof", Cwd: work, Enforce: &proto.SandboxEnforceIntent{
			StandardRevision:  1,
			ProfileProvenance: sandbox.StandardRoot, Profile: sandbox.ProfileRoots{ReadOnlyDirs: []string{filepath.Join(outside, "missing")}},
		},
	})
	if err == nil {
		_ = currentClient.SandboxDiscard(ctx, invalid.Ticket)
		panic("malformed Enforce prepare was accepted")
	}
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != "sandbox_prepare_failed" || !strings.Contains(string(refusal.Details), "invalid_root") {
		must(err, "malformed prepare did not report invalid_root")
	}
	report("MALFORMED_PREPARE_REFUSED_NO_FALLBACK")
	entries := mustSessions(currentClient, ctx)
	if len(entries) != 1 || !containsSession(entries, source.HostSessionID) {
		panic("malformed prepare disturbed the ordinary source or created a fallback")
	}

	failureDB, err := content.Open(ctx, content.Config{
		Path: filepath.Join(root, "failure-content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 16 << 20, DiskCeilingBytes: 64 << 20, CompactionFloor: .8},
		Logger: nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil))),
	})
	must(err, "open runner-failure ContentDB")
	defer func() { _ = failureDB.Close() }()
	_, err = failureDB.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-linux-failure", Name: "runner failure"},
		content.Tab{ID: "tab-linux-failure", WorkspaceID: "ws-linux-failure", Layout: content.LayoutRow},
		content.Pane{ID: "pane-linux-failure", TabID: "tab-linux-failure", Cwd: work, Kind: content.PaneLocal, SizeShare: 1})
	must(err, "persist runner-failure workspace")
	must(failureDB.Ledger().CreateSession(ctx, content.Session{
		ID: identity.SessionID, WorkspaceID: "ws-linux-failure",
		Host: identity.Host, Account: identity.Account, Generation: identity.Generation, PaneID: "pane-linux-failure",
	}),
		"persist runner-failure source identity")
	failedPrepare, err := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "linux-runner-failure", LaunchID: "linux-runner-failure-launch", Mode: proto.SandboxEnforce,
		Workspace: "ws-linux-failure", Cwd: work, Enforce: &proto.SandboxEnforceIntent{
			StandardRevision: 1, WorkspaceRevision: 0, ProfileProvenance: sandbox.StandardRoot,
			Profile: sandbox.ProfileRoots{ReadOnlyDirs: []string{readOnly}},
		},
	})
	must(err, "prepare runner-failure scenario")
	failedGrant, err := failureDB.Launches().Prepare(ctx, content.LaunchPrepare{
		ID: failedPrepare.LaunchID, PaneID: "pane-linux-failure",
		WorkspaceID: "ws-linux-failure", Source: identity, TargetGeneration: identity.Generation, StandardRevision: 1,
		WorkspaceRevision: 0, Mode: content.LaunchEnforce, Policy: failedPrepare.Policy,
		PolicyDigest: failedPrepare.Digest, PolicyVersion: sandbox.PolicyVersion,
	})
	must(err, "persist runner-failure Enforce grant")
	if failedGrant.GrantID == nil {
		panic("runner-failure scenario has no persisted grant")
	}
	runnerPath := failedPrepare.Policy.Runner
	runnerInfo, err := os.Stat(runnerPath)
	must(err, "stat embedded runner before failure injection")
	runnerBytes, err := os.ReadFile(runnerPath) // #nosec G304 -- runner belongs to this isolated helper generation; its content hash is verified below.
	must(err, "read embedded runner before failure injection")
	runnerDigest := sha256.Sum256(runnerBytes)
	if filepath.Base(runnerPath) != hex.EncodeToString(runnerDigest[:]) {
		panic("embedded runner installation is not keyed by its content hash")
	}
	must(os.WriteFile(runnerPath, []byte("not an executable runner"), 0o700), "corrupt only the installed runner bytes") // #nosec G306 -- executable fixture, not secret storage.
	restoreRunner := func() {
		must(os.WriteFile(runnerPath, runnerBytes, runnerInfo.Mode().Perm()), "restore installed runner")
	}
	defer restoreRunner()
	if mutated, statErr := os.Stat(runnerPath); statErr != nil || !os.SameFile(runnerInfo, mutated) {
		panic("runner failure fixture replaced the installed runner object")
	}
	_, launchErr := currentClient.SandboxLaunch(ctx, proto.SandboxLaunchParams{
		Ticket:      failedPrepare.Ticket,
		OperationID: failedPrepare.OperationID, LaunchID: failedPrepare.LaunchID, Mode: proto.SandboxEnforce,
		Grant: &proto.SandboxGrantBinding{ID: *failedGrant.GrantID, Digest: failedPrepare.Digest, Version: sandbox.PolicyVersion},
		Shape: proto.SandboxLaunchShape{Cols: 80, Rows: 24},
	})
	restoreRunner()
	if !errors.As(launchErr, &refusal) || refusal.Code != "sandbox_launch_failed" {
		must(launchErr, "invalid runner did not fail through native launch refusal")
	}
	entries = mustSessions(currentClient, ctx)
	if len(entries) != 1 || !containsSession(entries, source.HostSessionID) {
		panic("runner failure created an ordinary fallback or disturbed the source")
	}
	report("RUNNER_FAILURE_REFUSED_NO_ORDINARY_FALLBACK")
	refusal = nil

	prepared, err := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "linux-native-operation", LaunchID: "linux-native-launch", Mode: proto.SandboxEnforce,
		Workspace: "ws-linux-proof", Cwd: work, Enforce: &proto.SandboxEnforceIntent{
			StandardRevision:  1,
			WorkspaceRevision: 0, ProfileProvenance: sandbox.StandardRoot,
			Profile: sandbox.ProfileRoots{ReadOnlyDirs: []string{readOnly}},
		},
	})
	must(err, "prepare Linux ABI9 policy through actual helper")
	if prepared.Policy == nil || prepared.Digest == "" || prepared.Policy.Backend != sandbox.LinuxLandlock || prepared.Policy.BackendVersion != 9 {
		panic("helper did not prepare Linux Landlock ABI9 policy")
	}
	if !filepath.IsAbs(prepared.Policy.Runner) {
		panic("helper policy has no installed embedded runner")
	}
	launch, err := db.Launches().Prepare(ctx, content.LaunchPrepare{
		ID: prepared.LaunchID, PaneID: "pane-linux-proof", WorkspaceID: "ws-linux-proof",
		Source: identity, TargetGeneration: identity.Generation, StandardRevision: 1, WorkspaceRevision: 0,
		Mode: content.LaunchEnforce, Policy: prepared.Policy, PolicyDigest: prepared.Digest, PolicyVersion: sandbox.PolicyVersion,
	})
	must(err, "persist immutable production Enforce grant")
	if launch.GrantID == nil {
		panic("ContentDB failed to mint persisted Enforce grant")
	}
	candidate, err := currentClient.SandboxLaunch(ctx, proto.SandboxLaunchParams{
		Ticket: prepared.Ticket, OperationID: prepared.OperationID,
		LaunchID: prepared.LaunchID, Mode: proto.SandboxEnforce,
		Grant: &proto.SandboxGrantBinding{ID: *launch.GrantID, Digest: prepared.Digest, Version: sandbox.PolicyVersion},
		Shape: proto.SandboxLaunchShape{Cols: 120, Rows: 40},
	})
	must(err, "launch policy under exact persisted grant")
	defer closeSession(currentClient, candidate.HostSessionID)
	binding, err := currentClient.SandboxGet(ctx, candidate.HostSessionID)
	must(err, "read live Linux grant binding")
	if binding.Mode != string(proto.SandboxEnforce) || binding.Enforcement != "enforced" || binding.Grant == nil ||
		binding.Grant.ID != *launch.GrantID || binding.Grant.Digest != prepared.Digest {
		panic("live helper grant differs from persisted ContentDB grant")
	}
	report(strings.ToUpper(lane) + "_EMBEDDED_RUNNER_AND_PERSISTED_GRANT_BOUND")

	attachment, err := currentClient.Attach(ctx, proto.AttachParams{
		Subscriber: "aabbccddeeff00112233445566778899",
		Session:    proto.HostSessionID{Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session},
		Offset:     proto.StreamOffset(candidate.Window.Base), Fresh: true, RequestWrite: true,
	})
	must(err, "attach to actual restricted PTY")
	defer func() { _ = attachment.Close() }()
	command := strings.Join([]string{
		probe, shellQuote(work), shellQuote(readOnly), shellQuote(outside), shellQuote(workspaceSocket),
		shellQuote(currentSocket), shellQuote(oldSocket), shellQuote(coordinatorSocket), shellQuote(tcpListener.Addr().String()),
		shellQuote(prepared.Policy.Runtime.Root), shellQuote(prepared.Policy.Runtime.Home), shellQuote(prepared.Policy.WorkspaceRoot),
	}, " ") + "\n"
	if _, err = attachment.Write([]byte(command)); err != nil {
		panic("write Linux probe command")
	}
	if err = waitForMarker(ctx, attachment, "LINUX_NATIVE_PROBE_COMPLETE"); err != nil {
		panic(fmt.Sprintf("native Linux policy probe failed: %v", err))
	}
	diagnosticPath := filepath.Join(outside, "diagnostic-denied")
	must(os.WriteFile(diagnosticPath, []byte("denial fixture"), 0o600), "write diagnostic denial fixture")
	_, err = attachment.Write([]byte(shellQuote("/bin/cat") + " " + shellQuote(diagnosticPath) + " >/dev/null 2>&1\n"))
	if err != nil {
		panic("request actual Landlock denial for diagnostic observer")
	}
	diagnostic := waitForLinuxDenial(ctx, currentClient, candidate.HostSessionID, prepared.LaunchID, diagnosticPath)
	reserved, err := currentClient.SandboxAccessReserve(ctx, proto.SandboxAccessReserveParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session,
		}, EventID: diagnostic.ID,
		Revision: diagnostic.Revision, Decision: sandbox.DecisionAllowRO,
	})
	must(err, "reserve real Linux diagnostic receipt")
	if reserved.LaunchID != prepared.LaunchID || reserved.Record.ID != diagnostic.ID || reserved.Record.State != sandbox.DiagnosticPending {
		panic("helper reserved a different Linux denial receipt")
	}
	finished, err := currentClient.SandboxAccessFinish(ctx, proto.SandboxAccessFinishParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session,
		}, EventID: diagnostic.ID,
		Reservation: reserved.Reservation, Committed: false,
	})
	must(err, "abort Linux diagnostic receipt reservation")
	if finished.LaunchID != prepared.LaunchID || finished.Record.ID != diagnostic.ID || finished.Record.State != sandbox.DiagnosticUnresolved {
		panic("Linux denial receipt was not retained as unresolved")
	}
	retained, err := currentClient.SandboxAccessList(ctx, proto.SandboxAccessListParams{Session: proto.HostSessionID{
		Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session,
	}, Limit: 200})
	must(err, "read real Linux diagnostic inbox")
	found := false
	for _, record := range retained.Inbox.Records {
		if record.ID == diagnostic.ID && record.Source == sandbox.DiagnosticLinuxSeccomp &&
			record.Precision == sandbox.PrecisionAttempted && record.Prediction == sandbox.PredictionDenied &&
			record.PathKnown && record.Path == diagnosticPath && record.State == sandbox.DiagnosticUnresolved {
			found = true
		}
	}
	if !found || retained.LaunchID != prepared.LaunchID || retained.Inbox.Observer != sandbox.ObserverActive {
		panic("real Linux attempted-denial metadata/receipt was not correlated and retained")
	}
	after, err := currentClient.SandboxGet(ctx, candidate.HostSessionID)
	must(err, "read grant after Linux diagnostic processing")
	if after.Grant == nil || after.Grant.ID != binding.Grant.ID || after.Grant.Digest != binding.Grant.Digest || after.Enforcement != binding.Enforcement {
		panic("diagnostic processing mutated the live Enforce grant")
	}
	if _, err = attachment.Write([]byte("if " + shellQuote("/bin/cat") + " " + shellQuote(diagnosticPath) +
		" >/dev/null 2>&1; then echo LINUX_DIAGNOSTIC_POLICY_CHANGED; else echo LINUX_DIAGNOSTIC_POLICY_UNCHANGED; fi\n")); err != nil {
		panic("verify denial remains after receipt processing")
	}
	if err = waitForMarker(ctx, attachment, "LINUX_DIAGNOSTIC_POLICY_UNCHANGED"); err != nil {
		panic(fmt.Sprintf("native denial-after-diagnostics proof failed: %v", err))
	}
	report("REAL_LINUX_DENIAL_METADATA_RECEIPT_AND_GRANT_UNCHANGED")
	if !containsSession(mustSessions(currentClient, ctx), source.HostSessionID) {
		panic("ordinary current source session was lost or fallback was created")
	}
	if !containsSession(mustSessions(oldClient, ctx), oldSession.HostSessionID) {
		panic("ordinary old helper session was lost")
	}
	if err := waitAccept(tcpDone, time.Second); err != nil {
		panic("host TCP control did not remain accessible")
	}
	report("OLD_CURRENT_IPC_BOUNDARIES_AND_ORDINARY_SESSIONS")
	fmt.Printf("LINUX_NATIVE_%s_PROOF_COMPLETE\n", strings.ToUpper(lane))
}

func extractPackagedHelper(destination string, localVariant bool) string {
	platform := deploy.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	var compressed []byte
	var expected string
	var err error
	if localVariant {
		local, ok := artifacts.DefaultSource.(deploy.LocalArtifactSource)
		if !ok {
			panic("packaged local helper source is unavailable")
		}
		compressed, expected, err = local.LocalArtifact(platform)
	} else {
		compressed, expected, err = artifacts.DefaultSource.Artifact(platform)
	}
	must(err, "read embedded packaged helper")
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	must(err, "open packaged local helper archive")
	binary, err := io.ReadAll(io.LimitReader(reader, 512<<20))
	must(err, "read packaged local helper")
	must(reader.Close(), "close packaged helper archive")
	digest := sha256.Sum256(binary)
	if hex.EncodeToString(digest[:]) != expected {
		panic("packaged helper digest mismatch")
	}
	must(os.WriteFile(destination, binary, 0o700), "materialize packaged helper") // #nosec G306 -- executable artifact with verified content hash in a private fixture directory.
	return expected
}

func verifyRemoteOnlyArtifact(ctx context.Context, root, work string) {
	binary := filepath.Join(root, "remote-only-helper")
	hash := extractPackagedHelper(binary, false)
	lane, _, stop := startHelper(ctx, binary, filepath.Join(root, "remote-only-home"), hash)
	defer stop()
	_, err := lane.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "remote-artifact-proof", LaunchID: "remote-artifact-proof",
		Mode: proto.SandboxEnforce, Workspace: "remote-artifact-proof", Cwd: work,
		Enforce: &proto.SandboxEnforceIntent{Profile: sandbox.ProfileRoots{}, Delta: sandbox.ProfileRoots{}, ProfileProvenance: sandbox.StandardRoot},
	})
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != "sandbox_unsupported" {
		panic("remote-only packaged helper accepted local native preparation")
	}
	entries, err := lane.Sessions(ctx)
	must(err, "read remote-only helper inventory")
	if len(entries) != 0 {
		panic("remote-only sandbox refusal created a process")
	}
	report("REMOTE_ONLY_ARTIFACT_REFUSES_LOCAL_SANDBOX_WITHOUT_FALLBACK")
}

func buildOldHelper(ctx context.Context, destination string) {
	root, err := os.MkdirTemp("", "nocx-old-linux-helper-")
	must(err, "create old helper source directory")
	defer func() { must(os.RemoveAll(root), "remove isolated old-helper build directory") }()
	archive := exec.CommandContext(ctx, "git", "-C", repositoryRoot(), "archive", "--format=tar", oldHelperRevision) // #nosec G204 -- fixed repository revision.
	pipe, err := archive.StdoutPipe()
	must(err, "open old source archive")
	must(archive.Start(), "start old helper source archive")
	tar := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", root) // #nosec G204 -- private temporary extraction.
	tar.Stdin = pipe
	if extractErr := tar.Run(); extractErr != nil {
		_ = archive.Wait()
		panic("extract old helper source")
	}
	must(archive.Wait(), "finish old source archive")
	vendor := filepath.Join(repositoryRoot(), "build", "libghostty-vt", "vendor")
	must(copyTree(vendor, filepath.Join(root, "build", "libghostty-vt", "vendor")), "stage verified pinned VT vendor")
	notices, err := os.ReadFile(filepath.Join(repositoryRoot(), "internal", "helper", "notices", "licenses", "THIRD_PARTY_LICENSES.txt"))
	must(err, "read staged VT notices")
	noticesPath := filepath.Join(root, "internal", "helper", "notices", "licenses", "THIRD_PARTY_LICENSES.txt")
	must(os.MkdirAll(filepath.Dir(noticesPath), 0o700), "create old helper notice directory")
	must(os.WriteFile(noticesPath, notices, 0o600), "stage old helper notices")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-linkmode=external", "-o", destination, "./cmd/nocx-helper") // #nosec G204 -- fixed old helper package and private output.
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build old helper: %s", output))
	}
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		input, err := os.Open(path) // #nosec G304 -- WalkDir visits only the fixed repository build-input tree.
		if err != nil {
			return err
		}
		defer func() { _ = input.Close() }()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()) // #nosec G304 -- destination is the isolated old-helper build tree.
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func repositoryRoot() string {
	root, err := os.Getwd()
	must(err, "get repository working directory")
	return root
}

func startHelper(ctx context.Context, binary, home, expectedHash string) (*client.Client, string, func()) {
	must(os.MkdirAll(home, 0o700), "create isolated helper home")
	tmp := filepath.Join(home, "tmp")
	config := filepath.Join(home, ".config")
	data := filepath.Join(home, ".local", "share")
	cache := filepath.Join(home, ".cache")
	for _, dir := range []string{tmp, config, data, cache} {
		must(os.MkdirAll(dir, 0o700), "create helper XDG directory")
	}
	command := exec.Command(binary, "serve")
	command.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + config,
		"XDG_DATA_HOME=" + data, "XDG_CACHE_HOME=" + cache, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp, "TERM=xterm-256color",
	}
	for _, name := range []string{"LANG", "LC_ALL", "SHELL"} {
		if value := os.Getenv(name); value != "" {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	must(command.Start(), "start real helper binary")
	ended := make(chan error, 1)
	go func() { ended <- command.Wait() }()
	generation := expectedHash
	if generation == "" {
		data, err := os.ReadFile(binary) // #nosec G304 -- binary is built or hash-verified in this private fixture.
		must(err, "read helper binary identity")
		digest := sha256.Sum256(data)
		generation = hex.EncodeToString(digest[:])
	}
	dir := endpoint.Dir(home)
	var conn net.Conn
	for {
		conn, _ = endpoint.Dial(ctx, dir, proto.GenerationID(generation))
		if conn != nil {
			break
		}
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
			<-ended
			panic("helper endpoint readiness timeout")
		case <-ended:
			panic("helper exited before endpoint readiness")
		case <-time.After(25 * time.Millisecond):
		}
	}
	lane, err := client.Dial(ctx, client.Config{Exec: client.NewSocketConn(conn), ExpectHash: generation, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(err, "authenticate real helper")
	socket, err := endpoint.Path(dir, proto.GenerationID(generation))
	must(err, "resolve real helper IPC path")
	stop := func() {
		_ = lane.Close()
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-ended
		}
	}
	return lane, socket, stop
}

func waitForMarker(ctx context.Context, attachment *client.AttachedSession, marker string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		var output strings.Builder
		for {
			n, err := attachment.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				// OSC prompt metadata may precede an emitted marker. Require its
				// complete line ending: echoed shell source ends with "; fi".
				if strings.Contains(output.String(), marker+"\r\n") || strings.Contains(output.String(), marker+"\n") {
					result <- nil
					return
				}
				if strings.Contains(output.String(), "LINUX_NATIVE_FAILURE") || output.Len() > 8192 {
					result <- fmt.Errorf("probe failed or output bound exceeded: %s", output.String())
					return
				}
			}
			if err != nil {
				result <- err
				return
			}
		}
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		_ = attachment.Close()
		return ctx.Err()
	}
}

func waitForLinuxDenial(ctx context.Context, lane *client.Client, session client.HostSessionID, launchID, expectedPath string) sandbox.DiagnosticRecord {
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		result, err := lane.SandboxAccessList(deadline, proto.SandboxAccessListParams{Session: proto.HostSessionID{
			Generation: proto.GenerationID(session.Generation), Session: session.Session,
		}, Limit: 200})
		must(err, "read live Linux diagnostic inbox")
		if result.LaunchID != launchID || result.Inbox.Observer != sandbox.ObserverActive {
			panic("Linux diagnostic observer/inbox identity mismatch")
		}
		for _, record := range result.Inbox.Records {
			if record.Source == sandbox.DiagnosticLinuxSeccomp && record.Precision == sandbox.PrecisionAttempted &&
				record.Prediction == sandbox.PredictionDenied && record.PathKnown && record.Path == expectedPath &&
				record.Access == sandbox.DiagnosticRead {
				return record
			}
		}
		select {
		case <-deadline.Done():
			panic("actual correlated Linux attempted-denial event was not observed")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type proofBackend struct{ address, token string }

func (b proofBackend) WSAddress() string { return b.address }
func (b proofBackend) WSToken() string   { return b.token }

func mustSessions(lane *client.Client, ctx context.Context) []client.SessionEntry {
	entries, err := lane.Sessions(ctx)
	must(err, "read helper session inventory")
	return entries
}

func containsSession(entries []client.SessionEntry, id client.HostSessionID) bool {
	for _, entry := range entries {
		if entry.HostSessionID == id && entry.Exit == nil {
			return true
		}
	}
	return false
}

func closeSession(lane *client.Client, id client.HostSessionID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = lane.CloseSession(ctx, id)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func waitAccept(done <-chan struct{}, timeout time.Duration) error {
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("accept timeout")
	}
}
