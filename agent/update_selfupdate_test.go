package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type selfUpdateEnv struct {
	dir       string
	statePath string
	logs      *syncBuffer
	busy      atomic.Bool
	sigErr    error
	launches  atomic.Int32
	launch    func(msi, logPath string, cfg *Config) (int, func() (int, error), error)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newSelfUpdateEnv swaps every platform hook and the global guard for the
// duration of a test.
func newSelfUpdateEnv(t *testing.T, running string) *selfUpdateEnv {
	t.Helper()
	env := &selfUpdateEnv{dir: t.TempDir(), logs: &syncBuffer{}}
	env.statePath = filepath.Join(t.TempDir(), "update-state.json")

	saved := struct {
		supported func() bool
		dir       func() string
		client    func() *http.Client
		busy      func() bool
		sig       func(string) (string, error)
		launch    func(string, string, *Config) (int, func() (int, error), error)
		alive     func(int) bool
		sleep     func(time.Duration)
		guard     *selfUpdateGuard
		version   string
		needsNT6  func() bool
		sha2      func() bool
	}{selfUpdateSupported, selfUpdateDir, selfUpdateHTTPClient, selfUpdateMSIBusy, selfUpdateVerifySig,
		selfUpdateLaunch, selfUpdateAlive, selfUpdateSleep, selfUpdate, agentVersion,
		selfUpdateNeedsNT6, selfUpdateSHA2Missing}
	t.Cleanup(func() {
		selfUpdateSupported, selfUpdateDir, selfUpdateHTTPClient = saved.supported, saved.dir, saved.client
		selfUpdateMSIBusy, selfUpdateVerifySig, selfUpdateLaunch = saved.busy, saved.sig, saved.launch
		selfUpdateAlive, selfUpdateSleep, selfUpdate, agentVersion = saved.alive, saved.sleep, saved.guard, saved.version
		selfUpdateNeedsNT6, selfUpdateSHA2Missing = saved.needsNT6, saved.sha2
		log.SetOutput(os.Stderr)
	})
	// A Windows 10+ machine with SHA-2 code signing, whatever runs the test.
	selfUpdateNeedsNT6 = func() bool { return false }
	selfUpdateSHA2Missing = func() bool { return false }

	agentVersion = running
	selfUpdate = newSelfUpdateGuard(func() string { return env.statePath })
	selfUpdateSupported = func() bool { return true }
	selfUpdateDir = func() string { return env.dir }
	selfUpdateMSIBusy = func() bool { return env.busy.Load() }
	selfUpdateVerifySig = func(string) (string, error) {
		if env.sigErr != nil {
			return "", env.sigErr
		}
		return "Test Publisher", nil
	}
	selfUpdateLaunch = func(msi, logPath string, cfg *Config) (int, func() (int, error), error) {
		env.launches.Add(1)
		if env.launch != nil {
			return env.launch(msi, logPath, cfg)
		}
		return 4242, func() (int, error) { return 1603, nil }, nil
	}
	selfUpdateAlive = func(int) bool { return false }
	selfUpdateSleep = func(time.Duration) {}
	log.SetOutput(env.logs)
	return env
}

func (env *selfUpdateEnv) state(t *testing.T) selfUpdateState {
	t.Helper()
	var st selfUpdateState
	data, err := os.ReadFile(env.statePath)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (env *selfUpdateEnv) files(t *testing.T, ext string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(env.dir, "*"+ext))
	return m
}

func fakeMSI(size int) []byte {
	b := make([]byte, size)
	copy(b, msiMagic)
	for i := len(msiMagic); i < size; i++ {
		b[i] = byte(i * 7)
	}
	return b
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type pkgServer struct {
	*httptest.Server
	hits   atomic.Int32
	apiKey atomic.Value
}

func newPkgServer(t *testing.T, body []byte, shaHeader string) *pkgServer {
	t.Helper()
	ps := &pkgServer{}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.hits.Add(1)
		ps.apiKey.Store(r.Header.Get("X-Api-Key"))
		if r.URL.Path != "/api/agent/download/oblireach-agent.msi" {
			http.NotFound(w, r)
			return
		}
		if shaHeader != "" {
			w.Header().Set("X-Content-SHA256", shaHeader)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ps.Close)
	return ps
}

func updateCmd(id, version string) *command {
	return &command{Type: "update", ID: id, Payload: map[string]interface{}{
		"version": version, "url": "/api/agent/download/oblireach-agent.msi",
	}}
}

func TestSelfUpdateWrongSHANeverLaunches(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex([]byte("something else")))

	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_1", "1.0.200"))

	if n := env.launches.Load(); n != 0 {
		t.Fatalf("msiexec launched %d times with a wrong sha256", n)
	}
	st := env.state(t)
	if st.Failures != 1 || !strings.Contains(st.LastError, "sha256 mismatch") {
		t.Fatalf("state = %+v", st)
	}
	if left := env.files(t, ".msi"); len(left) != 0 {
		t.Fatalf("package not removed: %v", left)
	}
	if !strings.Contains(env.logs.String(), "self-update: result — FAILED") {
		t.Fatalf("no result line:\n%s", env.logs.String())
	}
}

func TestSelfUpdatePayloadChecks(t *testing.T) {
	body := fakeMSI(100 << 10)
	cases := []struct {
		name    string
		body    []byte
		payload map[string]interface{}
		want    string
	}{
		{"payload sha256 mismatch", body, map[string]interface{}{"sha256": shaHex([]byte("x"))}, "sha256 mismatch"},
		{"payload size mismatch", body, map[string]interface{}{"size": float64(len(body) + 1)}, "does not match the announced"},
		{"not an MSI", append([]byte("<html>"), make([]byte, 100<<10)...), nil, "not an MSI"},
		{"too small", fakeMSI(1024), nil, "too small"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newSelfUpdateEnv(t, "1.0.199")
			srv := newPkgServer(t, c.body, shaHex(c.body))
			cmd := updateCmd("auto_update_ws_2", "1.0.200")
			for k, v := range c.payload {
				cmd.Payload[k] = v
			}
			requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, cmd)
			if env.launches.Load() != 0 {
				t.Fatalf("launched")
			}
			if st := env.state(t); !strings.Contains(st.LastError, c.want) {
				t.Fatalf("LastError = %q, want %q", st.LastError, c.want)
			}
			if left := env.files(t, ".msi"); len(left) != 0 {
				t.Fatalf("package not removed: %v", left)
			}
		})
	}
}

