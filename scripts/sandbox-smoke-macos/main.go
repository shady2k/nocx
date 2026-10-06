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
)

const pinnedMain = "7e60042546581fde39035dce29110cd3e6a684ac"

func must(err error, operation string) {
	if err != nil {
		var refusal *client.RefusalError
		if errors.As(err, &refusal) {
			var detail sandbox.BuildError
			_ = json.Unmarshal(refusal.Details, &detail)
			fmt.Printf("MACOS_NATIVE_FAILURE operation=%q code=%q prepare_code=%q prepare_field=%q prepare_index=%d\n", operation, refusal.Code, preparationCode(err), detail.Field, detail.Index)
		}
		panic(operation)
	}
}

func preparationCode(err error) string {
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != "sandbox_prepare_failed" {
		return ""
	}
	var detail sandbox.BuildError
	if json.Unmarshal(refusal.Details, &detail) != nil {
		return ""
	}
	return detail.Code
}

func report(name string) { fmt.Println("MACOS_NATIVE_PASS " + name) }

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func main() {
	if runtime.GOOS != "darwin" || os.Getenv("NOCX_SANDBOX_SMOKE_MANDATORY") != "1" {
		panic("mandatory native sandbox proof must be invoked by make sandbox-smoke-macos on macOS")
	}
	if len(os.Args) != 2 || os.Args[1] != "source" && os.Args[1] != "packaged" {
		panic("native proof requires an explicit source or packaged lane")
	}
	lane := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	sourceSHA := gitRevision()
	fmt.Printf("MACOS_NATIVE_PROOF_SOURCE_SHA %s\n", sourceSHA)
	fmt.Printf("MACOS_NATIVE_PROOF_LANE %s\n", lane)
	fmt.Printf("MACOS_NATIVE_PROOF_OLD_HELPER_SHA %s\n", pinnedMain)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nxsm-")
	must(err, "create private proof root")
	root, err = filepath.EvalSymlinks(root)
	must(err, "canonicalize private proof root")
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			panic("private native proof cleanup failed")
		}
	}()

	hostHome := filepath.Join(root, "h")
	work := filepath.Join(hostHome, "workspace 'quoted' \\\\ café")
	readOnly := filepath.Join(hostHome, "read-only 'quoted' \\\\ café")
	outside := filepath.Join(root, "outside")
	if err = os.MkdirAll(hostHome, 0o700); err != nil {
		panic("create isolated proof home")
	}
	caseProbe := filepath.Join(hostHome, "case-sensitivity-probe")
	must(os.WriteFile(caseProbe, []byte("case probe"), 0o600), "create filesystem case probe")
	aliasInfo, aliasErr := os.Stat(filepath.Join(hostHome, strings.ToUpper(filepath.Base(caseProbe))))
	probeInfo, probeErr := os.Stat(caseProbe)
	if aliasErr != nil || probeErr != nil || !os.SameFile(aliasInfo, probeInfo) {
		fmt.Println("MACOS_NATIVE_ALIAS_FIXTURE_UNSUPPORTED case-insensitive-volume-required")
		panic("mandatory reserved-root alias proof requires a case-insensitive macOS volume")
	}
	must(os.Remove(caseProbe), "remove case-sensitivity probe")
	must(os.MkdirAll(filepath.Join(hostHome, ".nocx"), 0o700), "create reserved app subtree fixture")
	for _, dir := range []string{work, readOnly, outside} {
		must(os.MkdirAll(dir, 0o700), "create proof fixture")
	}
	must(os.WriteFile(filepath.Join(readOnly, "readable"), []byte("read-only-fixture"), 0o600), "write read-only fixture")
	must(os.WriteFile(filepath.Join(outside, "denied"), []byte("outside-fixture"), 0o600), "write outside fixture")
	must(os.WriteFile(filepath.Join(hostHome, ".host-secret"), []byte("never project"), 0o600), "write projection sentinel")

	probePath := filepath.Join(work, ".native-probe")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", probePath, "./scripts/sandbox-smoke-macos/probe") // #nosec G204 -- fixed Go build target and private fixture output, no shell.
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if _, err = build.CombinedOutput(); err != nil {
		panic("build native probe")
	}

	currentBinary := filepath.Join(root, lane+"-local-helper")
	var currentHash string
	if lane == "source" {
		buildSourceHelper(ctx, currentBinary)
	} else {
		currentHash = extractPackagedHelper(currentBinary)
	}
	oldBinary := filepath.Join(root, "pinned-main-old-helper")
	buildPinnedOldHelper(ctx, oldBinary)
	currentClient, currentSocket, stopCurrent := startHelper(ctx, currentBinary, hostHome, currentHash)
	defer stopCurrent()
	oldHome := filepath.Join(root, "old-home")
	must(os.MkdirAll(oldHome, 0o700), "create old helper home")
	oldClient, oldSocket, stopOld := startHelper(ctx, oldBinary, oldHome, "")
	defer stopOld()
	oldSource, err := oldClient.Spawn(ctx, proto.SpawnParams{Workspace: "ws-old-helper", Cwd: work, Cols: 80, Rows: 24})
	must(err, "spawn pinned-main ordinary helper session")
	defer closeSession(oldClient, oldSource.HostSessionID)

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err, "listen TCP control")
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

	tokenBytes := make([]byte, 32)
	_, err = rand.Read(tokenBytes)
	must(err, "generate private coordinator test token")
	coordinatorBackend := proofBackend{address: tcpListener.Addr().String(), token: hex.EncodeToString(tokenBytes)}
	coordinatorDir := filepath.Join(work, "coordinator-runtime")
	server, err := coordinator.NewServer(coordinator.Config{
		Dir:     coordinatorDir,
		Build:   coordinator.Build{Version: version.Version, Commit: version.Commit},
		Backend: coordinatorBackend,
		Peers:   coordinator.SystemPeerCredentials{},
		Owner:   coordinator.SystemPathOwner{},
		SelfUID: coordinator.SelfUID(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	must(err, "configure real coordinator discovery endpoint")
	must(server.Start(), "start real coordinator discovery endpoint")
	defer func() { _ = server.Close() }()
	coordinatorSocket := server.SocketPath()
	discovery, err := coordinator.NewClient(coordinator.ClientConfig{
		Socket: coordinatorSocket,
		Self:   coordinator.ClientIdentity{Version: version.Version, Commit: version.Commit, Protocol: coordinator.ProtocolVersion},
		Dialer: coordinator.SystemDialer{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	must(err, "configure coordinator discovery client")
	if _, err = discovery.Hello(ctx); err != nil {
		panic("host coordinator discovery did not answer before sandbox launch")
	}

	source, err := currentClient.Spawn(ctx, proto.SpawnParams{Workspace: "ws-native-proof", Cwd: work, Cols: 80, Rows: 24})
	must(err, "spawn ordinary source session")
	defer closeSession(currentClient, source.HostSessionID)

	key := make([]byte, 32)
	_, err = rand.Read(key)
	must(err, "generate ephemeral ContentDB key")
	db, err := content.Open(ctx, content.Config{
		Path: filepath.Join(root, "content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 16 << 20, DiskCeilingBytes: 64 << 20, CompactionFloor: .8},
		Logger: nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil))),
	})
	must(err, "open isolated ContentDB")
	defer func() { _ = db.Close() }()
	_, err = db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-native-proof", Name: "native proof"},
		content.Tab{ID: "tab-native-proof", WorkspaceID: "ws-native-proof", Layout: content.LayoutRow},
		content.Pane{ID: "pane-native-proof", TabID: "tab-native-proof", Cwd: work, Kind: content.PaneLocal, SizeShare: 1})
	must(err, "persist proof workspace")
	identity := content.HelperIdentity{Host: "local", Generation: source.HostSessionID.Generation, SessionID: source.HostSessionID.Session}
	must(db.Ledger().CreateSession(ctx, content.Session{
		ID: identity.SessionID, WorkspaceID: "ws-native-proof", Host: identity.Host,
		Account: identity.Account, Generation: identity.Generation, PaneID: "pane-native-proof",
	}), "persist ordinary source session")
	reservedAlias := filepath.Join(hostHome, ".NOCX")
	aliasPreparation, aliasErr := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "mac-native-reserved-alias", LaunchID: "mac-native-reserved-alias-launch",
		Mode: proto.SandboxEnforce, Workspace: "ws-native-proof", Cwd: work,
		Enforce: &proto.SandboxEnforceIntent{
			StandardRevision: 1, WorkspaceRevision: 0,
			Profile:           sandbox.ProfileRoots{ReadOnlyDirs: []string{reservedAlias}},
			ProfileProvenance: sandbox.StandardRoot,
		},
	})
	if aliasErr == nil {
		_ = currentClient.SandboxDiscard(ctx, aliasPreparation.Ticket)
		panic("case-insensitive reserved-root alias was accepted")
	}
	if preparationCode(aliasErr) != "reserved_root_conflict" {
		must(aliasErr, "reserved-root alias returned the wrong refusal")
	}
	report("APFS_CASE_INSENSITIVE_RESERVED_ALIAS_REFUSED")

	invalid, err := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "mac-native-invalid-operation", LaunchID: "mac-native-invalid-launch",
		Mode: proto.SandboxEnforce, Workspace: "ws-native-proof", Cwd: work,
		Enforce: &proto.SandboxEnforceIntent{
			StandardRevision: 1, ProfileProvenance: sandbox.StandardRoot,
			Profile: sandbox.ProfileRoots{ReadOnlyDirs: []string{filepath.Join(outside, "missing-root")}},
		},
	})
	if err == nil {
		_ = currentClient.SandboxDiscard(ctx, invalid.Ticket)
		panic("helper accepted malformed native root instead of refusing Enforce")
	}
	if preparationCode(err) != "invalid_root" {
		must(err, "missing native root returned the wrong refusal")
	}
	report("MALFORMED_NATIVE_PREPARE_REFUSED")
	failed, err := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "mac-native-exec-failure", LaunchID: "mac-native-exec-failure-launch",
		Mode: proto.SandboxEnforce, Workspace: "ws-native-failure", Cwd: work,
		Enforce: &proto.SandboxEnforceIntent{
			StandardRevision: 1, WorkspaceRevision: 0,
			Profile:           sandbox.ProfileRoots{ReadOnlyDirs: []string{readOnly}},
			ProfileProvenance: sandbox.StandardRoot,
		},
	})
	must(err, "prepare native exec-unwind scenario")
	if failed.Policy == nil || failed.Digest == "" {
		panic("helper returned no policy for native exec failure scenario")
	}
	failureDB, err := content.Open(ctx, content.Config{
		Path: filepath.Join(root, "failure-content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 16 << 20, DiskCeilingBytes: 64 << 20, CompactionFloor: .8},
		Logger: nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil))),
	})
	must(err, "open failure proof ContentDB")
	defer func() { _ = failureDB.Close() }()
	_, err = failureDB.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: "ws-native-failure", Name: "failure proof"},
		content.Tab{ID: "tab-native-failure", WorkspaceID: "ws-native-failure", Layout: content.LayoutRow},
		content.Pane{ID: "pane-native-failure", TabID: "tab-native-failure", Cwd: work, Kind: content.PaneLocal, SizeShare: 1})
	must(err, "persist failure proof workspace")
	must(failureDB.Ledger().CreateSession(ctx, content.Session{
		ID: identity.SessionID, WorkspaceID: "ws-native-failure", Host: identity.Host,
		Account: identity.Account, Generation: identity.Generation, PaneID: "pane-native-failure",
	}), "persist failure source session")
	failedGrant, err := failureDB.Launches().Prepare(ctx, content.LaunchPrepare{
		ID: failed.LaunchID, PaneID: "pane-native-failure", WorkspaceID: "ws-native-failure", Source: identity,
		TargetGeneration: identity.Generation,
		StandardRevision: 1, WorkspaceRevision: 0, Mode: content.LaunchEnforce,
		Policy: failed.Policy, PolicyDigest: failed.Digest, PolicyVersion: sandbox.PolicyVersion,
	})
	must(err, "persist failure scenario grant")
	if failedGrant.GrantID == nil {
		panic("ContentDB did not mint failure scenario grant")
	}
	runnerPath := failed.Policy.Runner
	runnerInfo, err := os.Stat(runnerPath)
	must(err, "stat isolated embedded runner")
	runnerBytes, err := os.ReadFile(runnerPath) // #nosec G304 -- verified runner installed by this isolated helper fixture.
	must(err, "read isolated embedded runner copy")
	must(os.WriteFile(runnerPath, []byte("not a Mach-O executable"), 0o700), "corrupt isolated runner for exec failure") // #nosec G306 -- executable fixture requires owner execute permission.
	mutatedInfo, err := os.Stat(runnerPath)
	must(err, "verify isolated runner path")
	if !os.SameFile(runnerInfo, mutatedInfo) {
		panic("native exec-failure fixture replaced the pinned runner object")
	}
	_, failureErr := currentClient.SandboxLaunch(ctx, proto.SandboxLaunchParams{
		Ticket: failed.Ticket, OperationID: failed.OperationID, LaunchID: failed.LaunchID,
		Mode:  proto.SandboxEnforce,
		Grant: &proto.SandboxGrantBinding{ID: *failedGrant.GrantID, Digest: failed.Digest, Version: sandbox.PolicyVersion},
		Shape: proto.SandboxLaunchShape{Cols: 80, Rows: 24},
	})
	must(os.WriteFile(runnerPath, runnerBytes, runnerInfo.Mode().Perm()), "restore isolated embedded runner")
	if failureErr == nil {
		panic("malformed native executable unexpectedly launched")
	}
	var spawnRefusal *client.RefusalError
	if !errors.As(failureErr, &spawnRefusal) || spawnRefusal.Code != "sandbox_launch_failed" {
		must(failureErr, "malformed runner did not reach the helper native launch failure path")
	}
	if _, err = os.Stat(failed.Policy.Runtime.Root); !errors.Is(err, os.ErrNotExist) {
		panic("failed native candidate runtime was not unwound")
	}
	sourceEntries, err := currentClient.Sessions(ctx)
	must(err, "query source after native exec failure")
	if len(sourceEntries) != 1 || !containsSession(sourceEntries, source.HostSessionID) {
		panic("native exec failure spawned a fallback or disturbed its source")
	}
	report("NATIVE_RUNNER_EXEC_FAILURE_UNWIND_AND_NO_FALLBACK_PASS")

	prepared, err := currentClient.SandboxPrepare(ctx, proto.SandboxPrepareParams{
		OperationID: "mac-native-proof-operation", LaunchID: "mac-native-proof-launch",
		Mode: proto.SandboxEnforce, Workspace: "ws-native-proof", Cwd: work,
		Enforce: &proto.SandboxEnforceIntent{
			StandardRevision: 1, WorkspaceRevision: 0,
			Profile:           sandbox.ProfileRoots{ReadOnlyDirs: []string{readOnly}},
			ProfileProvenance: sandbox.StandardRoot,
		},
	})
	must(err, "prepare enforced policy through helper")
	if prepared.Policy == nil || prepared.Digest == "" {
		panic("helper returned no native policy")
	}
	if prepared.Policy.Backend != sandbox.MacOSSeatbelt || prepared.Policy.BackendVersion != 1 {
		panic("helper did not prepare the required Seatbelt backend")
	}
	if !filepath.IsAbs(prepared.Policy.Runner) {
		panic("native helper policy has no absolute embedded runner")
	}
	runnerInfo, err = os.Stat(prepared.Policy.Runner)
	if err != nil || runnerInfo.Mode().Perm()&0o100 == 0 {
		panic("packaged helper did not install its embedded native runner")
	}
	rootObserved := false
	readOnlyCanonical, err := filepath.EvalSymlinks(readOnly)
	must(err, "canonicalize proof read-only root")
	desired, err := os.Stat(readOnlyCanonical)
	must(err, "stat canonical read-only fixture")
	for _, root := range prepared.Policy.Roots {
		info, statErr := os.Stat(root.Path)
		if statErr == nil && os.SameFile(info, desired) && root.Access == sandbox.ReadOnly {
			rootObserved = true
		}
	}
	if !rootObserved {
		panic("Seatbelt policy did not retain the quoted Unicode read-only root")
	}
	if _, err = os.Stat("/usr/bin/sandbox-exec"); err != nil {
		panic("mandatory Seatbelt executable unavailable")
	}
	launch, err := db.Launches().Prepare(ctx, content.LaunchPrepare{
		ID: prepared.LaunchID, PaneID: "pane-native-proof", WorkspaceID: "ws-native-proof", Source: identity,
		TargetGeneration: identity.Generation,
		StandardRevision: 1, WorkspaceRevision: 0, Mode: content.LaunchEnforce,
		Policy: prepared.Policy, PolicyDigest: prepared.Digest, PolicyVersion: sandbox.PolicyVersion,
	})
	must(err, "persist immutable ContentDB launch grant")
	if launch.GrantID == nil {
		panic("ContentDB did not mint a durable Enforce grant")
	}
	candidate, err := currentClient.SandboxLaunch(ctx, proto.SandboxLaunchParams{
		Ticket: prepared.Ticket, OperationID: prepared.OperationID, LaunchID: prepared.LaunchID,
		Mode:  proto.SandboxEnforce,
		Grant: &proto.SandboxGrantBinding{ID: *launch.GrantID, Digest: prepared.Digest, Version: sandbox.PolicyVersion},
		Shape: proto.SandboxLaunchShape{Cols: 120, Rows: 40},
	})
	must(err, "consume prepared grant in real native launch")
	defer closeSession(currentClient, candidate.HostSessionID)
	binding, err := currentClient.SandboxGet(ctx, candidate.HostSessionID)
	must(err, "read helper launch binding")
	if binding.Mode != string(proto.SandboxEnforce) || binding.Enforcement != "enforced" || binding.Grant == nil || binding.Grant.ID != *launch.GrantID || binding.Grant.Digest != prepared.Digest {
		panic("helper binding did not match the persisted immutable grant")
	}
	report(strings.ToUpper(lane) + "_HELPER_EMBEDDED_RUNNER_AND_PERSISTED_GRANT_BOUND")

	attachment, err := currentClient.Attach(ctx, proto.AttachParams{
		Subscriber: "aabbccddeeff00112233445566778899",
		Session:    proto.HostSessionID{Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session},
		Offset:     proto.StreamOffset(candidate.Window.Base), Fresh: true, RequestWrite: true,
	})
	must(err, "attach to restricted helper PTY")
	defer func() { _ = attachment.Close() }()
	deniedProbePath := filepath.Join(outside, "denied")
	if !filepath.IsAbs(deniedProbePath) || strings.ContainsAny(deniedProbePath, " \t\r\n'\"()") {
		panic("dedicated native diagnostics path is not an unambiguous absolute path")
	}
	command := strings.Join([]string{
		shellQuote(probePath), shellQuote(work), shellQuote(readOnly), shellQuote(outside), shellQuote(workspaceSocket),
		shellQuote(currentSocket), shellQuote(oldSocket), shellQuote(coordinatorSocket), shellQuote(tcpListener.Addr().String()),
		shellQuote(prepared.Policy.Runtime.Root), shellQuote(prepared.Policy.Runtime.Home), shellQuote(prepared.Policy.WorkspaceRoot),
	}, " ") + "\n" + shellQuote("/bin/cat") + " " + shellQuote(deniedProbePath) + " >/dev/null 2>&1\n"
	_, err = attachment.Write([]byte(command))
	must(err, "send native probe to protected PTY")
	if err = waitForProof(ctx, attachment); err != nil {
		panic("native Seatbelt probe failed")
	}
	diagnostic := waitForNativeDenial(ctx, currentClient, candidate.HostSessionID, prepared.LaunchID, deniedProbePath)
	reserved, err := currentClient.SandboxAccessReserve(ctx, proto.SandboxAccessReserveParams{
		Session: proto.HostSessionID{Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session},
		EventID: diagnostic.ID, Revision: diagnostic.Revision, Decision: sandbox.DecisionAllowRO,
	})
	must(err, "reserve native denial receipt without profile mutation")
	if reserved.LaunchID != prepared.LaunchID || reserved.Record.ID != diagnostic.ID || reserved.Record.State != sandbox.DiagnosticPending {
		panic("helper reserved a different native denial receipt")
	}
	finished, err := currentClient.SandboxAccessFinish(ctx, proto.SandboxAccessFinishParams{
		Session: proto.HostSessionID{Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session},
		EventID: diagnostic.ID, Reservation: reserved.Reservation, Committed: false,
	})
	must(err, "abort uncommitted native denial reservation")
	if finished.LaunchID != prepared.LaunchID || finished.Record.ID != diagnostic.ID || finished.Record.State != sandbox.DiagnosticUnresolved {
		panic("helper did not retain the unresolved native denial receipt")
	}
	retained, err := currentClient.SandboxAccessList(ctx, proto.SandboxAccessListParams{
		Session: proto.HostSessionID{Generation: proto.GenerationID(candidate.HostSessionID.Generation), Session: candidate.HostSessionID.Session},
		Limit:   200,
	})
	must(err, "read retained native denial receipt")
	if retained.LaunchID != prepared.LaunchID || retained.Inbox.Observer != sandbox.ObserverActive {
		panic("native diagnostic inbox lost launch correlation or observer status")
	}
	retainedReceipt := false
	for _, record := range retained.Inbox.Records {
		if record.ID == diagnostic.ID && record.Source == sandbox.DiagnosticMacSeatbelt &&
			record.Precision == sandbox.PrecisionReportedDenial && record.PathKnown &&
			record.Path == deniedProbePath && record.State == sandbox.DiagnosticUnresolved {
			retainedReceipt = true
			break
		}
	}
	if !retainedReceipt {
		panic("helper did not retain the uncommitted native denial receipt")
	}
	postDiagnostics, err := currentClient.SandboxGet(ctx, candidate.HostSessionID)
	must(err, "read native binding after diagnostic processing")
	if postDiagnostics.Mode != binding.Mode || postDiagnostics.Enforcement != binding.Enforcement ||
		postDiagnostics.Grant == nil || postDiagnostics.Grant.ID != binding.Grant.ID || postDiagnostics.Grant.Digest != binding.Grant.Digest {
		panic("diagnostic processing changed the live native grant or enforcement")
	}
	if _, err = attachment.Write([]byte("if " + shellQuote("/bin/cat") + " " + shellQuote(deniedProbePath) +
		" >/dev/null 2>&1; then printf 'MACOS_NATIVE_DIAGNOSTIC_POLICY_%s\\n' CHANGED; else printf 'MACOS_NATIVE_DIAGNOSTIC_POLICY_%s\\n' UNCHANGED; fi\n")); err != nil {
		panic("send post-diagnostic native denial check")
	}
	if err = waitForMarker(ctx, attachment, "MACOS_NATIVE_DIAGNOSTIC_POLICY_UNCHANGED"); err != nil {
		panic("native shell denial did not remain after diagnostic processing")
	}
	report("REAL_SEATBELT_DENIAL_CORRELATED_AND_RECEIPT_RETAINED")
	report("DIAGNOSTIC_PROCESSING_PRESERVED_LIVE_GRANT_AND_NATIVE_DENIAL")
	entries, err := currentClient.Sessions(ctx)
	must(err, "query current helper source inventory")
	if !containsSession(entries, source.HostSessionID) {
		panic("ordinary source session did not survive native launch")
	}
	oldEntries, err := oldClient.Sessions(ctx)
	must(err, "query pinned-main helper inventory")
	if !containsSession(oldEntries, oldSource.HostSessionID) {
		panic("pinned-main helper session disappeared")
	}
	// #nosec G304 -- sentinel created inside this private proof fixture.
	if _, err = os.ReadFile(filepath.Join(outside, "denied")); err != nil {
		panic("helper/coordinator host filesystem access was restricted")
	}
	if _, err = discovery.Hello(ctx); err != nil {
		panic("coordinator discovery stopped working for the host")
	}
	if err = waitAccept(tcpDone, time.Second); err != nil {
		panic("host TCP fixture did not accept the sandbox probe")
	}
	report("ORDINARY_SOURCE_AND_HOST_CONTROL_REMAIN_UNRESTRICTED")
	fmt.Printf("MACOS_NATIVE_%s_PROOF_COMPLETE\n", strings.ToUpper(lane))
}

