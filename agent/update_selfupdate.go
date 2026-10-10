package main

// Self-update pipeline ("update" command from the server):
//
//	guard (one attempt at a time, min gap, back-off)
//	→ Windows Installer busy?            → busy back-off, nothing downloaded
//	→ download to a UNIQUE temp file     (oblireach-update-<random>.msi)
//	→ size + SHA-256 + MSI header check  (X-Content-SHA256 / payload sha256)
//	→ Authenticode: same subject as this executable's signer (or the pin)
//	→ Windows Installer busy again?
//	→ msiexec /i … /l*v <unique log>, detached (it stops this service)
//	→ wait for msiexec: if THIS process is still alive when it exits, the
//	  exit code is logged and the back-off applied; otherwise the process
//	  started after the upgrade (or after a rollback) reads the persisted
//	  "pending" attempt and the msiexec log, and logs the result.
//
// Log lines: "self-update: start", "self-update: download ok",
// "self-update: msiexec launched", "self-update: result", plus
// "self-update: … skipped" (rate-limited) and "self-update: failed at …".

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oblireach/agent/engine"
)

const (
	selfUpdateMaxSize = 256 << 20 // refuse anything bigger (disk filling)
	selfUpdateMinSize = 64 << 10  // an error page is not an MSI
	selfUpdatePollGap = 3 * time.Second
	// legacy log (older agent code): wait at most 40 × 3 s for msiexec to end
	selfUpdateLegacyPolls = 40
)

// OLE compound document signature: every .msi starts with it.
var msiMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// Process-wide guard; the state file lives next to config.json.
var selfUpdate = newSelfUpdateGuard(func() string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "update-state.json")
})

// Hooks — platform implementations in update_windows.go / update_stub.go,
// replaced in tests.
var (
	selfUpdateSupported  = platformSelfUpdateSupported
	selfUpdateDir        = func() string { return os.TempDir() }
	selfUpdateHTTPClient = newSelfUpdateHTTPClient
	selfUpdateMSIBusy    = windowsInstallerBusy
	selfUpdateVerifySig  = verifyUpdateSigner
	selfUpdateLaunch     = launchMSIExec
	selfUpdateAlive      = processAlive
	selfUpdateSleep      = time.Sleep
	// Windows build line of this machine (nt6 on Windows < 10) and SHA-2
	// Authenticode support (Windows 7 / 2008 R2 need KB4474419 / KB3033929).
	selfUpdateNeedsNT6    = func() bool { return engine.WindowsVersion().NeedsNT6() }
	selfUpdateSHA2Missing = platformSHA2SigningMissing
)

func newSelfUpdateHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			// Never forward the API key to another host.
			if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				req.Header.Del("X-Api-Key")
			}
			return nil
		},
	}
}

type selfUpdateRequest struct {
	URL    string // relative to the server URL
	SHA256 string // optional (payload); the X-Content-SHA256 header is checked too
	Size   int64  // optional (payload)
}

func updateRequestFromPayload(p map[string]interface{}) (version string, req selfUpdateRequest) {
	version = strings.TrimSpace(payloadString(p, "version"))
	req.URL = strings.TrimSpace(payloadString(p, "url"))
	req.SHA256 = strings.ToLower(strings.TrimSpace(payloadString(p, "sha256")))
	if f, ok := p["size"].(float64); ok && f > 0 {
		req.Size = int64(f)
	}
	return version, req
}

// requestSelfUpdate handles the "update" command (same protocol as before:
// payload {version, url}, optional {sha256, size}). Runs in the command's own
// goroutine and blocks until the attempt ends (or this process is stopped by
// the installer).
func requestSelfUpdate(cfg *Config, cmd *command) {
	version, req := updateRequestFromPayload(cmd.Payload)
	if req.URL == "" {
		log.Printf("self-update: command %s ignored: missing url in payload", cmd.ID)
		return
	}
	d := selfUpdate.begin(cmd.ID, agentVersion, version)
	if !d.OK {
		if d.Log {
			log.Printf("self-update: v%s requested (id=%s) — skipped: %s", version, cmd.ID, d.Detail)
		}
		return
	}
	att := d.Attempt
	res := runSelfUpdate(cfg, att, req)
	next := selfUpdate.finish(att, res)
	logSelfUpdateResult(att, res, next, "")
	// The package is never needed again; the log only when something failed.
	keep := map[string]bool{}
	if res.Outcome != outcomeInstalled && att.Log != "" {
		keep[att.Log] = true
	}
	removeOwnUpdateFile(att.MSI)
	cleanupSelfUpdateFiles(selfUpdateDir(), keep, time.Now())
}

