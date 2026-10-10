package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oblireach/agent/engine"
)

// agentVersion is injected at build time via:
//
//	go build -ldflags="-X main.agentVersion=x.y.z"
var agentVersion = "dev"

var (
	configDir  string
	configFile string
	logFilePath string
)

func init() {
	if runtime.GOOS == "windows" {
		programData := os.Getenv("PROGRAMDATA")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		configDir = filepath.Join(programData, "OblireachAgent")
	} else {
		configDir = "/etc/oblireach-agent"
	}
	configFile   = filepath.Join(configDir, "config.json")
	logFilePath  = filepath.Join(configDir, "oblireach.log")
}

// setupLogging redirects log output to both stdout and a persistent log file.
// The log file is created / appended to at configDir/oblireach.log.
// If the file cannot be opened (e.g. permission issue) we silently fall back
// to stdout-only so the process still starts.
func setupLogging() {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return // no directory — stdout only
	}
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return // can't open — stdout only
	}
	log.SetOutput(io.MultiWriter(f, os.Stdout))
	log.SetFlags(log.Ldate | log.Ltime)
}

// ── Config ────────────────────────────────────────────────────────────────────

type Config struct {
	ServerURL  string `json:"serverUrl"`
	APIKey     string `json:"apiKey"`
	DeviceUUID string `json:"deviceUuid"`
	// IdentityV2: DeviceUUID is the shared Obli* device ID acknowledged by
	// the server (device_identity.go).
	IdentityV2 bool `json:"identityV2,omitempty"`
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// config.json holds the API key: 0600 root on Unix, protected SYSTEM +
	// Administrators DACL on Windows (secretfile_*.go).
	return writeSecretFile(configFile, data)
}

func generateUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// setupConfig: config.json, overridden by the non-empty values the MSI wrote
// under HKLM\SOFTWARE\OblireachAgent\Config (Windows: so that reinstalling
// with new SERVERURL / APIKEY switches the agent over, as the service
// arguments of older MSIs did), overridden by --url / --key.
func setupConfig(urlArg, keyArg string) *Config {
	cfg, err := loadConfig()
	if err != nil {
		cfg = nil
	}
	regURL, regKey := loadConfigFromRegistry()
	if urlArg == "" {
		urlArg = regURL
	}
	if keyArg == "" {
		keyArg = regKey
	}

	if cfg == nil {
		if urlArg == "" || keyArg == "" {
			fmt.Fprintf(os.Stderr, "First run: provide --url <serverUrl> --key <apiKey>\n")
			fmt.Fprintf(os.Stderr, "Example: oblireach-agent --url https://obliance.example.com --key your-api-key\n")
			os.Exit(1)
		}
		cfg = &Config{
			ServerURL: strings.TrimRight(urlArg, "/"),
			APIKey:    keyArg,
		}
	} else {
		// Override URL/key if provided (command line / MSI registry values)
		if urlArg != "" {
			cfg.ServerURL = strings.TrimRight(urlArg, "/")
		}
		if keyArg != "" {
			cfg.APIKey = keyArg
		}
	}

	// Device ID (identity v2, device_identity.go): the stored shared ID once
	// the server acknowledged it, else the legacy resolver (hardware UUID,
	// unchanged) until the server acknowledges the switch to the shared ID.
	storedUUID := cfg.DeviceUUID
	cur, shared := initDeviceIdentity(cfg.IdentityV2, storedUUID,
		func() string { return resolveDeviceUUID(storedUUID) },
		"oblireach-agent/"+agentVersion,
		func(newID string) {
			// Server acknowledged the shared ID: persist it, redial the
			// command WebSocket under the new ID (?uuid=).
			cfg.DeviceUUID = newID
			cfg.IdentityV2 = true
			if err := saveConfig(cfg); err != nil {
				log.Printf("Warning: could not save config: %v", err)
			}
			reconnectCmdWS()
		})
	cfg.DeviceUUID = cur
	if shared {
		cfg.IdentityV2 = true
	}

	// Rewrite config.json on every start, through writeSecretFile: besides
	// persisting overrides and the device UUID, this replaces a file written
	// by an older agent (0644 / inherited ProgramData ACL: readable by every
	// local user) with a 0600 / SYSTEM + Administrators only one.
	serr := saveConfig(cfg)
	saved := serr == nil
	if !saved {
		log.Printf("Warning: could not save config: %v", serr)
		if herr := hardenSecretFile(configFile); herr != nil {
			log.Printf("Warning: could not restrict access to %s: %v", configFile, herr)
		}
	}
	if err := protectAgentRegistrySecrets(); err != nil {
		log.Printf("Warning: could not protect the agent registry key: %v", err)
	}
	// The key must survive in config.json before it leaves the service
	// command line (older MSIs passed it there; readable by every user).
	if saved && cfg.APIKey != "" {
		if done, err := scrubServiceArguments(); err != nil {
			log.Printf("Warning: could not remove the API key from the service command line: %v", err)
		} else if done {
			log.Printf("Service command line: --url / --key arguments removed (config.json carries them)")
		}
	}

	return cfg
}