func TestSelfUpdateBadSignatureNeverLaunches(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	env.sigErr = errors.New(`signed by "Someone Else", not by the publisher of this agent`)
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))

	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_3", "1.0.200"))

	if env.launches.Load() != 0 {
		t.Fatalf("launched despite a bad signature")
	}
	if st := env.state(t); !strings.Contains(st.LastError, "signature") || !strings.Contains(st.LastError, "Someone Else") {
		t.Fatalf("LastError = %q", st.LastError)
	}
}

func TestSelfUpdateBusyInstallerSkipsDownload(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	env.busy.Store(true)
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))

	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_4", "1.0.200"))

	if srv.hits.Load() != 0 || env.launches.Load() != 0 {
		t.Fatalf("busy installer: %d downloads, %d launches", srv.hits.Load(), env.launches.Load())
	}
	st := env.state(t)
	if st.BusyRetries != 1 || st.Failures != 0 || st.NextAttemptAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

func TestSelfUpdateNotNewerIgnored(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.200")
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))

	// e.g. the queued manual "update" delivered to the already-updated build.
	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("manual_update_5", "1.0.200"))

	if srv.hits.Load() != 0 || env.launches.Load() != 0 {
		t.Fatalf("not-newer update ran: %d downloads, %d launches", srv.hits.Load(), env.launches.Load())
	}
	if !strings.Contains(env.logs.String(), "is not newer than the running v1.0.200") {
		t.Fatalf("missing skip line:\n%s", env.logs.String())
	}
}