func logSelfUpdateResult(att *selfUpdateAttempt, res selfUpdateResult, next time.Time, prefix string) {
	switch res.Outcome {
	case outcomeInstalled:
		status := ""
		if res.Code >= 0 {
			status = fmt.Sprintf(", msiexec status %d", res.Code)
		}
		log.Printf("self-update: result — %sOK: now running v%s (was v%s, id=%s%s)",
			prefix, agentVersion, att.From, att.ID, status)
	case outcomeUnsupported:
		log.Printf("self-update: result — %sv%s available but self-update is not implemented on this platform; next check not before %s",
			prefix, att.Target, next.Format("2006-01-02 15:04:05"))
	default:
		where := ""
		if att.Log != "" && res.Stage == "msiexec" {
			where = " — msiexec log kept at " + att.Log
		}
		log.Printf("self-update: result — %s%s updating v%s -> v%s (id=%s): %s%s; next attempt not before %s",
			prefix, strings.ToUpper(res.Outcome.String()), att.From, att.Target, att.ID, res.summary(), where,
			next.Format("2006-01-02 15:04:05"))
	}
}

func removeOwnUpdateFile(p string) {
	if p != "" && isOwnUpdateFile(p, selfUpdateDir()) {
		_ = os.Remove(p)
	}
}

// runSelfUpdate performs one attempt. att.MSI / att.Log are filled as soon as
// the files exist so the caller can clean them up.
func runSelfUpdate(cfg *Config, att *selfUpdateAttempt, req selfUpdateRequest) selfUpdateResult {
	log.Printf("self-update: start v%s -> v%s (id=%s)", att.From, att.Target, att.ID)
	if !selfUpdateSupported() {
		return selfUpdateResult{Outcome: outcomeUnsupported, Code: -1}
	}
	// A package of the other Windows build line could only fail in msiexec
	// (launch condition) or install a binary that cannot start here.
	if nt6 := selfUpdateNeedsNT6(); selfUpdateFlavorCheck(req.URL, nt6) != "" {
		return selfUpdateResult{Outcome: outcomeFailed, Stage: "precheck", Code: -1,
			Reason: "wrong_flavor", Detail: selfUpdateFlavorDetail(req.URL, nt6)}
	}
	if selfUpdateMSIBusy() {
		return selfUpdateResult{Outcome: outcomeBusy, Stage: "precheck", Code: -1,
			Detail: "Windows Installer is busy (another MSI installation is running)"}
	}

	dl, res := downloadUpdatePackage(cfg, req, att)
	if res != nil {
		return *res
	}
	signer, err := selfUpdateVerifySig(dl.path)
	if err != nil {
		return selfUpdateSignatureFailure(err, selfUpdateSHA2Missing())
	}
	sigText := "signature ok"
	if signer != "" {
		sigText = fmt.Sprintf("signed by %q", signer)
	}
	log.Printf("self-update: download ok (%d bytes, sha256 %s, %s, %s) -> %s",
		dl.size, dl.sha256, dl.shaCheck, sigText, dl.path)

	if selfUpdateMSIBusy() {
		return selfUpdateResult{Outcome: outcomeBusy, Stage: "precheck", Code: -1,
			Detail: "Windows Installer became busy during the download"}
	}

	logPath := strings.TrimSuffix(dl.path, filepath.Ext(dl.path)) + ".log"
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644); err == nil {
		f.Close()
	} else {
		return selfUpdateResult{Outcome: outcomeFailed, Stage: "launch", Code: -1,
			Detail: "create msiexec log: " + err.Error()}
	}
	att.Log = logPath

	// Persist the pending attempt BEFORE msiexec starts (it may stop this
	// service within seconds), then again with its PID.
	if err := selfUpdate.launched(att); err != nil {
		log.Printf("self-update: warning: cannot persist the pending attempt (%v) — its result will only be logged if this process survives", err)
	}
	pid, wait, err := selfUpdateLaunch(dl.path, logPath, cfg)
	if err != nil {
		return selfUpdateResult{Outcome: outcomeFailed, Stage: "launch", Code: -1, Detail: err.Error()}
	}
	att.PID = pid
	_ = selfUpdate.launched(att)
	log.Printf("self-update: msiexec launched (PID %d, log %s) — the installer stops this service and starts v%s",
		pid, logPath, att.Target)

	started := time.Now()
	code, werr := wait()
	// Still alive: the installer did NOT replace this process.
	elapsed := time.Since(started).Round(time.Second)
	if werr != nil {
		return selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: -1,
			Detail: fmt.Sprintf("waiting for msiexec: %v (after %s)", werr, elapsed)}
	}
	switch code {
	case 1618:
		return selfUpdateResult{Outcome: outcomeBusy, Stage: "msiexec", Code: code,
			Detail: fmt.Sprintf("after %s", elapsed)}
	case 0, 1641, 3010:
		return selfUpdateResult{Outcome: outcomeNoRestart, Stage: "msiexec", Code: code,
			Detail: fmt.Sprintf("v%s is still running after %s (reboot pending, or not running as the service?)", agentVersion, elapsed)}
	}
	return selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: code,
		Detail: fmt.Sprintf("after %s", elapsed)}
}