func extractPackagedHelper(destination string) string {
	local, ok := artifacts.DefaultSource.(deploy.LocalArtifactSource)
	if !ok {
		panic("packaged local helper source is unavailable")
	}
	compressed, expected, err := local.LocalArtifact(deploy.Platform{GOOS: "darwin", GOARCH: runtime.GOARCH})
	must(err, "read embedded packaged local helper")
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	must(err, "open embedded helper artifact")
	binary, err := io.ReadAll(io.LimitReader(reader, 512<<20))
	must(err, "read embedded helper artifact")
	must(reader.Close(), "close packaged helper archive")
	digest := sha256.Sum256(binary)
	if hex.EncodeToString(digest[:]) != expected {
		panic("packaged local helper content hash mismatch")
	}
	must(os.WriteFile(destination, binary, 0o700), "materialize packaged helper in private fixture") // #nosec G306 -- hash-verified executable fixture requires owner execute permission.
	return expected
}

func buildSourceHelper(ctx context.Context, destination string) {
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-tags", "nocx_local_ssh", "-o", destination, "./cmd/nocx-helper")
	command.Dir = repositoryRoot()
	command.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS=darwin", "GOARCH="+runtime.GOARCH)
	if _, err := command.CombinedOutput(); err != nil {
		panic("compile current source helper")
	}
}