// The full path: one attempt while the server keeps sending "update", the
// pending attempt persisted while msiexec runs, the exit code logged when
// this process survives it.
func TestSelfUpdateSingleAttemptAndExitCode(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	body := fakeMSI(200 << 10)
	srv := newPkgServer(t, body, shaHex(body))

	type launch struct{ msi, log string }
	launchedCh := make(chan launch, 1)
	exitCh := make(chan int)
	env.launch = func(msi, logPath string, cfg *Config) (int, func() (int, error), error) {
		if cfg.APIKey != "k" {
			t.Errorf("cfg not passed to the launcher")
		}
		var st selfUpdateState
		if data, err := os.ReadFile(env.statePath); err != nil || json.Unmarshal(data, &st) != nil ||
			st.Pending == nil || st.Pending.MSI != msi || st.Pending.Log != logPath {
			t.Errorf("pending attempt not persisted before msiexec started: %+v", st.Pending)
		}
		launchedCh <- launch{msi, logPath}
		return 4242, func() (int, error) { return <-exitCh, nil }, nil
	}

	cfg := &Config{ServerURL: srv.URL, APIKey: "k"}
	done := make(chan struct{})
	go func() {
		requestSelfUpdate(cfg, updateCmd("auto_update_ws_10", "1.0.200"))
		close(done)
	}()

	var l launch
	select {
	case l = <-launchedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("msiexec never launched")
	}
	got, err := os.ReadFile(l.msi)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("package handed to msiexec differs (err=%v)", err)
	}
	if filepath.Dir(l.msi) != env.dir || !strings.HasPrefix(filepath.Base(l.msi), selfUpdateFilePrefix) ||
		strings.TrimSuffix(l.log, ".log") != strings.TrimSuffix(l.msi, ".msi") {
		t.Fatalf("unexpected paths: msi=%s log=%s", l.msi, l.log)
	}
	if _, err := os.Stat(l.log); err != nil {
		t.Fatalf("msiexec log not reserved: %v", err)
	}
	// Persisted before msiexec starts (paths), then with its PID.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := env.state(t)
		if st.Pending != nil && st.Pending.PID == 4242 && st.Pending.MSI == l.msi && st.Pending.Log == l.log {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending attempt not persisted: %+v", st.Pending)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The server repeats the command on every heartbeat meanwhile.
	for i := 0; i < 3; i++ {
		requestSelfUpdate(cfg, updateCmd("auto_update_ws_1"+string(rune('1'+i)), "1.0.200"))
	}
	if srv.hits.Load() != 1 || env.launches.Load() != 1 {
		t.Fatalf("parallel attempts: %d downloads, %d launches", srv.hits.Load(), env.launches.Load())
	}

	exitCh <- 1603
	<-done

	st := env.state(t)
	if st.Pending != nil || st.Failures != 1 || !strings.Contains(st.LastError, "1603") {
		t.Fatalf("state after msiexec 1603 = %+v", st)
	}
	if _, err := os.Stat(l.msi); !os.IsNotExist(err) {
		t.Fatalf("package not removed after the attempt")
	}
	if _, err := os.Stat(l.log); err != nil {
		t.Fatalf("msiexec log of a failed attempt must be kept: %v", err)
	}
	out := env.logs.String()
	for _, want := range []string{
		"self-update: start v1.0.199 -> v1.0.200 (id=auto_update_ws_10)",
		"self-update: download ok (204800 bytes, sha256 " + shaHex(body) + ", sha256 matches X-Content-SHA256, signed by \"Test Publisher\")",
		"self-update: msiexec launched (PID 4242, log " + l.log,
		"self-update: v1.0.200 requested (id=auto_update_ws_11) — skipped: an update to v1.0.200 is already in progress",
		"self-update: result — FAILED updating v1.0.199 -> v1.0.200 (id=auto_update_ws_10): failed at msiexec (msiexec status 1603: fatal error during installation)",
		"msiexec log kept at " + l.log,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n--- log ---\n%s", want, out)
		}
	}
	if strings.Count(out, "skipped:") != 1 {
		t.Errorf("repeated skips must be logged once:\n%s", out)
	}
}

func TestSelfUpdateRedirectToOtherHostDropsAPIKey(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	body := fakeMSI(100 << 10)
	target := newPkgServer(t, body, shaHex(body))
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer front.Close()

	requestSelfUpdate(&Config{ServerURL: front.URL, APIKey: "secret-key"}, updateCmd("auto_update_ws_20", "1.0.200"))

	if target.hits.Load() != 1 {
		t.Fatalf("redirect not followed")
	}
	if k, _ := target.apiKey.Load().(string); k != "" {
		t.Fatalf("API key forwarded to another host: %q", k)
	}
	if env.launches.Load() != 1 {
		t.Fatalf("launches = %d", env.launches.Load())
	}
}

