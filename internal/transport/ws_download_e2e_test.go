package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/shady2k/nocx/internal/downloadsave"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// This crosses the real SSH/SFTP source, transport HTTP framing, the desktop
// receiver's streaming/atomic sink, and the completion RPC. Fake source tests
// remain useful for fault injection, but cannot prove these pieces interoperate.
func TestNativeDownloadEndToEndWithRealSFTPSource(t *testing.T) {
	fixtureHome := t.TempDir()
	fixture := startDownloadSSHD(t, fixtureHome)
	sshClient, sftpClient := connectDownloadSSHD(t, fixture)
	t.Cleanup(func() { _ = sftpClient.Close(); _ = sshClient.Close() })

	body := bytes.Repeat([]byte("native-real-sftp-"), 50_000)
	remotePath := filepath.ToSlash(filepath.Join(fixtureHome, "remote-large.bin"))
	remoteFile, remoteErr := sftpClient.OpenFile(remotePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if remoteErr != nil {
		t.Fatalf("create remote fixture: %v", remoteErr)
	}
	if _, err := remoteFile.Write(body); err != nil {
		_ = remoteFile.Close()
		t.Fatalf("write remote fixture: %v", err)
	}
	if err := remoteFile.Close(); err != nil {
		t.Fatalf("close remote fixture: %v", err)
	}

	remoteSource := transfer.NewSource(sftpReadFS{client: sftpClient}, transfer.DefaultChunk)
	factory := func(sess session.Session, root string) (filesystem.Provider, error) {
		provider, providerErr := filesLocalFactory(sess, root)
		if providerErr != nil {
			return nil, providerErr
		}
		return &downloadSourceProvider{Provider: provider, source: remoteSource}, nil
	}
	e := newDownloadTestEnvWith(t, factory)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, t.TempDir(), 2)

	destination := filepath.Join(t.TempDir(), "saved.bin")
	service, err := downloadsave.New(downloadsave.Config{
		Picker: downloadPicker{path: destination},
		Sink:   (&local.Provider{}).Sink(),
		Address: func() (string, error) {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(e.ws.Port())), nil
		},
		Now:    time.Now,
		Random: rand.Reader,
	})
	if err != nil {
		t.Fatalf("construct desktop receiver: %v", err)
	}
	defer service.Close()
	handle, err := service.Prepare(context.Background(), filepath.Base(remotePath))
	if err != nil || handle == "" {
		t.Fatalf("prepare native target handle=%q err=%v", handle, err)
	}

	params := downloadParams(bid, remotePath)
	params["destination"] = "native"
	started := callDownload(t, e.conn, params, 3).mustResult(t)
	if started.Size != int64(len(body)) {
		t.Fatalf("advertised size %d, want %d", started.Size, len(body))
	}
	result := service.Save(context.Background(), handle, started.Ticket, started.Size)
	if result.Outcome != "saved" {
		t.Fatalf("desktop receiver outcome %q, want saved", result.Outcome)
	}
	got, err := os.ReadFile(destination) //nolint:gosec // destination is inside t.TempDir
	if err != nil {
		t.Fatalf("read promoted destination: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("saved %d bytes, want exact %d-byte SFTP fixture", len(got), len(body))
	}

	rt := e.ws.transferFor(started.TransferID)
	select {
	case <-rt.done:
		t.Fatal("real native transfer finalized before completion RPC")
	default:
	}
	// Keep the request connection's session ownership, but detach notification
	// delivery so the terminal account must be retained and flushed on attach.
	e.ws.getRx(session.ID(sid)).setSubscriber(nil, nil)
	raw := jsonrpcCallWithID(t, e.conn, "files.downloadComplete", map[string]any{
		"transferId": started.TransferID, "outcome": result.Outcome,
	}, 4)
	var completion struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &completion); err != nil || completion.Error != nil || string(completion.Result) != "{}" {
		t.Fatalf("completion response %s (decode %v)", raw, err)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateSent {
		t.Fatalf("final state %q, want sent", state)
	}
	reconnected := reattach(t, e, sid, 5)
	var done filesDownloadDoneParams
	if err := json.Unmarshal(readNotification(t, reconnected, "files.downloadDone", wantWithin), &done); err != nil {
		t.Fatalf("decode retained terminal outcome: %v", err)
	}
	if done.TransferID != started.TransferID || done.Outcome != downloadStateSent || done.Bytes != int64(len(body)) {
		t.Fatalf("downloadDone %+v, want sent %d-byte transfer %s", done, len(body), started.TransferID)
	}
}