func buildPinnedOldHelper(ctx context.Context, destination string) {
	root, err := os.MkdirTemp("", "nocx-pinned-old-source-")
	must(err, "create pinned source fixture")
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			panic("pinned helper source cleanup failed")
		}
	}()
	archive := exec.CommandContext(ctx, "git", "-C", repositoryRoot(), "archive", "--format=tar", pinnedMain) // #nosec G204 -- fixed pinned commit and repository root, no shell.
	pipe, err := archive.StdoutPipe()
	must(err, "open pinned-source archive")
	if err = archive.Start(); err != nil {
		panic("start pinned-source archive")
	}
	tar := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", root) // #nosec G204 -- trusted Git archive extracted into this process's private temporary directory.
	tar.Stdin = pipe
	if err = tar.Run(); err != nil {
		_ = archive.Wait()
		panic("extract pinned-main source")
	}
	must(archive.Wait(), "read pinned-main source")
	currentVendor := filepath.Join(repositoryRoot(), "build", "libghostty-vt", "vendor")
	oldVendor := filepath.Join(root, "build", "libghostty-vt", "vendor")
	must(copyTree(currentVendor, oldVendor), "stage verified pinned VT archives for old helper")
	currentNotices := filepath.Join(repositoryRoot(), "internal", "helper", "notices", "licenses", "THIRD_PARTY_LICENSES.txt")
	notices, err := os.ReadFile(currentNotices) // #nosec G304 -- fixed repository VT notices path.
	must(err, "read verified pinned VT notices")
	oldNotices := filepath.Join(root, "internal", "helper", "notices", "licenses", "THIRD_PARTY_LICENSES.txt")
	must(os.MkdirAll(filepath.Dir(oldNotices), 0o700), "create old helper notices directory")
	must(os.WriteFile(oldNotices, notices, 0o600), "stage verified pinned VT notices")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", destination, "./cmd/nocx-helper")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS=darwin", "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		_ = output
		panic("compile pinned-main helper generation")
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
		input, err := os.Open(path) // #nosec G304 -- entry supplied by WalkDir over the verified VT vendor tree.
		if err != nil {
			return err
		}
		defer func() { _ = input.Close() }()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()) // #nosec G304 -- corresponding entry inside the private pinned-source tree; never overwrites.
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

