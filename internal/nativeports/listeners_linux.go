//go:build linux

package nativeports

// Linux: /proc/net/tcp and /proc/net/tcp6, with the socket inode matched
// through /proc/*/fd to the owning process. The approach is the reference
// implementation's (Orca's relay port scan); the bounds below are its
// limits: /proc content is neither trusted input nor trusted size.
//
// The state column uses 0A (TCP_LISTEN) only. The inode column is the
// socket inode in the current network namespace; the fd symlink target
// "socket:[N]" matches it. A listener whose inode matches no walkable
// process gets permission-denied evidence — its owner exists but was not
// visible to this user, the same fact non-root ss reports remotely.
import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/discovery"
)

const probeName = "linux-procfs"

// procNetCap bounds one /proc/net/tcp{,6} read. A table beyond the cap is
// unreadable: the sample is failed-transiently, exactly like a remote table
// cut short — never a partial "no ports".
const procNetCap = 8 << 20 // 8 MiB, the reference implementation's table limit

// maxFDWalkEntries is the shared budget for /proc/*/fd entries examined
// across the whole owner walk — the reference's retained-byte budget in
// spirit. An fd explosion in some process must not balloon the sample; once
// exhausted, the remaining inodes stay unmatched and surface as
// permission-denied.
const maxFDWalkEntries = 1 << 18

// procOwner is one matched socket owner.
type procOwner struct {
	pid  int
	name string
}

// sockEntry is one parsed LISTEN row before owner resolution.
type sockEntry struct {
	family  discovery.AddressFamily
	address string
	port    int
	inode   uint64
}

func listeners(ctx context.Context) ([]discovery.Listener, error) {
	v4, err := procNet(ctx, "/proc/net/tcp", discovery.FamilyIPv4)
	if err != nil {
		return nil, err
	}
	v6, err := procNet(ctx, "/proc/net/tcp6", discovery.FamilyIPv6)
	if err != nil {
		return nil, err
	}
	entries := append(v4, v6...)
	if len(entries) == 0 {
		return []discovery.Listener{}, nil
	}

	owners := socketOwners(ctx, entries)
	listeners := make([]discovery.Listener, 0, len(entries))
	for _, e := range entries {
		l := discovery.Listener{Family: e.family, Address: e.address, Port: e.port}
		if o, ok := owners[e.inode]; ok {
			l.Process = discovery.Process{Evidence: discovery.EvidenceKnown, Name: o.name, PID: o.pid}
		} else {
			// Unmatched: either the owner's fd list was unreadable (EACCES
			// — you can only walk the processes you own) or the walk budget
			// ran out. Either way the owner was not visible to this user —
			// permission-denied, never "unowned", rendered exactly like the
			// remote path's same evidence.
			l.Process = discovery.Process{Evidence: discovery.EvidencePermissionDenied}
		}
		listeners = append(listeners, l)
	}
	return listeners, nil
}

// procNet parses one /proc/net/tcp{,6} table: whitespace columns with the
// local address at [1], the state at [3] and the socket inode at [9]. No
// row cap here: the deterministic cap applies AFTER the shared sort (the
// exported Listeners entry point), so the kept set never depends on /proc
// enumeration order.
func procNet(ctx context.Context, path string, family discovery.AddressFamily) ([]sockEntry, error) {
	//nolint:gosec // path is a fixed constant at every call site, or a test's own temp file
	f, err := os.Open(path) // #nosec G304 -- path is application-owned or validated beneath an authorized root
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, procNetCap+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > procNetCap {
		return nil, fmt.Errorf("%s exceeds the %d-byte read cap", path, procNetCap)
	}
	var out []sockEntry
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			continue // header
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[3] != "0A" {
			continue // not TCP_LISTEN
		}
		host, port, ok := parseHexAddress(fields[1])
		if !ok {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		out = append(out, sockEntry{family: family, address: host, port: port, inode: inode})
	}
	return out, nil
}

// parseHexAddress decodes the local_address column: "HEXIP:HEXPORT".
// IPv4 is 8 hex chars in host-byte-order; IPv6 is 32 hex chars — four
// 32-bit words, each little-endian (the reference implementation's
// encoding, confirmed against a shipping scanner).
func parseHexAddress(hexAddr string) (host string, port int, ok bool) {
	parts := strings.Split(hexAddr, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	p, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil || p == 0 {
		return "", 0, false
	}
	hex := parts[0]
	switch len(hex) {
	case 8:
		return net.IPv4(hexByte(hex, 6), hexByte(hex, 4), hexByte(hex, 2), hexByte(hex, 0)).String(), int(p), true
	case 32:
		return ipv6FromHex(hex), int(p), true
	}
	return "", 0, false
}

// hexByte reads one byte from a hex string at the given pair offset.
func hexByte(hex string, offset int) byte {
	v, _ := strconv.ParseUint(hex[offset:offset+2], 16, 8)
	return byte(v)
}

// ipv6FromHex decodes a 32-hex-char address: four 32-bit words, each
// little-endian, then canonical IPv6 text via net.IP.
func ipv6FromHex(hex string) string {
	var b [16]byte
	for w := 0; w < 4; w++ {
		chunk := hex[w*8 : w*8+8]
		for i := 0; i < 4; i++ {
			b[w*4+i] = hexByte(chunk, (3-i)*2)
		}
	}
	return net.IP(b[:]).String()
}

// socketOwners walks /proc/*/fd mapping socket inodes to (pid, name).
// Unreadable fd dirs (EACCES — other users' processes) are skipped: their
// sockets stay unmatched and surface as permission-denied evidence.
func socketOwners(ctx context.Context, entries []sockEntry) map[uint64]procOwner {
	wanted := make(map[uint64]bool, len(entries))
	for _, e := range entries {
		wanted[e.inode] = true
	}
	owners := make(map[uint64]procOwner, len(entries))

	procs, err := os.ReadDir("/proc")
	if err != nil {
		return owners // no owners visible: every row degrades to permission-denied
	}
	budget := maxFDWalkEntries
	for _, p := range procs {
		if ctx.Err() != nil || budget <= 0 {
			break
		}
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		pidDir := "/proc/" + p.Name()
		fds, err := os.ReadDir(pidDir + "/fd")
		if err != nil {
			continue // EACCES: not a process we may inspect
		}
		for _, fd := range fds {
			if budget <= 0 {
				break
			}
			budget--
			target, err := os.Readlink(pidDir + "/fd/" + fd.Name())
			if err != nil {
				continue
			}
			if !strings.HasPrefix(target, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 64)
			if err != nil {
				continue
			}
			if wanted[ino] {
				owners[ino] = procOwner{pid: pid, name: processName(pidDir)}
			}
		}
	}
	return owners
}

// processName reads /proc/<pid>/comm — the same name ss's users: column
// shows (the kernel truncates it to 15 chars). A bounded read: /proc
// content is untrusted size.
func processName(pidDir string) string {
	//nolint:gosec // pidDir is built from a /proc dirent name, never caller input
	f, err := os.Open(pidDir + "/comm") // #nosec G304 -- path is application-owned or validated beneath an authorized root
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
