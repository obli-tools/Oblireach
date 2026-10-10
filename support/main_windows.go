//go:build windows

// Command OblireachSupport is the portable remote-help client for the person
// being helped: it registers with an oblireach-rdv rendezvous server, shows
// an ephemeral ID and a one-time code, asks the person before anyone
// connects, and runs the ObliReach streaming engine over the end-to-end
// encrypted peer-to-peer link. No installation, no service, runs as the
// signed-in user; nothing is written to disk unless -debug is given.
//
// Build (Windows 10+, amd64, cgo with a MinGW-w64 gcc for the engine):
//
//	go generate
//	go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=0.1.0" -o OblireachSupport.exe .
//
// The nt6 line, OblireachSupport-nt6.exe for Windows 7 SP1 / 8.1 / Server
// 2008 R2 / 2012 / 2012 R2 (Go 1.20, pion/webrtc v3): see go120.mod.
//
// go generate rebuilds resource_windows_amd64.syso (icon, version
// information, manifest: asInvoker, PerMonitorV2, Common Controls v6) with
// goversioninfo.
package main

//go:generate goversioninfo -64 -manifest=OblireachSupport.exe.manifest -o resource_windows_amd64.syso versioninfo.json

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/oblireach/agent/engine"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "0.1.0-dev"

func init() {
	// Win32 windows belong to the thread that created them: the UI runs
	// on the main thread.
	runtime.LockOSThread()
}

func main() {
	server := flag.String("server", "", "rendezvous server (overrides "+ConfigFileName+" and the default)")
	debug := flag.Bool("debug", false, "write a diagnostic log to the temporary folder")
	lang := flag.String("lang", "", "language of the windows (en, fr, pt-BR...; default: the Windows display language)")
	flag.Parse()

	hardenDLLSearch()
	configureEngine()
	engine.EnablePerMonitorDPIAwareness()
	locale := windowsUILanguage()
	if *lang != "" {
		locale = *lang
	}
	tr := newTr(locale)

	logger, closeLog := setupLogging(*debug)
	defer closeLog()

	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	cfg, err := resolveConfig(*server, exeDir)
	if err != nil {
		log.Printf("config: %v", err)
		messageBox(tr.T("app.title"), tr.T("error.config", "error", err.Error()))
		os.Exit(2)
	}
	log.Printf("OblireachSupport %s, server %s (%s), language %s", version, cfg.Server, cfg.Source, tr.Lang())

	a := newApp(tr, cfg)
	backend := &linkBackend{server: cfg.Server, version: version, logger: logger}
	a.ctl = NewController(backend, a, log.Printf)
	if !a.createMain() {
		messageBox(tr.T("app.title"), tr.T("error.window", "error", "CreateWindowEx"))
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.ctl.Run(ctx)
	}()
	messageLoop()

	// The window is gone: end the session and unregister, briefly.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	log.Printf("exit")
}

// configureEngine: nothing written on the helped machine by the engine
// (diagnostic files, policy values), and no third-party DLL loaded from the
// folder of the exe (typically Downloads): system encoders only (Media
// Foundation H.264, then JPEG). Process-wide, before any session.
func configureEngine() {
	engine.SetDiskWrites(false)
	engine.SetBundledDLLs(false)
}

// setupLogging sends the standard logger (the engine's too), the engine's
// diagnostics and the supportlink logger to a file in the temporary folder
// with -debug, and nowhere otherwise.
func setupLogging(debug bool) (*slogLogger, func()) {
	if !debug {
		log.SetOutput(io.Discard)
		return nil, func() {}
	}
	name := fmt.Sprintf("OblireachSupport-%s-%d.log", time.Now().Format("20060102-150405"), os.Getpid())
	f, err := os.OpenFile(filepath.Join(os.TempDir(), name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.SetOutput(io.Discard)
		return nil, func() {}
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	engine.SetDiagnosticOutput(f) // the engine's diagnostics, in this log instead of their own files
	return newDebugLogger(f), func() { f.Close() }
}