func TestSelfUpdateUnsupportedPlatform(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	selfUpdateSupported = func() bool { return false }
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))
	for i := 0; i < 5; i++ {
		requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_3"+string(rune('0'+i)), "1.0.200"))
	}
	if srv.hits.Load() != 0 {
		t.Fatalf("downloaded on an unsupported platform")
	}
	if n := strings.Count(env.logs.String(), "not implemented on this platform"); n != 1 {
		t.Fatalf("unsupported logged %d times:\n%s", n, env.logs.String())
	}
}

// writePending simulates the process replaced by the installer: pending
// attempt persisted, package + msiexec log on disk.
func writePending(t *testing.T, env *selfUpdateEnv, target string, logBody []byte) *selfUpdateAttempt {
	t.Helper()
	att := &selfUpdateAttempt{
		ID: "auto_update_ws_77", Target: target, From: "1.0.199", StartedAt: time.Now().Add(-time.Minute),
		MSI: filepath.Join(env.dir, "oblireach-update-77.msi"), Log: filepath.Join(env.dir, "oblireach-update-77.log"), PID: 999999,
	}
	if err := os.WriteFile(att.MSI, fakeMSI(1024), 0644); err != nil {
		t.Fatal(err)
	}
	if logBody != nil {
		if err := os.WriteFile(att.Log, logBody, 0644); err != nil {
			t.Fatal(err)
		}
	}
	g := newSelfUpdateGuard(func() string { return env.statePath })
	if d := g.begin(att.ID, att.From, att.Target); !d.OK {
		t.Fatalf("begin: %+v", d)
	}
	if err := g.launched(att); err != nil {
		t.Fatal(err)
	}
	return att
}

func msiLog(status int, finished bool) []byte {
	s := "=== Verbose logging started: 29/09/2026  02:09:49  Build type: SHIP UNICODE 5.00.10011.00 ===\r\n" +
		"MSI (s) (94:EC) [02:10:00:000]: Doing action: StartServices\r\n"
	if finished {
		s += "MSI (s) (94:EC) [02:10:01:100]: Windows Installer installed the product. Installation success or error status: " +
			itoa(status) + ".\r\nMSI (c) (68:48) [02:10:01:300]: MainEngineThread is returning " + itoa(status) +
			"\r\n=== Verbose logging stopped: 29/09/2026  02:10:01 ===\r\n"
	}
	return utf16LEWithBOM(s)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestSelfUpdateOnStartReportsSuccess(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.200") // the new version started
	att := writePending(t, env, "1.0.200", msiLog(0, true))

	selfUpdateOnStart()

	st := env.state(t)
	if st.Pending != nil || st.Failures != 0 || !strings.Contains(st.LastResult, "installed") {
		t.Fatalf("state = %+v", st)
	}
	for _, p := range []string{att.MSI, att.Log} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not cleaned up after a success", filepath.Base(p))
		}
	}
	if want := "self-update: result — previous attempt: OK: now running v1.0.200 (was v1.0.199, id=auto_update_ws_77, msiexec status 0)"; !strings.Contains(env.logs.String(), want) {
		t.Fatalf("log lacks %q:\n%s", want, env.logs.String())
	}
}

func TestSelfUpdateOnStartReportsRollback(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199") // old version restarted by the rollback
	att := writePending(t, env, "1.0.200", msiLog(1603, true))

	selfUpdateOnStart()

	st := env.state(t)
	if st.Pending != nil || st.Failures != 1 || st.NextAttemptAt.IsZero() || !strings.Contains(st.LastError, "1603") {
		t.Fatalf("state = %+v", st)
	}
	if _, err := os.Stat(att.MSI); !os.IsNotExist(err) {
		t.Errorf("package not removed")
	}
	if _, err := os.Stat(att.Log); err != nil {
		t.Errorf("log of the failed attempt must be kept: %v", err)
	}
	if !strings.Contains(env.logs.String(), "self-update: result — previous attempt: FAILED updating v1.0.199 -> v1.0.200") {
		t.Fatalf("no result line:\n%s", env.logs.String())
	}
	// The back-off applies to the next "update" command.
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))
	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_78", "1.0.200"))
	if srv.hits.Load() != 0 {
		t.Fatalf("back-off ignored after the rollback")
	}
}

