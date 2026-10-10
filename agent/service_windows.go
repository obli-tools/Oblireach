//go:build windows

package main

import (
	"log"
	"sync"
	"syscall"
	"unsafe"
)

var (
	modKernel32        = syscall.NewLazyDLL("kernel32.dll")
	procSetDllDirectory = modKernel32.NewProc("SetDllDirectoryW")
)

func setDLLSearchPath(dir string) {
	p, _ := syscall.UTF16PtrFromString(dir)
	procSetDllDirectory.Call(uintptr(unsafe.Pointer(p)))
}

var (
	modAdvapi32                      = syscall.NewLazyDLL("advapi32.dll")
	procStartServiceCtrlDispatcherW  = modAdvapi32.NewProc("StartServiceCtrlDispatcherW")
	procRegisterServiceCtrlHandlerEx = modAdvapi32.NewProc("RegisterServiceCtrlHandlerExW")
	procSetServiceStatus             = modAdvapi32.NewProc("SetServiceStatus")
)

// Windows service state / control constants
const (
	svcTypeOwnProcess    = uint32(0x10)
	svcAcceptStop        = uint32(0x00000001)
	svcAcceptShutdown    = uint32(0x00000004)
	svcAcceptSessionChg  = uint32(0x00000080) // SERVICE_ACCEPT_SESSIONCHANGE
	svcStateStopped      = uint32(1)
	svcStateStarting     = uint32(2)
	svcStateStopping     = uint32(3)
	svcStateRunning      = uint32(4)
	svcCtrlStop          = uint32(1)
	svcCtrlShutdown      = uint32(5)
	svcCtrlSessionChange = uint32(0x0000000E) // SERVICE_CONTROL_SESSIONCHANGE
)

// serviceStatus mirrors the Windows SERVICE_STATUS struct.
type serviceStatus struct {
	ServiceType             uint32
	CurrentState            uint32
	ControlsAccepted        uint32
	Win32ExitCode           uint32
	ServiceSpecificExitCode uint32
	CheckPoint              uint32
	WaitHint                uint32
}

var (
	gSvcHandle   uintptr
	gSvcStopOnce sync.Once
	gSvcStopCh   = make(chan struct{})
	gSvcRunFn    func()

	gSvcMainCb uintptr // SERVICE_MAIN callback
	gSvcCtrlCb uintptr // control handler callback
)

func svcSetStatus(state uint32) {
	if gSvcHandle == 0 {
		return
	}
	ss := serviceStatus{
		ServiceType:  svcTypeOwnProcess,
		CurrentState: state,
	}
	switch state {
	case svcStateRunning:
		// SESSIONCHANGE too: the ObliReach follow supervisor watches WTS logon /
		// logoff / console connect-disconnect to move the capture helper into the
		// session the operator is looking at (session_watch_windows.go).
		ss.ControlsAccepted = svcAcceptStop | svcAcceptShutdown | svcAcceptSessionChg
	case svcStateStarting, svcStateStopping:
		ss.WaitHint = 5000
	}
	procSetServiceStatus.Call(gSvcHandle, uintptr(unsafe.Pointer(&ss)))
}

func init() {
	// Control handler: called by SCM on stop/shutdown signals.
	gSvcCtrlCb = syscall.NewCallback(func(ctrl, evType, evData, ctx uintptr) uintptr {
		switch uint32(ctrl) {
		case svcCtrlStop, svcCtrlShutdown:
			svcSetStatus(svcStateStopping)
			gSvcStopOnce.Do(func() { close(gSvcStopCh) })
		case svcCtrlSessionChange:
			// A WTS session changed (logon, logoff, console connect/disconnect,
			// lock/unlock). Wake the follow watcher; it re-reads the WTS state
			// and decides whether to move the capture helper. Non-blocking, and
			// the handler must return promptly.
			select {
			case sessionChangeCh <- struct{}{}:
			default:
			}
		}
		return 0
	})

	// Service main: called by SCM dispatcher thread after StartServiceCtrlDispatcher.
	gSvcMainCb = syscall.NewCallback(func(argc, argv uintptr) uintptr {
		namePtr, err := syscall.UTF16PtrFromString("OblireachAgent")
		if err != nil {
			return 1
		}
		h, _, _ := procRegisterServiceCtrlHandlerEx.Call(
			uintptr(unsafe.Pointer(namePtr)),
			gSvcCtrlCb,
			0,
		)
		gSvcHandle = h
		if h == 0 {
			log.Printf("service: RegisterServiceCtrlHandlerEx failed")
			return 1
		}

		svcSetStatus(svcStateStarting)

		done := make(chan struct{})
		go func() {
			defer close(done)
			gSvcRunFn()
		}()

		svcSetStatus(svcStateRunning)

		select {
		case <-gSvcStopCh:
		case <-done:
		}

		svcSetStatus(svcStateStopping)
		svcSetStatus(svcStateStopped)
		return 0
	})
}

// tryRunAsService tries to hand off to the Windows Service Control Manager.
// If this process was launched by SCM, it registers and runs as a service
// (blocking until stopped) and returns true.
// If launched interactively (console), it returns false immediately.
func tryRunAsService(runFn func()) bool {
	gSvcRunFn = runFn

	namePtr, err := syscall.UTF16PtrFromString("OblireachAgent")
	if err != nil {
		return false
	}

	// SERVICE_TABLE_ENTRYW is two pointers: name + proc, followed by a null entry.
	type svcTableEntry struct {
		name uintptr
		proc uintptr
	}
	table := [2]svcTableEntry{
		{uintptr(unsafe.Pointer(namePtr)), gSvcMainCb},
		{0, 0},
	}

	r, _, callErr := procStartServiceCtrlDispatcherW.Call(uintptr(unsafe.Pointer(&table[0])))
	if r == 0 {
		// ERROR_FAILED_SERVICE_CONTROLLER_CONNECT (1063) = not launched by SCM
		if errno, ok := callErr.(syscall.Errno); ok && errno == 1063 {
			return false
		}
		log.Printf("service: StartServiceCtrlDispatcher failed: %v", callErr)
		return false
	}
	return true
}