func main() {
	// Ensure DLLs next to the exe are found (required for Windows services
	// whose working directory is System32, not the exe's directory).
	if exe, err := os.Executable(); err == nil {
		setDLLSearchPath(filepath.Dir(exe))
	}

	setupLogging()

	urlFlag         := flag.String("url", "", "Obliance server URL (first run only)")
	keyFlag         := flag.String("key", "", "Oblireach API key (first run only)")
	captureHelper   := flag.Bool("capture-helper", false, "Run as a capture helper subprocess (internal use)")
	helperAddr      := flag.String("addr", "", "TCP address for capture helper to connect to")
	notifyTitle     := flag.String("notify-title", "", "Show a toast notification with this title (internal use)")
	notifyMsg       := flag.String("notify-msg", "", "Toast notification message body")
	notifyTimeout   := flag.Int("notify-timeout", 8, "Toast auto-close timeout in seconds")
	chatHelper      := flag.Bool("chat-helper", false, "Run as chat helper subprocess (internal use)")
	chatID          := flag.String("chat-id", "", "Chat session ID (chat-helper mode)")
	chatOperator    := flag.String("operator", "", "Operator display name (chat-helper mode)")
	chatUI          := flag.String("chat-ui", "", "Chat window: webview2 (default) or native (chat-helper mode, internal use)")
	pillTest        := flag.Bool("pill-test", false, "Standalone LIVE/REC watermark preview (dev only — shows pill + toggles every 3s)")
	followFlag      := flag.Bool("follow", false, "Capture helper launched by the follow supervisor (internal use)")
	nonceFlag       := flag.String("nonce", "", "One-time handshake token for the supervised capture helper (internal use)")
	genBaseFlag     := flag.Int("gen-base", 0, "View-generation base so gens do not rewind across legs (internal use)")
	dxgiWaitFlag    := flag.Int("dxgi-wait", 0, "Milliseconds the supervised capture helper retries DXGI before a fallback (internal use)")
	noFilesFlag     := flag.Bool("no-files", false, "Capture helper: file transfers are not permitted in this session (internal use)")
	lockWsFlag      := flag.Bool("lock-workstation", false, "Lock this Windows session: run by the service in the session, as its user, when a connection ends (internal use)")
	flag.Parse()

	// ── Lock-the-session mode (lock_on_end_windows.go) ───────────────────────
	// Started by the service in the streamed session, as its signed-in user,
	// when the connection ends and the session is to be locked again.
	if *lockWsFlag {
		os.Exit(runLockWorkstation())
	}

	// Session-follow helper wiring (only meaningful in capture-helper mode).
	// RunningUnderFollow makes agentFeatures advertise session_follow; helperNonce
	// is sent as the first pipe frame; gen-base offsets this leg's view-gen range.
	engine.RunningUnderFollow = *followFlag
	helperNonce = *nonceFlag
	// --no-files: the Obliance server did not permit file transfers in this
	// session (files_gate.go) — no file feature advertised, every file_* refused.
	engine.HelperNoFiles = *noFilesFlag
	if *genBaseFlag > 0 {
		atomic.StoreInt64(&engine.ViewGenCounter, int64(*genBaseFlag))
	}
	if *dxgiWaitFlag > 0 && *dxgiWaitFlag <= maxDXGIWaitMs {
		engine.HelperDXGIWait = time.Duration(*dxgiWaitFlag) * time.Millisecond
	}

	// ── Pill preview mode (dev) ──────────────────────────────────────────────
	// Runs just the watermark pump so you can eyeball the LIVE/REC capsule
	// without rebuilding the MSI / deploying the agent. Toggles LIVE↔REC
	// every 3 seconds. Ctrl+C to exit.
	if *pillTest {
		runPillPreview()
		return
	}

	// ── Toast notification subprocess mode ───────────────────────────────────
	// Launched by the service process inside a user session to show a toast.
	if *notifyTitle != "" {
		runToastNotification(*notifyTitle, *notifyMsg, *notifyTimeout)
		return
	}

	// ── Chat helper subprocess mode ──────────────────────────────────────────
	if *chatHelper {
		if *helperAddr == "" || *chatID == "" {
			fmt.Fprintln(os.Stderr, "--addr and --chat-id required in chat-helper mode")
			os.Exit(1)
		}
		runChatHelperMode(*helperAddr, *chatID, *chatOperator, *chatUI)
		return
	}

	// Per-Monitor-V2 DPI awareness for the capture helper and the service,
	// before any window / DXGI use, so the virtual screen (input) and DXGI
	// capture coordinates share one physical pixel space — fixes the
	// mixed-DPI multi-monitor mouse offset. NOT for the chat /
	// toast / pill helpers above: their windows are sized in fixed pixels and
	// rely on Windows' DPI scaling (they would be tiny on a 150-200 % screen).
	engine.EnablePerMonitorDPIAwareness()

	// ── Capture helper subprocess mode ───────────────────────────────────────
	// Launched by the service process inside a user session to perform DXGI capture.
	if *captureHelper {
		if *helperAddr == "" {
			fmt.Fprintln(os.Stderr, "--addr required in capture-helper mode")
			os.Exit(1)
		}
		runHelperMode(*helperAddr)
		return
	}

	// ── Normal agent mode ─────────────────────────────────────────────────────
	cfg := setupConfig(*urlFlag, *keyFlag)

	log.Printf("Oblireach Agent v%s starting (uuid=%s server=%s)",
		agentVersion, currentDeviceUUID(), cfg.ServerURL)

	// Identity v2: follow regenerations / clone detections of the shared
	// device ID, and push to the server while a switch awaits its ack.
	go watchSharedIdentity(nil)

	// Write version file so the Obliance tray can read it.
	_ = os.WriteFile(filepath.Join(configDir, "version.txt"), []byte(agentVersion), 0644)

	// No keystroke logging: wipe the old input diagnostic log once for this
	// version — earlier builds recorded typed keys (passwords included) there.
	engine.PurgeInputLogOnce(configDir, agentVersion)

	// Result of a self-update launched by the previous process (the installer
	// stopped it) + cleanup of leftover update files. Never blocks the start.
	go selfUpdateOnStart()
	// WebView2 runtime of the chat window: conclude an install attempt the
	// previous process started (never a second installer), first detection.
	go webview2OnStart()

	runFn := func() { runCmdWS(cfg) }

	// Try to run as a Windows service first; fall back to interactive mode.
	if tryRunAsService(runFn) {
		return
	}
	runFn()
}
