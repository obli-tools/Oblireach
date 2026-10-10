//go:build windows

package engine

// Win32 side of the resolution selector (displaymode.go has the contract).
// Runs in the capture helper — the process that lives in the streamed user
// session and owns its input desktop — so EnumDisplaySettingsEx sees that
// session's monitors and ChangeDisplaySettingsEx changes them. Plain syscall
// (no CGo): user32 / wtsapi32 entry points only.

import (
	"encoding/json"
	"log"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	dmUser32                    = newSystemLazyDLL("user32.dll")
	dmWtsapi32                  = newSystemLazyDLL("wtsapi32.dll")
	dmProcEnumDisplaySettingsEx = dmUser32.NewProc("EnumDisplaySettingsExW")
	dmProcChangeDisplaySettings = dmUser32.NewProc("ChangeDisplaySettingsExW")
	dmProcGetSystemMetrics      = dmUser32.NewProc("GetSystemMetrics")
	dmProcWTSQuerySessionInfo   = dmWtsapi32.NewProc("WTSQuerySessionInformationW")
	dmProcWTSFreeMemory         = dmWtsapi32.NewProc("WTSFreeMemory")
)

const (
	dmEnumCurrentSettings = 0xFFFFFFFF // ENUM_CURRENT_SETTINGS
	dmBitsPerPel          = 0x00040000
	dmPelsWidth           = 0x00080000
	dmPelsHeight          = 0x00100000
	dmDisplayFlags        = 0x00200000
	dmDisplayFrequency    = 0x00400000
	dmDisplayOrientation  = 0x00000080
	dmInterlaced          = 0x00000002 // dmDisplayFlags
	cdsUpdateRegistry     = 0x00000001
	cdsTest               = 0x00000002
	smRemoteSession       = 0x1000
	wtsClientProtocolType = 16
	wtsCurrentSession     = 0xFFFFFFFF
)

// devModeW is DEVMODEW with the display union (220 bytes).
type devModeW struct {
	DeviceName         [32]uint16
	SpecVersion        uint16
	DriverVersion      uint16
	Size               uint16
	DriverExtra        uint16
	Fields             uint32
	PositionX          int32
	PositionY          int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

func newDevMode() devModeW {
	var dm devModeW
	dm.Size = uint16(unsafe.Sizeof(dm))
	return dm
}

func rawFromDevMode(dm *devModeW) rawDisplayMode {
	return rawDisplayMode{
		W: int(dm.PelsWidth), H: int(dm.PelsHeight), Hz: int(dm.DisplayFrequency),
		Bpp: int(dm.BitsPerPel), Orientation: int(dm.DisplayOrientation),
		Interlaced: dm.DisplayFlags&dmInterlaced != 0,
	}
}

func enumDisplaySetting(dev *uint16, n uint32, dm *devModeW) bool {
	r, _, _ := dmProcEnumDisplaySettingsEx.Call(uintptr(unsafe.Pointer(dev)), uintptr(n), uintptr(unsafe.Pointer(dm)), 0)
	return r != 0
}

// winDisplayOps is the production displayOps.
type winDisplayOps struct{}

func (winDisplayOps) current(dev string) (rawDisplayMode, bool) {
	name, err := syscall.UTF16PtrFromString(dev)
	if err != nil {
		return rawDisplayMode{}, false
	}
	dm := newDevMode()
	if !enumDisplaySetting(name, dmEnumCurrentSettings, &dm) {
		return rawDisplayMode{}, false
	}
	return rawFromDevMode(&dm), true
}

// list enumerates every mode Windows offers for dev (no raw / unsupported-by-
// the-monitor modes: EDS_RAWMODE is not passed).
func (winDisplayOps) list(dev string) []rawDisplayMode {
	name, err := syscall.UTF16PtrFromString(dev)
	if err != nil {
		return nil
	}
	var out []rawDisplayMode
	for i := uint32(0); i < 4096; i++ {
		dm := newDevMode()
		if !enumDisplaySetting(name, i, &dm) {
			break
		}
		out = append(out, rawFromDevMode(&dm))
	}
	return out
}

// apply: CDS_TEST first, then the change — dynamic (flags 0) or written to the
// registry (persist). Runs on a thread attached to the input desktop.
func (winDisplayOps) apply(dev string, m rawDisplayMode, persist bool) int {
	name, err := syscall.UTF16PtrFromString(dev)
	if err != nil {
		return dispChangeFailed
	}
	dm := newDevMode()
	dm.PelsWidth, dm.PelsHeight = uint32(m.W), uint32(m.H)
	dm.Fields = dmPelsWidth | dmPelsHeight
	if m.Hz > 1 { // 0/1 = "hardware default"
		dm.DisplayFrequency = uint32(m.Hz)
		dm.Fields |= dmDisplayFrequency
	}
	if m.Bpp > 0 {
		dm.BitsPerPel = uint32(m.Bpp)
		dm.Fields |= dmBitsPerPel
	}
	if rotatedPortrait(m) {
		dm.DisplayOrientation = uint32(m.Orientation)
		dm.Fields |= dmDisplayOrientation
	}
	rc := 0
	onInputDesktopThread(func() {
		call := func(flags uintptr) int {
			r, _, _ := dmProcChangeDisplaySettings.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm)), 0, flags, 0)
			return int(int32(uint32(r)))
		}
		if rc = call(cdsTest); rc != dispChangeSuccessful {
			return
		}
		var flags uintptr
		if persist {
			flags = cdsUpdateRegistry
		}
		rc = call(flags)
	})
	return rc
}