func TestSelfUpdateOnStartWaitsForMsiexec(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.200")
	att := writePending(t, env, "1.0.200", msiLog(0, false))
	sleeps := 0
	selfUpdateAlive = func(pid int) bool { return true }
	selfUpdateSleep = func(time.Duration) {
		sleeps++
		if sleeps == 3 { // msiexec finishes a few polls later
			_ = os.WriteFile(att.Log, msiLog(0, true), 0644)
		}
	}

	// Meanwhile the server's "update" commands are ignored.
	resCh := make(chan updateDecision, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		resCh <- selfUpdate.begin("x", "1.0.199", "1.0.200")
	}()
	d := <-resCh
	if d.OK || d.Reason != "in_progress" {
		t.Fatalf("command during the wait = %+v", d)
	}

	selfUpdateOnStart()

	if sleeps < 3 {
		t.Fatalf("did not wait for msiexec (sleeps=%d)", sleeps)
	}
	if st := env.state(t); st.Pending != nil || !strings.Contains(st.LastResult, "installed") {
		t.Fatalf("state = %+v", st)
	}
	if !strings.Contains(env.logs.String(), "self-update: waiting for the msiexec started by the previous process") {
		t.Fatalf("no waiting line:\n%s", env.logs.String())
	}
}

func TestSelfUpdateOnStartIgnoresForeignPaths(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.200")
	victim := filepath.Join(t.TempDir(), "important.msi")
	if err := os.WriteFile(victim, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	g := newSelfUpdateGuard(func() string { return env.statePath })
	g.begin("evil", "1.0.199", "1.0.200")
	_ = g.launched(&selfUpdateAttempt{ID: "evil", Target: "1.0.200", From: "1.0.199",
		StartedAt: time.Now().Add(-2 * time.Hour), MSI: victim, Log: victim})

	selfUpdateOnStart()

	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a path from the state file outside the update dir was deleted")
	}
	if st := env.state(t); st.Pending != nil {
		t.Fatalf("pending not concluded: %+v", st)
	}
}

// Transition from the older agent code (fixed file names, result never read).
func TestSelfUpdateOnStartReportsLegacyAttemptOnce(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.200")
	legacy := filepath.Join(env.dir, "oblireach-update.log")
	if err := os.WriteFile(legacy, msiLog(0, false), 0644); err != nil {
		t.Fatal(err)
	}
	sleeps := 0
	selfUpdateSleep = func(time.Duration) {
		sleeps++
		if sleeps == 2 { // the old code's msiexec finishes just after this start
			_ = os.WriteFile(legacy, msiLog(0, true), 0644)
		}
	}

	selfUpdateOnStart()
	selfUpdateOnStart() // a later restart must not report it again

	out := env.logs.String()
	want := "self-update: result — previous self-update (older agent code): msiexec status 0 (success); now running v1.0.200"
	if strings.Count(out, want) != 1 {
		t.Fatalf("legacy result logged %d times, want 1:\n%s", strings.Count(out, want), out)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy log not renamed")
	}
	if m, _ := filepath.Glob(filepath.Join(env.dir, "oblireach-update-legacy-*.log")); len(m) != 1 {
		t.Fatalf("renamed legacy log: %v", m)
	}
}

func TestSelfUpdateOnStartLegacyStillRunning(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.200")
	legacy := filepath.Join(env.dir, "oblireach-update.log")
	if err := os.WriteFile(legacy, msiLog(0, false), 0644); err != nil {
		t.Fatal(err)
	}
	sleeps := 0
	selfUpdateSleep = func(time.Duration) { sleeps++ }

	selfUpdateOnStart()

	if sleeps != selfUpdateLegacyPolls {
		t.Fatalf("polls = %d, want %d", sleeps, selfUpdateLegacyPolls)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("unfinished legacy log must stay for a later start: %v", err)
	}
	if strings.Contains(env.logs.String(), "older agent code") {
		t.Fatalf("unfinished legacy attempt reported:\n%s", env.logs.String())
	}
}