func gitRevision() string {
	command := exec.Command("git", "-C", repositoryRoot(), "rev-parse", "--verify", "HEAD") // #nosec G204 -- fixed Git query of the repository working directory.
	output, err := command.Output()
	must(err, "read source revision")
	revision := strings.TrimSpace(string(output))
	if len(revision) != 40 {
		panic("source revision has unexpected format")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		panic("source revision has unexpected format")
	}
	return revision
}

func startHelper(ctx context.Context, binary, home, expectedHash string) (*client.Client, string, func()) {
	must(os.MkdirAll(home, 0o700), "create helper home")
	tmpDir := filepath.Join(home, "tmp")
	must(os.MkdirAll(tmpDir, 0o700), "create isolated helper temporary directory")
	configDir := filepath.Join(home, "Library", "Application Support")
	cacheDir := filepath.Join(home, "Library", "Caches")
	command := exec.Command(binary, "serve")
	command.Env = []string{
		"PATH=" + safePATH(),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_DATA_HOME=" + configDir,
		"XDG_CACHE_HOME=" + cacheDir,
		"TMPDIR=" + tmpDir,
		"TMP=" + tmpDir,
		"TEMP=" + tmpDir,
		"TERM=xterm-256color",
	}
	for _, name := range []string{"LANG", "LC_ALL", "SHELL"} {
		if value := os.Getenv(name); value != "" {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	must(command.Start(), "start helper")
	ended := make(chan error, 1)
	go func() { ended <- command.Wait() }()
	generation := expectedHash
	if generation == "" {
		data, err := os.ReadFile(binary) // #nosec G304 -- own compiled or hash-verified executable fixture.
		must(err, "read old helper identity")
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
	must(err, "authenticate helper connection")
	socketPath, err := endpoint.Path(dir, proto.GenerationID(generation))
	must(err, "resolve helper endpoint")
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
	return lane, socketPath, stop
}

func safePATH() string {
	if path := os.Getenv("PATH"); path != "" {
		return path
	}
	return "/usr/bin:/bin:/usr/sbin:/sbin"
}

func waitForProof(ctx context.Context, attachment *client.AttachedSession) error {
	read := make(chan error, 1)
	go func() {
		var output bytes.Buffer
		buffer := make([]byte, 8192)
		for {
			n, err := attachment.Read(buffer)
			if n > 0 {
				output.Write(buffer[:n])
				text := output.String()
				if strings.Contains(text, "NATIVE_FAIL") {
					for _, line := range strings.Split(text, "\n") {
						if strings.HasPrefix(line, "NATIVE_FAIL ") {
							fmt.Println(line)
						}
					}
					read <- errors.New("native probe reported failure")
					return
				}
				if strings.Contains(text, "NATIVE_KERNEL_SMOKE_COMPLETE") {
					for _, line := range strings.Split(text, "\n") {
						if strings.HasPrefix(line, "NATIVE_PASS ") {
							fmt.Println(line)
						}
					}
					read <- nil
					return
				}
				if output.Len() > 128<<10 {
					read <- errors.New("probe output bound exceeded")
					return
				}
			}
			if err != nil {
				read <- err
				return
			}
		}
	}()
	select {
	case err := <-read:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitForNativeDenial(ctx context.Context, lane *client.Client, session client.HostSessionID, launchID, expectedPath string) sandbox.DiagnosticRecord {
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		result, err := lane.SandboxAccessList(deadline, proto.SandboxAccessListParams{
			Session: proto.HostSessionID{Generation: proto.GenerationID(session.Generation), Session: session.Session},
			Limit:   200,
		})
		must(err, "read real native diagnostic inbox")
		if result.Session.Generation != proto.GenerationID(session.Generation) || result.Session.Session != session.Session ||
			result.LaunchID != launchID || result.Inbox.Observer != sandbox.ObserverActive {
			panic("native diagnostic inbox identity or required observer status mismatch")
		}
		for _, record := range result.Inbox.Records {
			if record.Source == sandbox.DiagnosticMacSeatbelt && record.Precision == sandbox.PrecisionReportedDenial &&
				record.PathKnown && record.Path == expectedPath && record.Access == sandbox.DiagnosticRead &&
				strings.HasPrefix(record.Operation, "file-read") {
				return record
			}
		}
		select {
		case <-deadline.Done():
			panic("required correlated Seatbelt denial was not collected before deadline")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForMarker(ctx context.Context, attachment *client.AttachedSession, marker string) error {
	read := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1024)
		var output strings.Builder
		for {
			n, err := attachment.Read(buffer)
			if n > 0 {
				output.Write(buffer[:n])
				if strings.Contains(output.String(), marker) {
					read <- nil
					return
				}
				if strings.Contains(output.String(), "MACOS_NATIVE_DIAGNOSTIC_POLICY_CHANGED") {
					read <- errors.New("sandbox policy changed")
					return
				}
				if output.Len() > 4096 {
					read <- errors.New("post-diagnostic marker output bound exceeded")
					return
				}
			}
			if err != nil {
				read <- err
				return
			}
		}
	}()
	select {
	case err := <-read:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func containsSession(entries []client.SessionEntry, id client.HostSessionID) bool {
	for _, entry := range entries {
		if entry.HostSessionID == id && entry.Exit == nil {
			return true
		}
	}
	return false
}

type proofBackend struct {
	address string
	token   string
}

func (b proofBackend) WSAddress() string { return b.address }
func (b proofBackend) WSToken() string   { return b.token }

func closeSession(lane *client.Client, id client.HostSessionID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = lane.CloseSession(ctx, id)
}

func waitAccept(done <-chan struct{}, timeout time.Duration) error {
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("accept timeout")
	}
}
