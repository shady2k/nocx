//go:build windows

package nativeports

// Windows: GetExtendedTcpTable from iphlpapi.dll, bound through
// golang.org/x/sys/windows' DLL machinery — a real API call, no PowerShell,
// no netstat, no cgo. x/sys v0.47.0 does not wrap the TCP table syscalls,
// so the MIB structs and the proc are declared here (the owning PID comes
// from the table itself with TCP_TABLE_OWNER_PID_LISTENER; the process name
// via QueryFullProcessImageName). The reference implementation's
// PowerShell shell-out is a Node constraint nocx does not have.
import (
	"context"
	"encoding/binary"
	"net"
	"path/filepath"
	"unsafe"

	"github.com/shady2k/nocx/internal/portsprobe"
	"golang.org/x/sys/windows"
)

const probeName = "windows-getextendedtcptable"

// Table class, state and the win32 error constant the API reports when the
// buffer is too small. Not exported by x/sys/windows.
const (
	tcpTableOwnerPIDListener = 4
	mibTCPStateListen        = 2
	errInsufficientBuffer    = 122
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

// mibTCPRowOwnerPID mirrors MIB_TCPROW_OWNER_PID: state, the two addresses
// and the owning PID, with the port in network byte order.
type mibTCPRowOwnerPID struct {
	DwState      uint32
	DwLocalAddr  uint32
	DwLocalPort  uint32
	DwRemoteAddr uint32
	DwRemotePort uint32
	DwOwningPid  uint32
}

// mibTCP6RowOwnerPID mirrors MIB_TCP6ROW_OWNER_PID (56 bytes, naturally
// 4-aligned).
type mibTCP6RowOwnerPID struct {
	DwState         uint32
	LocalAddr       [16]byte
	DwLocalScopeId  uint32
	DwLocalPort     uint32
	RemoteAddr      [16]byte
	DwRemoteScopeId uint32
	DwRemotePort    uint32
	DwOwningPid     uint32
}

func listeners(ctx context.Context) ([]portsprobe.Listener, error) {
	v4, err := extendedTCPTable(ctx, windows.AF_INET, false)
	if err != nil {
		return nil, err
	}
	v6, err := extendedTCPTable(ctx, windows.AF_INET6, true)
	if err != nil {
		return nil, err
	}
	return append(v4, v6...), nil
}

// extendedTCPTable reads one address family's LISTEN table. The returned
// port and address bytes are network byte order per the MIB documentation:
// the API hands back the byte-swapped values, so the port is the
// big-endian read of the stored bytes and the address is the stored bytes
// as-is.
func extendedTCPTable(ctx context.Context, af uint32, v6 bool) ([]portsprobe.Listener, error) {
	size := uint32(0)
	var buf []byte
	for {
		// nil table + zero size asks for the buffer size first.
		var tablePtr uintptr
		if len(buf) > 0 {
			tablePtr = uintptr(unsafe.Pointer(&buf[0]))
		}
		r1, _, _ := procGetExtendedTcpTable.Call(
			tablePtr,
			uintptr(unsafe.Pointer(&size)),
			0,
			uintptr(af),
			uintptr(tcpTableOwnerPIDListener),
			0,
		)
		if r1 == 0 {
			break
		}
		if r1 != errInsufficientBuffer || size == 0 {
			return nil, windows.Errno(r1)
		}
		buf = make([]byte, size)
	}
	if len(buf) == 0 {
		return []portsprobe.Listener{}, nil
	}

	rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	if v6 {
		rowSize = int(unsafe.Sizeof(mibTCP6RowOwnerPID{}))
	}
	n := int(binary.LittleEndian.Uint32(buf[:4]))
	out := make([]portsprobe.Listener, 0, n)
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		if v6 {
			row := (*mibTCP6RowOwnerPID)(unsafe.Pointer(&buf[off]))
			if row.DwState != mibTCPStateListen {
				continue
			}
			port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&row.DwLocalPort))[:])
			out = append(out, portsprobe.Listener{
				Family:  portsprobe.FamilyIPv6,
				Address: net.IP(row.LocalAddr[:]).String(),
				Port:    int(port),
				Process: ownerProcess(row.DwOwningPid),
			})
			continue
		}
		row := (*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[off]))
		if row.DwState != mibTCPStateListen {
			continue
		}
		port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&row.DwLocalPort))[:])
		addrBytes := (*[4]byte)(unsafe.Pointer(&row.DwLocalAddr))
		out = append(out, portsprobe.Listener{
			Family:  portsprobe.FamilyIPv4,
			Address: net.IPv4(addrBytes[0], addrBytes[1], addrBytes[2], addrBytes[3]).String(),
			Port:    int(port),
			Process: ownerProcess(row.DwOwningPid),
		})
	}
	return out, nil
}

// ownerProcess names the owning PID. The table hands the PID to any user,
// so evidence is always known; the name may be empty for a process that
// exited between the table read and the query.
func ownerProcess(pid uint32) portsprobe.Process {
	name := ""
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
		defer windows.CloseHandle(h)
		var buf [windows.MAX_PATH + 1]uint16
		size := uint32(len(buf))
		if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err == nil {
			name = filepath.Base(windows.UTF16ToString(buf[:size]))
		}
	}
	return portsprobe.Process{
		Evidence: portsprobe.EvidenceKnown,
		Name:     name,
		PID:      int(pid),
	}
}