// selfUpdateSignatureFailure: the result of a refused signature. On Windows
// 7 / Server 2008 R2 without KB4474419 (or KB3033929) WinVerifyTrust cannot
// verify a SHA-256 Authenticode signature at all: every update would be
// refused with an obscure trust error. It is then reported with the
// machine-readable reason "sha2_unsupported" and what to install. The
// package is refused either way (never installed unverified).
func selfUpdateSignatureFailure(err error, sha2Missing bool) selfUpdateResult {
	res := selfUpdateResult{Outcome: outcomeFailed, Stage: "signature", Code: -1, Detail: err.Error()}
	if sha2Missing {
		res.Reason = "sha2_unsupported"
		res.Detail = "sha2_unsupported: this Windows cannot verify SHA-256 code signatures (KB4474419 / KB3033929 not installed) — install KB4474419, then the update is retried (" + err.Error() + ")"
	}
	return res
}

type downloadedPackage struct {
	path     string
	size     int64
	sha256   string
	shaCheck string
}

func downloadUpdatePackage(cfg *Config, req selfUpdateRequest, att *selfUpdateAttempt) (*downloadedPackage, *selfUpdateResult) {
	fail := func(stage, format string, a ...interface{}) (*downloadedPackage, *selfUpdateResult) {
		return nil, &selfUpdateResult{Outcome: outcomeFailed, Stage: stage, Code: -1, Detail: fmt.Sprintf(format, a...)}
	}

	base := strings.TrimRight(cfg.ServerURL, "/")
	fullURL := base + "/" + strings.TrimLeft(req.URL, "/")
	hreq, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return fail("download", "build request: %v", err)
	}
	hreq.Header.Set("X-Api-Key", cfg.APIKey)
	resp, err := selfUpdateHTTPClient().Do(hreq)
	if err != nil {
		return fail("download", "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail("download", "server returned HTTP %d for %s", resp.StatusCode, req.URL)
	}
	if resp.ContentLength > selfUpdateMaxSize {
		return fail("download", "package too large (%d bytes)", resp.ContentLength)
	}

	f, err := os.CreateTemp(selfUpdateDir(), selfUpdateFilePrefix+"*.msi")
	if err != nil {
		return fail("download", "create temp file: %v", err)
	}
	att.MSI = f.Name()
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, selfUpdateMaxSize+1))
	closeErr := f.Close()
	if copyErr != nil {
		return fail("download", "after %d bytes: %v", n, copyErr)
	}
	if closeErr != nil {
		return fail("download", "write %s: %v", f.Name(), closeErr)
	}

	// ── Sanity checks ────────────────────────────────────────────────────
	if n > selfUpdateMaxSize {
		return fail("verify", "package too large (more than %d bytes)", selfUpdateMaxSize)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fail("verify", "truncated download: %d of %d bytes", n, resp.ContentLength)
	}
	if req.Size > 0 && n != req.Size {
		return fail("verify", "size %d does not match the announced %d bytes", n, req.Size)
	}
	if n < selfUpdateMinSize {
		return fail("verify", "package too small (%d bytes)", n)
	}
	got := hex.EncodeToString(h.Sum(nil))
	var checked []string
	for _, c := range []struct{ src, want string }{
		{"payload", req.SHA256},
		{"X-Content-SHA256", strings.ToLower(strings.TrimSpace(resp.Header.Get("X-Content-SHA256")))},
	} {
		if c.want == "" {
			continue
		}
		if c.want != got {
			return fail("verify", "sha256 mismatch: got %s, %s says %s", got, c.src, c.want)
		}
		checked = append(checked, c.src)
	}
	shaCheck := "sha256 not announced by the server"
	if len(checked) > 0 {
		shaCheck = "sha256 matches " + strings.Join(checked, " + ")
	}
	head := make([]byte, len(msiMagic))
	if rf, err := os.Open(f.Name()); err == nil {
		_, _ = io.ReadFull(rf, head)
		rf.Close()
	}
	if string(head) != string(msiMagic) {
		return fail("verify", "downloaded file is not an MSI package")
	}
	return &downloadedPackage{path: f.Name(), size: n, sha256: got, shaCheck: shaCheck}, nil
}