// onInputDesktopThread runs fn on a locked OS thread attached to the desktop
// that receives input (Default, or Winlogon on the lock / UAC screen), like
// the input dispatch thread. Bounded: a display driver that hangs never keeps
// the helper from exiting.
func onInputDesktopThread(fn func()) {
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		inputSwitchActiveDesktop()
		fn()
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		log.Printf("display: mode change still running after 15 s — giving up waiting")
	}
}

// displaySessionIsRDP: the helper's session is an RDP session (its
// resolution is the RDP client's window; Windows refuses a mode change).
func displaySessionIsRDP() bool {
	if r, _, _ := dmProcGetSystemMetrics.Call(smRemoteSession); r != 0 {
		return true
	}
	var buf *uint16
	var n uint32
	r, _, _ := dmProcWTSQuerySessionInfo.Call(0, wtsCurrentSession, wtsClientProtocolType,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || buf == nil {
		return false
	}
	defer dmProcWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	if n < 2 {
		return false
	}
	return *buf == 2 // WTS_PROTOCOL_TYPE_RDP
}

// displayModesSupported: advertised by the Windows capture helper.
func displayModesSupported() bool { return true }

// displayTarget returns the monitor the selector acts on: the one the
// single-monitor view shows, else (mosaic, Magnification fallback, unknown)
// the primary. dev is its GDI device name (\\.\DISPLAYn).
func displayTarget() (idx int, dev string) {
	mons := enumerateMonitors()
	want := -1
	if v, _, ok := getCurrentView(); ok && v.Mode == "monitor" && len(v.Tiles) == 1 {
		want = v.Tiles[0].Index
	}
	for _, m := range mons {
		if m.Index == want && m.Name != "" && m.Width > 0 {
			return m.Index, m.Name
		}
	}
	for _, m := range mons {
		if m.Primary && m.Name != "" {
			return m.Index, m.Name
		}
	}
	for _, m := range mons {
		if m.Name != "" && m.Width > 0 {
			return m.Index, m.Name
		}
	}
	return -1, ""
}

// helperDisplayKeeper is this helper's (= this stream's) bookkeeping.
var helperDisplayKeeper displayKeeper

// handleDisplayModeMsg answers a display_modes_get / display_mode_set.
func handleDisplayModeMsg(payload []byte, reply func([]byte)) {
	var peek struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &peek)
	ops := winDisplayOps{}
	idx, dev := displayTarget()
	rdp := displaySessionIsRDP()
	switch peek.Type {
	case "display_modes_get":
		id := peekDisplayModeID(payload)
		info := displayModesInfo{Monitor: idx}
		cur, ok := rawDisplayMode{}, false
		if dev != "" {
			cur, ok = ops.current(dev)
		}
		if ok {
			info.Modes = shapeDisplayModes(ops.list(dev), cur)
			c := displayMode{W: cur.W, H: cur.H, Hz: cur.Hz, Current: true}
			info.Current = &c
		}
		switch {
		case rdp:
			info.Reason = "rdp"
		case !ok:
			info.Reason = "unavailable"
		default:
			info.Changeable = true
		}
		reply(displayModesMsg(id, info))
	case "display_mode_set":
		req, valid := parseDisplayModeSet(payload)
		if !valid {
			reply(displayModeResult(peekDisplayModeID(payload), false, "invalid", 0))
			return
		}
		if rdp {
			reply(displayModeResult(req.ID, false, "rdp", 0))
			return
		}
		if dev == "" {
			reply(displayModeResult(req.ID, false, "unavailable", 0))
			return
		}
		cur, ok := ops.current(dev)
		if !ok {
			reply(displayModeResult(req.ID, false, "unavailable", 0))
			return
		}
		target, listed := resolveDisplayTarget(ops.list(dev), cur, req)
		if !listed {
			reply(displayModeResult(req.ID, false, "not_listed", 0))
			return
		}
		rc := helperDisplayKeeper.set(ops, dev, target, req.Keep)
		if rc != dispChangeSuccessful {
			log.Printf("display: %s → %dx%d@%d refused (DISP_CHANGE %d)", dev, target.W, target.H, target.Hz, rc)
			reply(displayModeResult(req.ID, false, displayModeCode(rc), rc))
			return
		}
		log.Printf("display: %s %dx%d@%d → %dx%d@%d (keep=%v)", dev, cur.W, cur.H, cur.Hz, target.W, target.H, target.Hz, req.Keep)
		reply(displayModeResult(req.ID, true, "", 0))
	}
}

// restoreDisplayModes puts back what this stream changed (helper exit).
func restoreDisplayModes() {
	if !helperDisplayKeeper.changed() {
		return
	}
	for _, dev := range helperDisplayKeeper.restoreAll(winDisplayOps{}) {
		log.Printf("display: %s restored to its mode before the session", dev)
	}
}