type downloadSourceProvider struct {
	filesystem.Provider
	source transfer.Source
}

func (p *downloadSourceProvider) Source() transfer.Source { return p.source }

type sftpReadFS struct{ client *sftp.Client }

func (f sftpReadFS) Open(path string) (transfer.RemoteReader, int64, error) {
	file, err := f.client.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, transfer.ErrNotRegular
	}
	return file, info.Size(), nil
}

type downloadPicker struct{ path string }

func (p downloadPicker) SaveFile(_ context.Context, suggested string) (string, error) {
	if suggested != "remote-large.bin" {
		return "", fmt.Errorf("unexpected suggested name %q", suggested)
	}
	return p.path, nil
}

type downloadSSHD struct {
	addr, userKey, knownHosts string
}

func startDownloadSSHD(t *testing.T, home string) downloadSSHD {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	binary := filepath.Join(home, "e2e-sshd")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/e2e-sshd") //nolint:gosec // test builds the helper from the checked-out module
	build.Dir = root
	output, err := build.CombinedOutput()
	if err != nil {
		cancel()
		t.Fatalf("build e2e-sshd: %v: %s", err, output)
	}
	cmd := exec.CommandContext(ctx, binary) //nolint:gosec // binary was just built into t.TempDir
	cmd.Dir = root
	cmd.Env = downloadFixtureEnv(home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start e2e-sshd: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	type readyResult struct {
		fixture downloadSSHD
		err     error
	}
	ready := make(chan readyResult, 1)
	go func() {
		values := make(map[string]string)
		scanner := bufio.NewScanner(stdout)
		announced := false
		for scanner.Scan() {
			line := scanner.Text()
			if key, value, found := strings.Cut(line, "="); found {
				values[key] = value
			}
			if line == "READY" && !announced {
				ready <- readyResult{fixture: downloadSSHD{
					addr: values["ADDR"], userKey: values["USERKEY"], knownHosts: values["KNOWNHOSTS"],
				}}
				announced = true
			}
		}
		if !announced {
			ready <- readyResult{err: scanner.Err()}
		}
	}()
	select {
	case result := <-ready:
		if result.err != nil || result.fixture.addr == "" || result.fixture.userKey == "" || result.fixture.knownHosts == "" {
			t.Fatalf("e2e-sshd did not become ready: fixture=%+v err=%v", result.fixture, result.err)
		}
		return result.fixture
	case <-ctx.Done():
		t.Fatalf("e2e-sshd startup timed out: %v", ctx.Err())
		return downloadSSHD{}
	}
}

func downloadFixtureEnv(home string) []string {
	baseHome := os.Getenv("HOME")
	if baseHome == "" {
		baseHome, _ = os.UserHomeDir()
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(baseHome, "go")
	}
	gocache := os.Getenv("GOCACHE")
	if gocache == "" {
		gocache = filepath.Join(baseHome, ".cache", "go-build")
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "HOME=") || strings.HasPrefix(item, "GOPATH=") || strings.HasPrefix(item, "GOCACHE=") || strings.HasPrefix(item, "TMPDIR=") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "HOME="+home, "GOPATH="+gopath, "GOCACHE="+gocache, "TMPDIR="+home)
}

func connectDownloadSSHD(t *testing.T, fixture downloadSSHD) (*ssh.Client, *sftp.Client) {
	t.Helper()
	key, keyErr := os.ReadFile(fixture.userKey)
	if keyErr != nil {
		t.Fatalf("read fixture key: %v", keyErr)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatalf("parse fixture key: %v", err)
	}
	knownHostsFile := filepath.Join(t.TempDir(), "known_hosts")
	if writeErr := os.WriteFile(knownHostsFile, []byte(fixture.knownHosts+"\n"), 0o600); writeErr != nil {
		t.Fatalf("write fixture known_hosts: %v", writeErr)
	}
	hostKeyCallback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		t.Fatalf("load fixture known_hosts: %v", err)
	}
	client, err := ssh.Dial("tcp", fixture.addr, &ssh.ClientConfig{
		User: "e2e", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKeyCallback,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("authenticate to e2e-sshd: %v", err)
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		t.Fatalf("open SFTP subsystem: %v", err)
	}
	return client, sftpClient
}