// selfUpdateOnStart logs the result of an attempt launched by the previous
// process (the installer stopped it) and cleans up leftover files. Runs in
// its own goroutine; while it waits for msiexec to finish, the guard keeps
// the attempt in flight so repeated "update" commands are ignored.
func selfUpdateOnStart() {
	att := selfUpdate.adoptPending()
	dir := selfUpdateDir()
	if att == nil {
		reportLegacySelfUpdate(dir)
		cleanupSelfUpdateFiles(dir, nil, time.Now())
		return
	}
	logOK := isOwnUpdateFile(att.Log, dir)
	readLog := func() (int, bool, bool) {
		if !logOK {
			return 0, false, false
		}
		b, err := os.ReadFile(att.Log)
		if err != nil {
			return 0, false, false
		}
		return parseMSILog(b)
	}

	status, found, done := readLog()
	if !done && time.Since(att.StartedAt) < selfUpdatePendingMaxAge {
		log.Printf("self-update: waiting for the msiexec started by the previous process (v%s -> v%s, id=%s, PID %d) to finish",
			att.From, att.Target, att.ID, att.PID)
		deadline := time.Now().Add(selfUpdatePendingWait)
		for !done && time.Now().Before(deadline) {
			if att.PID > 0 && !selfUpdateAlive(att.PID) {
				selfUpdateSleep(selfUpdatePollGap) // let msiexec flush its log
				status, found, done = readLog()
				break
			}
			selfUpdateSleep(selfUpdatePollGap)
			status, found, done = readLog()
		}
	}
	res := concludePending(att, agentVersion, status, found)
	next := selfUpdate.finish(att, res)
	logSelfUpdateResult(att, res, next, "previous attempt: ")
	removeOwnUpdateFile(att.MSI)
	keep := map[string]bool{}
	if res.Outcome == outcomeInstalled {
		removeOwnUpdateFile(att.Log)
	} else if att.Log != "" {
		keep[att.Log] = true
	}
	cleanupSelfUpdateFiles(dir, keep, time.Now())
}

// reportLegacySelfUpdate: agents older than this code updated themselves
// through fixed names (oblireach-update.msi / .log in the same temp dir) and
// never read the result. On the first start after such an update (i.e. the
// transition to this code), log the msiexec status once, then give the log a
// unique name: the retention applies to it and it is not reported again.
func reportLegacySelfUpdate(dir string) {
	p := filepath.Join(dir, "oblireach-update.log")
	info, err := os.Stat(p)
	if err != nil || time.Since(info.ModTime()) > selfUpdatePendingMaxAge {
		return
	}
	var status int
	var found, done bool
	// This process may have been started by that very msiexec, which
	// finishes its log a few seconds later.
	for i := 0; ; i++ {
		if b, err := os.ReadFile(p); err == nil {
			status, found, done = parseMSILog(b)
		}
		if done || i >= selfUpdateLegacyPolls {
			break
		}
		selfUpdateSleep(selfUpdatePollGap)
	}
	if !done {
		return // still running: a later start reports it
	}
	kept := filepath.Join(dir, fmt.Sprintf("%slegacy-%d.log", selfUpdateFilePrefix, time.Now().Unix()))
	if os.Rename(p, kept) != nil {
		kept = p
	}
	if !found {
		log.Printf("self-update: result — previous self-update (older agent code): msiexec status unknown; now running v%s — log %s",
			agentVersion, kept)
		return
	}
	log.Printf("self-update: result — previous self-update (older agent code): msiexec status %d (%s); now running v%s — log %s",
		status, msiExitText(status), agentVersion, kept)
}
