package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const capNetBind = 10 // CAP_NET_BIND_SERVICE

// setcapSelf gives this executable the file capability cap_net_bind_service
// in its PERMITTED set only, like "setcap cap_net_bind_service=+p". The
// image build runs it in the final stage (exec form: distroless has no
// shell): a capability set in a build stage does not survive COPY --from
// with the classic builder, which drops extended attributes. Needs
// CAP_SETFCAP (the build step runs as root).
//
// Not "+ep": with the effective bit, the kernel refuses to exec the binary
// at all when the container lacks NET_BIND_SERVICE (cap_drop: ALL behind a
// reverse proxy). Permitted only, the binary always starts and raises the
// capability itself when it is available (raiseBindCap).
func setcapSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	const vfsCapRevision2 = 0x02000000
	// struct vfs_cap_data (revision 2): magic_etc, then {permitted,
	// inheritable} for capabilities 0-31 and 32-63, little endian.
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[0:], vfsCapRevision2)
	binary.LittleEndian.PutUint32(b[4:], 1<<capNetBind)
	if err := syscall.Setxattr(exe, "security.capability", b, 0); err != nil {
		return fmt.Errorf("setcap %s: %w", exe, err)
	}
	return nil
}

type capHeader struct {
	version uint32
	pid     int32
}

type capData struct {
	effective, permitted, inheritable uint32
}

// raiseBindCap makes CAP_NET_BIND_SERVICE effective on every thread when it
// is permitted (file capability + cap_add NET_BIND_SERVICE). It reports
// whether the capability is effective afterwards.
func raiseBindCap() bool {
	hdr := capHeader{version: 0x20080522} // _LINUX_CAPABILITY_VERSION_3
	var data [2]capData
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return false
	}
	const bit = 1 << capNetBind
	if data[0].effective&bit != 0 {
		return true
	}
	if data[0].permitted&bit == 0 {
		return false
	}
	data[0].effective |= bit
	// Capabilities are per thread: set them on all of them (CGO disabled).
	_, _, e := syscall.AllThreadsSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
	return e == 0
}
