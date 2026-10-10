//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the tools of the elevated script (tasklist,
// ping, msiexec, explorer) when OBLIREACH_FAKE_TOOL is set: the real cmd.exe
// then runs the real generated script, nothing is installed or elevated.
func TestMain(m *testing.M) {
	if os.Getenv("OBLIREACH_FAKE_TOOL") == "1" {
		os.Exit(fakeTool(os.Args[1:]))
	}
	// The windows of the tests are Per-Monitor V2 DPI aware, as the app's are
	// (dpi_windows.go; the test binary has no manifest).
	testDpiAware = enableDpiAwareness()
	os.Exit(m.Run())
}

func fakeLog(name, line string) {
	f, err := os.OpenFile(filepath.Join(os.Getenv("OBLIREACH_FAKE_DIR"), name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}

func fakeTool(args []string) int {
	dir := os.Getenv("OBLIREACH_FAKE_DIR")
	switch {
	case len(args) > 0 && args[0] == "/FI": // tasklist /FI "PID eq n" /FO CSV /NH
		cnt := filepath.Join(dir, "tasklist.count")
		n := 0
		if b, err := os.ReadFile(cnt); err == nil {
			n, _ = strconv.Atoi(string(b))
		}
		n++
		_ = os.WriteFile(cnt, []byte(strconv.Itoa(n)), 0o600)
		// What the script had done by the first round.
		if n == 1 {
			stage := os.Getenv("OBLIREACH_FAKE_STAGE")
			_, e1 := os.Stat(filepath.Join(stage, updateCopiedMarker))
			_, e2 := os.Stat(filepath.Join(stage, updateMSIName))
			fakeLog("tasklist.log", fmt.Sprintf("first round: copied=%v msi=%v args=%q", e1 == nil, e2 == nil, args))
		}
		alive, _ := strconv.Atoi(os.Getenv("OBLIREACH_FAKE_ALIVE"))
		if n <= alive {
			fmt.Println(`"ObliReach.exe","4242","Console","1","10 K"`)
		} else {
			fmt.Println("INFO: No tasks are running which match the specified criteria.")
		}
		return 0
	case len(args) > 0 && args[0] == "-n": // ping -n 2 127.0.0.1
		return 0
	case len(args) > 0 && args[0] == "/i": // msiexec /i <msi> /passive /norestart /l*v <log>
		data, err := os.ReadFile(args[1])
		fakeLog("msiexec.log", fmt.Sprintf("args=%q content=%q err=%v", args, string(data), err))
		rc, _ := strconv.Atoi(os.Getenv("OBLIREACH_FAKE_RC"))
		if len(args) > 5 {
			_ = os.WriteFile(args[5], []byte("fake log"), 0o600)
		}
		return rc
	case len(args) > 0 && args[0] == "/appended": // a line added to the script while it ran
		fakeLog("appended.log", "ran")
		return 0
	default: // explorer <exe>
		fakeLog("explorer.log", fmt.Sprintf("args=%q", args))
		// The script's last step: the app is long gone (so are its locks),
		// another program of the user adds a line to the script.
		if script := os.Getenv("OBLIREACH_FAKE_APPEND"); script != "" {
			self, _ := os.Executable()
			if f, err := os.OpenFile(script, os.O_APPEND|os.O_WRONLY, 0); err == nil {
				fmt.Fprintf(f, "\"%s\" /appended\r\n", self)
				f.Close()
			} else {
				fakeLog("appended.log", "append failed: "+err.Error())
			}
		}
		return 1 // explorer.exe returns 1 even when it worked
	}
}

type scriptRun struct {
	root, fake, base, stage, msi, exe string
	params                            updateScriptParams
	// appendAtEnd: the script is not locked and the relaunch step appends a
	// line to it; edit rewrites the generated script before it runs.
	appendAtEnd bool
	edit        func(s string) string
}

// newScriptRun lays out a private folder holding an MSI, a staging base (with
// an earlier staging folder in it) and the tools, under names with spaces
// and the characters cmd treats specially.
func newScriptRun(t *testing.T) *scriptRun {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	odd := `a b (c) & d^e !f 'g`
	r := &scriptRun{root: root, fake: filepath.Join(root, "fake")}
	r.msi = filepath.Join(root, "tmp "+odd, "Oblireach-update-0123456789abcdef", updateMSIName)
	r.base = filepath.Join(root, "Program Files "+odd, desktopInstallDir, updateStageDir)
	r.stage = filepath.Join(r.base, "fedcba9876543210")
	r.exe = filepath.Join(root, "Program Files "+odd, desktopInstallDir, desktopExeName)
	for _, d := range []string{r.fake, filepath.Dir(r.msi), filepath.Join(r.base, "0000000000000001", "sub")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(r.base, "0000000000000001", "sub", "old.log"), []byte("old"), 0o600)
	if err := os.WriteFile(r.msi, []byte("MSI-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.params = updateScriptParams{Tasklist: self, Find: filepath.Join(os.Getenv("SystemRoot"), "System32", "find.exe"),
		Ping: self, Msiexec: self, Explorer: self, PID: 4242, Image: "ObliReach.exe",
		MSI: r.msi, StageBase: r.base, Stage: r.stage, Exe: r.exe}
	return r
}

// run writes the script into the private folder and runs it the way the app
// has cmd.exe run it (same command line), with a hostile environment: the
// script's own flags pre-set.
func (r *scriptRun) run(t *testing.T, alive, rc int) string {
	t.Helper()
	s, err := updateScript(r.params)
	if err != nil {
		t.Fatal(err)
	}
	if r.edit != nil {
		s = r.edit(s)
	}
	script := filepath.Join(filepath.Dir(r.msi), "update.cmd")
	if err := os.WriteFile(script, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	// As in the app: the MSI and the script are held deny-write meanwhile
	// (the script only until the app exits, which appendAtEnd plays).
	locked := []string{r.msi, script}
	var extra []string
	if r.appendAtEnd {
		locked = locked[:1]
		extra = append(extra, "OBLIREACH_FAKE_APPEND="+script)
	}
	for _, p := range locked {
		if h, err := lockFileRead(p); err == nil {
			defer syscall.CloseHandle(h)
		}
	}
	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	c := exec.Command(cmdExe)
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + cmdExe + `" ` + updateCmdArgs(script), HideWindow: true}
	c.Env = append(os.Environ(), "OBLIREACH_FAKE_TOOL=1", "OBLIREACH_FAKE_DIR="+r.fake, "OBLIREACH_FAKE_STAGE="+r.stage,
		"OBLIREACH_FAKE_ALIVE="+strconv.Itoa(alive), "OBLIREACH_FAKE_RC="+strconv.Itoa(rc),
		"OBR_GONE=1", "OBR_OK=1", "OBR_RC=0", "OBR_ABORT=")
	c.Env = append(c.Env, extra...)
	done := make(chan error, 1)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("the script did not end")
	}
	return out.String()
}

func (r *scriptRun) log(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.fake, name))
	return string(b)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestUpdateScriptRunsInstall(t *testing.T) {
	r := newScriptRun(t)
	out := r.run(t, 2, 0)
	if exists(filepath.Join(r.base, "0000000000000001")) {
		t.Fatalf("earlier staging folder kept (%s)", out)
	}
	tl := r.log("tasklist.log")
	if !strings.Contains(tl, "first round: copied=true msi=true") || !strings.Contains(tl, `"PID eq 4242"`) {
		t.Fatalf("the MSI was not copied before the wait: %q (%s)", tl, out)
	}
	if n, _ := os.ReadFile(filepath.Join(r.fake, "tasklist.count")); string(n) != "3" {
		t.Fatalf("tasklist rounds = %s, want 3 (alive twice, then gone)", n)
	}
	ml := r.log("msiexec.log")
	stageMSI := filepath.Join(r.stage, updateMSIName)
	want := fmt.Sprintf("args=%q", []string{"/i", stageMSI, "/passive", "/norestart", "/l*v", filepath.Join(r.stage, updateLogFile)})
	if !strings.Contains(ml, want) || !strings.Contains(ml, `content="MSI-CONTENT"`) {
		t.Fatalf("msiexec: %q, want %s", ml, want)
	}
	if exists(r.stage) || exists(r.base) {
		t.Fatalf("success: the staging folder and its empty base go (stage %v, base %v)", exists(r.stage), exists(r.base))
	}
	if el := r.log("explorer.log"); !strings.Contains(el, fmt.Sprintf("args=%q", []string{r.exe})) {
		t.Fatalf("relaunch: %q", el)
	}
	if !exists(r.msi) {
		t.Fatal("the script must not touch the private folder (the app cleans it)")
	}
}

func TestUpdateScriptRunsFailedInstall(t *testing.T) {
	for _, rc := range []int{1603, 3010} {
		r := newScriptRun(t)
		out := r.run(t, 0, rc)
		res, err := os.ReadFile(filepath.Join(r.stage, updateResultFile))
		if err != nil {
			t.Fatalf("rc %d: no result file (%v): %s", rc, err, out)
		}
		if n, ok := parseUpdateRC(string(res)); !ok || n != rc {
			t.Fatalf("rc %d: result %q", rc, res)
		}
		if exists(filepath.Join(r.stage, updateMSIName)) || !exists(filepath.Join(r.stage, updateLogFile)) {
			t.Fatalf("rc %d: the copy goes, the log stays", rc)
		}
		if exists(filepath.Join(r.stage, updateCopiedMarker)) || exists(filepath.Join(r.stage, "pid.txt")) {
			t.Fatalf("rc %d: markers left", rc)
		}
		if !strings.Contains(r.log("explorer.log"), fmt.Sprintf("%q", r.exe)) {
			t.Fatalf("rc %d: Oblireach must start again (the old version): %s", rc, out)
		}
	}
}

// The app never goes away: nothing is installed, nothing relaunched, and the
// copy goes.
func TestUpdateScriptAbortsWhileAppRuns(t *testing.T) {
	if raceBuild {
		t.Skip("120 tool starts: too slow with -race")
	}
	r := newScriptRun(t)
	r.run(t, 1000, 0)
	if n, _ := os.ReadFile(filepath.Join(r.fake, "tasklist.count")); string(n) != strconv.Itoa(updateWaitRounds) {
		t.Fatalf("rounds = %s, want %d", n, updateWaitRounds)
	}
	if r.log("msiexec.log") != "" || r.log("explorer.log") != "" {
		t.Fatal("installed or relaunched while the app still runs")
	}
	if exists(r.stage) {
		t.Fatal("the staging folder stays after an abort")
	}
}

// The MSI is gone (the app gave up and cleaned its folder): nothing runs.
func TestUpdateScriptAbortsWithoutMSI(t *testing.T) {
	r := newScriptRun(t)
	if err := os.Remove(r.msi); err != nil {
		t.Fatal(err)
	}
	r.run(t, 0, 0)
	if r.log("tasklist.log") != "" || r.log("msiexec.log") != "" || r.log("explorer.log") != "" || exists(filepath.Join(r.stage, updateCopiedMarker)) {
		t.Fatal("went on without its MSI")
	}
	if exists(r.stage) || exists(r.base) {
		t.Fatal("an aborted run leaves its staging folder")
	}
}

// Once the app has exited, the script (in the user's %TEMP%) is no longer
// locked while the elevated cmd still runs it: a line added then must never
// run. The control run, without the final exit, shows that cmd does read the
// file again after the block.
func TestUpdateScriptIgnoresAppendedLines(t *testing.T) {
	r := newScriptRun(t)
	r.appendAtEnd = true
	out := r.run(t, 0, 0)
	if !strings.Contains(r.log("explorer.log"), fmt.Sprintf("%q", r.exe)) {
		t.Fatalf("the script did not reach its relaunch: %s", out)
	}
	if got := r.log("appended.log"); got != "" {
		t.Fatalf("appended line: %q (%s)", got, out)
	}
	script, _ := os.ReadFile(filepath.Join(filepath.Dir(r.msi), "update.cmd"))
	if !strings.HasSuffix(string(script), "/appended\r\n") {
		t.Fatalf("nothing was appended:\n%s", script)
	}

	c := newScriptRun(t)
	c.appendAtEnd = true
	c.edit = func(s string) string { return strings.Replace(s, "\r\nexit /b 0\r\n", "\r\n", 1) }
	c.run(t, 0, 0)
	if cs, _ := os.ReadFile(filepath.Join(filepath.Dir(c.msi), "update.cmd")); strings.Contains(string(cs), "exit /b") {
		t.Fatalf("control: the exit is still there:\n%s", cs)
	}
	if got := c.log("appended.log"); got != "ran\n" && got != "ran\r\n" {
		t.Fatalf("control: without the final exit, cmd should have run the appended line (got %q): the test proves nothing here", got)
	}
}

// ── Authenticode, on the real signed build artefacts when present ─────────

func TestAuthenticodeUnsigned(t *testing.T) {
	p := filepath.Join(t.TempDir(), "unsigned.msi")
	_ = os.WriteFile(p, []byte("not an installer"), 0o600)
	_, err := verifyAuthenticode(p, 0)
	var te trustError
	if !errors.As(err, &te) || te.Error() == "" {
		t.Fatalf("unsigned file: %v", err)
	}
	// This test binary is not signed either: an unsigned build has no
	// reference signer (the update refuses to run).
	self, _ := os.Executable()
	if _, err := verifyAuthenticode(self, 0); err == nil {
		t.Fatal("the unsigned test binary verified")
	}
	if _, err := msiProperties(p, "ProductVersion"); err == nil {
		t.Fatal("a text file read as an MSI")
	}
}

func TestAuthenticodeDistArtefacts(t *testing.T) {
	exe, msi := filepath.Join("dist", "Oblireach.exe"), filepath.Join("dist", "ObliReach.msi")
	for _, p := range []string{exe, msi} {
		if !exists(p) {
			t.Skipf("%s not built here", p)
		}
	}
	own, err := verifyAuthenticode(exe, 0)
	if err != nil {
		t.Skipf("the built exe does not verify here (%v)", err)
	}
	if len(own.subject) == 0 || own.name == "" {
		t.Fatalf("signer of the exe: %+v", own)
	}
	// The MSI, locked like the updater locks it, verified through that handle.
	cp := filepath.Join(t.TempDir(), updateMSIName)
	data, err := os.ReadFile(msi)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(cp, data, 0o600)
	h, err := lockFileRead(cp)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := verifyAuthenticode(cp, h)
	if err != nil {
		syscall.CloseHandle(h)
		t.Fatalf("dist MSI: %v", err)
	}
	if !sameSigner(sig, own) {
		t.Fatalf("MSI signer %q != exe signer %q", sig.name, own.name)
	}
	props, err := msiProperties(cp, "ProductVersion", "UpgradeCode", "ProductName")
	if err != nil || !sameUpgradeCode(props["UpgradeCode"], desktopUpgradeCode) {
		t.Fatalf("MSI properties (locked): %v %v", props, err)
	}
	if _, ok := parseVersion(props["ProductVersion"]); !ok {
		t.Fatalf("ProductVersion %q", props["ProductVersion"])
	}
	// Locked: no write, no delete, no rename, not even of its folder.
	if f, err := os.OpenFile(cp, os.O_WRONLY, 0); err == nil {
		f.Close()
		t.Fatal("the locked MSI opened for writing")
	}
	if err := os.Remove(cp); err == nil {
		t.Fatal("the locked MSI was deleted")
	}
	if err := os.Rename(filepath.Dir(cp), filepath.Dir(cp)+"-moved"); err == nil {
		t.Fatal("the folder of the locked MSI was renamed")
	}
	syscall.CloseHandle(h)

	// Another subject: refused; a modified copy: refused.
	if sameSigner(signerInfo{subject: []byte("CN=Other")}, own) || sameSigner(signerInfo{}, signerInfo{}) {
		t.Fatal("sameSigner accepted another publisher")
	}
	bad := append([]byte(nil), data...)
	bad[len(bad)/2] ^= 0xFF
	bp := filepath.Join(t.TempDir(), "tampered.msi")
	_ = os.WriteFile(bp, bad, 0o600)
	if _, err := verifyAuthenticode(bp, 0); err == nil {
		t.Fatal("a tampered MSI verified")
	}
}

// ── The update run, with fakes around the real code ──────────────────────

type fakeProc struct{ gone bool }

func (p *fakeProc) exited() bool { return p.gone }
func (p *fakeProc) release()     {}

type updTest struct {
	t        *testing.T
	u        *desktopUpdater
	srv      *httptest.Server
	paths    []string
	body     []byte
	mu       sync.Mutex
	pushes   []updateStatus
	saves    []*pendingUpdate
	elevated []string
	quits    int
	dirs     []string
	props    map[string]string
	own      signerInfo
	ownErr   error
	sig      signerInfo
	sigErr   error
	elevErr  error
	proc     *fakeProc
	copied   bool
}

func newUpdTest(t *testing.T) *updTest {
	ut := &updTest{t: t, body: bytes.Repeat([]byte("0123456789"), 70000),
		props: map[string]string{"ProductVersion": "1.0.12", "UpgradeCode": desktopUpgradeCode},
		own:   signerInfo{subject: []byte("CN=Pub"), name: "Pub", thumbprint: [20]byte{1}},
		sig:   signerInfo{subject: []byte("CN=Pub"), name: "Pub", thumbprint: [20]byte{2}},
		proc:  &fakeProc{}, copied: true}
	ut.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ut.mu.Lock()
		ut.paths = append(ut.paths, r.URL.RequestURI())
		ut.mu.Unlock()
		w.Header().Set("Content-Length", strconv.Itoa(len(ut.body)))
		for i := 0; i < len(ut.body); i += 100000 {
			_, _ = w.Write(ut.body[i:min(i+100000, len(ut.body))])
		}
	}))
	t.Cleanup(ut.srv.Close)
	tmp := t.TempDir()
	env := updaterEnv{
		appVersion: "1.0.11",
		serverURL:  func() string { return ut.srv.URL + "/ignored/path?x=1" },
		client:     updateHTTPClient(),
		tempRoot:   func() string { return tmp },
		privateDir: func(root string) (string, error) {
			d, err := makePrivateDir(root)
			ut.dirs = append(ut.dirs, d)
			return d, err
		},
		ownSigner: func() (signerInfo, error) { return ut.own, ut.ownErr },
		verify:    func(string, syscall.Handle) (signerInfo, error) { return ut.sig, ut.sigErr },
		msiProps:  func(string) (map[string]string, error) { return ut.props, nil },
		lock:      lockFileRead,
		unlock:    func(h syscall.Handle) { syscall.CloseHandle(h) },
		safePath:  scriptSafePath,
		tools: func() (updateTools, error) {
			return updateTools{Cmd: `C:\Windows\System32\cmd.exe`, Tasklist: `C:\Windows\System32\tasklist.exe`, Find: `C:\Windows\System32\find.exe`,
				Ping: `C:\Windows\System32\PING.EXE`, Msiexec: `C:\Windows\System32\msiexec.exe`, Explorer: `C:\Windows\explorer.exe`,
				SysDir: `C:\Windows\System32`, StageBase: `C:\Program Files\OblireachClient\Update`,
				Exe: `C:\Program Files\OblireachClient\ObliReach.exe`, Image: "ObliReach.exe", PID: 77}, nil
		},
		elevate: func(file, params, dir string) (elevatedProc, error) {
			ut.mu.Lock()
			ut.elevated = append(ut.elevated, file+"|"+params+"|"+dir)
			ut.mu.Unlock()
			if ut.elevErr != nil {
				return nil, ut.elevErr
			}
			return ut.proc, nil
		},
		copied: func(string) bool { return ut.copied },
		quit:   func() { ut.mu.Lock(); ut.quits++; ut.mu.Unlock() },
		save: func(pu *pendingUpdate) {
			ut.mu.Lock()
			ut.saves = append(ut.saves, pu)
			ut.mu.Unlock()
		},
		push: func(s updateStatus) {
			ut.mu.Lock()
			ut.pushes = append(ut.pushes, s)
			ut.mu.Unlock()
		},
		now:        time.Now,
		pollEvery:  5 * time.Millisecond,
		copyWait:   2 * time.Second,
		progressIv: 0,
	}
	ut.u = newDesktopUpdater(env, nil)
	return ut
}

// runSync: one run, to its end (the app closing, or an error pushed).
func (ut *updTest) runSync() error {
	err := ut.u.install()
	ut.u.finish(err)
	return err
}

func (ut *updTest) states() []string {
	ut.mu.Lock()
	defer ut.mu.Unlock()
	var out []string
	for _, s := range ut.pushes {
		if len(out) == 0 || out[len(out)-1] != s.State {
			out = append(out, s.State)
		}
	}
	return out
}

func (ut *updTest) lastPush() updateStatus {
	ut.mu.Lock()
	defer ut.mu.Unlock()
	return ut.pushes[len(ut.pushes)-1]
}

func TestUpdaterInstallFlow(t *testing.T) {
	ut := newUpdTest(t)
	if err := ut.runSync(); err != nil {
		t.Fatal(err)
	}
	// The fixed URL of the active server, nothing else.
	if len(ut.paths) != 1 || ut.paths[0] != desktopMSIPath {
		t.Fatalf("requests: %v", ut.paths)
	}
	if got := strings.Join(ut.states(), ","); got != "downloading,verifying,elevating,installing" {
		t.Fatalf("states: %s", got)
	}
	var pcts []int
	for _, s := range ut.pushes {
		if s.State == "downloading" {
			pcts = append(pcts, s.Percent)
		}
	}
	if pcts[0] != 0 || pcts[len(pcts)-1] != 100 || len(pcts) < 4 {
		t.Fatalf("progress: %v", pcts)
	}
	for i := 1; i < len(pcts); i++ {
		if pcts[i] < pcts[i-1] {
			t.Fatalf("progress goes back: %v", pcts)
		}
	}
	dir := ut.dirs[0]
	msi := filepath.Join(dir, updateMSIName)
	if data, _ := os.ReadFile(msi); !bytes.Equal(data, ut.body) {
		t.Fatal("downloaded content differs")
	}
	// Kept deny-write until the process ends; the script too.
	if f, err := os.OpenFile(msi, os.O_WRONLY, 0); err == nil {
		f.Close()
		t.Fatal("the MSI is writable after its verification")
	}
	script := filepath.Join(dir, "update.cmd")
	if f, err := os.OpenFile(script, os.O_WRONLY, 0); err == nil {
		f.Close()
		t.Fatal("the script is writable before it runs")
	}
	if len(ut.u.held) != 2 {
		t.Fatalf("held handles: %d", len(ut.u.held))
	}
	// The pending record, then the elevation of cmd.exe on that script.
	if len(ut.saves) != 1 || ut.saves[0] == nil || ut.saves[0].Version != "1.0.12" || ut.saves[0].From != "1.0.11" || ut.saves[0].Dir != dir ||
		!strings.HasPrefix(ut.saves[0].Stage, `C:\Program Files\OblireachClient\Update\`) {
		t.Fatalf("pending: %+v", ut.saves)
	}
	if len(ut.elevated) != 1 || ut.elevated[0] != `C:\Windows\System32\cmd.exe|/d /e:on /v:off /s /c ""`+script+`""|C:\Windows\System32` {
		t.Fatalf("elevated: %v", ut.elevated)
	}
	content, _ := os.ReadFile(script)
	if !strings.Contains(string(content), `copy /b /y "`+msi+`" "`+ut.saves[0].Stage+`\OblireachDesktop.msi"`) ||
		!strings.Contains(string(content), `/FI "PID eq 77"`) {
		t.Fatalf("script:\n%s", content)
	}
	if ut.quits != 1 {
		t.Fatalf("quit %d times", ut.quits)
	}
	for _, h := range ut.u.held {
		syscall.CloseHandle(h)
	}
}

func TestUpdaterRefusals(t *testing.T) {
	cases := []struct {
		name, code string
		setup      func(ut *updTest)
		requested  bool // the download ran
	}{
		{"unsigned app", "unsigned_app", func(ut *updTest) { ut.ownErr = trustError{0x800B0100} }, false},
		{"app not verifiable", "unsigned_app", func(ut *updTest) { ut.ownErr = trustError{0x800B0109} }, false},
		{"no server", "no_server", func(ut *updTest) { ut.u.env.serverURL = func() string { return "" } }, false},
		{"bad signature", "signature", func(ut *updTest) { ut.sigErr = trustError{0x80096010} }, true},
		{"other publisher", "signer_mismatch", func(ut *updTest) { ut.sig = signerInfo{subject: []byte("CN=Evil"), name: "Evil"} }, true},
		{"agent MSI", "not_desktop", func(ut *updTest) { ut.props["UpgradeCode"] = "{11111111-2222-3333-4444-555555555555}" }, true},
		{"no version", "not_desktop", func(ut *updTest) { ut.props["ProductVersion"] = "" }, true},
		{"same version", "not_newer", func(ut *updTest) { ut.props["ProductVersion"] = "1.0.11" }, true},
		{"downgrade", "not_newer", func(ut *updTest) { ut.props["ProductVersion"] = "1.0.9" }, true},
		{"UAC declined", "cancelled", func(ut *updTest) { ut.elevErr = errUACDeclined }, true},
		{"elevation error", "elevation", func(ut *updTest) { ut.elevErr = syscall.Errno(2) }, true},
		{"script stopped", "installer_not_started", func(ut *updTest) { ut.copied = false; ut.proc.gone = true }, true},
		{"script too slow", "installer_not_started", func(ut *updTest) { ut.copied = false; ut.u.env.copyWait = 50 * time.Millisecond }, true},
		{"script changed before its lock", "tampered", func(ut *updTest) {
			ut.u.env.lock = func(p string) (syscall.Handle, error) {
				if strings.HasSuffix(p, "update.cmd") { // another program of this user, first
					_ = os.WriteFile(p, []byte("@echo off\r\ncalc.exe\r\n"), 0o600)
				}
				return lockFileRead(p)
			}
		}, true},
		{"not found", "http_status", func(ut *updTest) {
			ut.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ut.paths = append(ut.paths, r.URL.Path)
				http.NotFound(w, r)
			})
		}, true},
		{"too large", "too_large", func(ut *updTest) {
			ut.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ut.paths = append(ut.paths, r.URL.Path)
				w.Header().Set("Content-Length", strconv.Itoa(maxUpdateMSISize+1))
				w.WriteHeader(200)
			})
		}, true},
		{"cut short", "network", func(ut *updTest) {
			ut.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ut.paths = append(ut.paths, r.URL.Path)
				w.Header().Set("Content-Length", "1000")
				_, _ = w.Write([]byte("short"))
			})
		}, true},
	}
	for _, c := range cases {
		ut := newUpdTest(t)
		c.setup(ut)
		err := ut.runSync()
		var ue *updateErr
		if !errors.As(err, &ue) || ue.Code != c.code || ue.Message == "" {
			t.Fatalf("%s: %v, want %s", c.name, err, c.code)
		}
		if (len(ut.paths) > 0) != c.requested {
			t.Fatalf("%s: requests %v", c.name, ut.paths)
		}
		last := ut.lastPush()
		wantState := "error"
		if c.code == "cancelled" {
			wantState = "cancelled"
		}
		if last.State != wantState || last.Code != c.code || last.Message != ue.Message {
			t.Fatalf("%s: last push %+v", c.name, last)
		}
		if ut.quits != 0 || ut.u.busy {
			t.Fatalf("%s: quit %d, busy %v", c.name, ut.quits, ut.u.busy)
		}
		// Nothing is ever elevated before every check passed.
		if elevatedStep := c.code == "cancelled" || c.code == "elevation" || c.code == "installer_not_started"; elevatedStep != (len(ut.elevated) == 1) {
			t.Fatalf("%s: elevated %v", c.name, ut.elevated)
		}
		// Nothing left behind: the folder goes, a pending record is undone.
		for _, d := range ut.dirs {
			if exists(d) {
				t.Fatalf("%s: folder %s kept", c.name, d)
			}
		}
		if n := len(ut.saves); n > 0 && ut.saves[n-1] != nil {
			t.Fatalf("%s: pending record left: %+v", c.name, ut.saves)
		}
		if c.code == "cancelled" && (len(ut.saves) != 2 || !strings.Contains(ue.Message, "declined")) {
			t.Fatalf("UAC declined: %v %q", ut.saves, ue.Message)
		}
	}
}

// A redirect is never followed, even to the same server.
func TestUpdaterRefusesRedirects(t *testing.T) {
	var other int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other++ }))
	defer target.Close()
	ut := newUpdTest(t)
	ut.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ut.paths = append(ut.paths, r.URL.Path)
		http.Redirect(w, r, target.URL+"/evil.msi", http.StatusFound)
	})
	err := ut.runSync()
	var ue *updateErr
	if !errors.As(err, &ue) || ue.Code != "redirect" || other != 0 {
		t.Fatalf("redirect: %v (followed %d)", err, other)
	}
}

// One update at a time; the binding takes nothing but the token.
func TestUpdateBindings(t *testing.T) {
	b, host, _, _, _ := testMainBridge(t)
	if _, err := b.installUpdate("nope"); err != errNotAllowed {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := b.installUpdate(b.token); err == nil {
		t.Fatal("no updater: accepted")
	}
	if s, err := b.updateState(b.token); err != nil || s.State != "idle" {
		t.Fatalf("state without updater: %+v %v", s, err)
	}
	ut := newUpdTest(t)
	block := make(chan struct{})
	ut.u.env.ownSigner = func() (signerInfo, error) { <-block; return ut.own, errors.New("stop here") }
	ut.u.last = &updateResult{OK: true, Version: "1.0.11", Message: "Oblireach was updated to v1.0.11."}
	b.updater = ut.u
	if s, _ := b.updateState(b.token); s.Result == nil || !s.Result.OK {
		t.Fatalf("result told: %+v", s)
	}
	if s, _ := b.updateState(b.token); s.Result != nil {
		t.Fatal("the result is told once")
	}
	s1, err := b.installUpdate(b.token)
	s2, _ := b.installUpdate(b.token)
	if err != nil || s1.State != "downloading" || s2.State != "downloading" || !ut.u.busy || s1.Seq == 0 || s2.Seq != s1.Seq {
		t.Fatalf("start: %+v %+v %v", s1, s2, err)
	}
	close(block)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ut.u.mu.Lock()
		busy := ut.u.busy
		ut.u.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s, _ := b.updateState(b.token); s.State != "error" || s.Code != "unsigned_app" || s.Seq <= s1.Seq {
		t.Fatalf("after the run: %+v", s)
	}
	// The main window binds them when it has them; nothing else does.
	b.startup = &startupCtl{}
	host.binds = nil
	b.bind()
	if got := strings.Join(host.binds[len(host.binds)-4:], ","); got != "reachInstallUpdate,reachUpdateState,reachStartupGet,reachStartupSet" {
		t.Fatalf("main bindings: %v", host.binds)
	}
	dh := &fakeHost{}
	d, _ := newNativeBridge(dh, "http://127.0.0.1:4321")
	d.closeSelf = func() {}
	d.bind()
	if strings.Contains(strings.Join(dh.binds, ","), "Update") || strings.Contains(strings.Join(dh.binds, ","), "Startup") {
		t.Fatalf("detached bindings: %v", dh.binds)
	}
}

// ── The next run ───────────────────────────────────────────────────────────

func TestFinishPendingUpdate(t *testing.T) {
	cfgDir := t.TempDir()
	oldDir, oldFile := configDir, configFile
	configDir, configFile = cfgDir, filepath.Join(cfgDir, "config.json")
	t.Cleanup(func() { configDir, configFile = oldDir, oldFile })

	mkDownload := func() string {
		d, err := os.MkdirTemp(os.TempDir(), updateTempPrefix)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		_ = os.WriteFile(filepath.Join(d, updateMSIName), []byte("x"), 0o600)
		return d
	}
	now := time.Now()
	if r := finishPendingUpdate(&Config{}, "1.0.11", now); r != nil {
		t.Fatal("no record, no result")
	}

	// Updated: said, folder removed, record cleared (and saved).
	d := mkDownload()
	cfg := &Config{PendingUpdate: &pendingUpdate{Version: "1.0.12", From: "1.0.11", Dir: d, Stage: filepath.Join(t.TempDir(), "gone"), At: now.Add(-time.Minute)}}
	r := finishPendingUpdate(cfg, "1.0.12", now)
	if r == nil || !r.OK || exists(d) || cfg.PendingUpdate != nil {
		t.Fatalf("updated: %+v, dir kept %v", r, exists(d))
	}
	if saved, _ := os.ReadFile(configFile); strings.Contains(string(saved), "pendingUpdate") {
		t.Fatal("record still saved")
	}

	// Failed: the script's result and its log.
	stage := t.TempDir()
	_ = os.WriteFile(filepath.Join(stage, updateResultFile), []byte("OBR_RC=1603\r\n"), 0o600)
	cfg = &Config{PendingUpdate: &pendingUpdate{Version: "1.0.12", Dir: mkDownload(), Stage: stage, At: now.Add(-time.Minute)}}
	r = finishPendingUpdate(cfg, "1.0.11", now)
	if r == nil || r.OK || r.Code != 1603 || r.Log != filepath.Join(stage, updateLogFile) {
		t.Fatalf("failed: %+v", r)
	}

	// Still installing (the copy is there, a minute ago): nothing said yet.
	_ = os.WriteFile(filepath.Join(stage, updateMSIName), []byte("x"), 0o600)
	d = mkDownload()
	cfg = &Config{PendingUpdate: &pendingUpdate{Version: "1.0.12", Dir: d, Stage: stage, At: now.Add(-time.Minute)}}
	if r := finishPendingUpdate(cfg, "1.0.11", now); r != nil || cfg.PendingUpdate == nil || !exists(d) {
		t.Fatalf("in progress: %+v", r)
	}
	// ... but not forever.
	if r := finishPendingUpdate(cfg, "1.0.11", now.Add(time.Hour)); r == nil || cfg.PendingUpdate != nil || exists(d) {
		t.Fatalf("stale copy: %+v", r)
	}
	// Very old record: cleared without a word.
	cfg = &Config{PendingUpdate: &pendingUpdate{Version: "1.0.12", At: now.Add(-30 * 24 * time.Hour)}}
	if r := finishPendingUpdate(cfg, "1.0.11", now); r != nil || cfg.PendingUpdate != nil {
		t.Fatalf("old record: %+v", r)
	}

	// Only our own download folders are ever removed.
	keep := t.TempDir()
	removeUpdateDir(keep)
	other, _ := os.MkdirTemp(os.TempDir(), "not-ours-")
	defer os.RemoveAll(other)
	removeUpdateDir(other)
	nested := filepath.Join(mkDownload(), updateTempPrefix+"x")
	_ = os.MkdirAll(nested, 0o700)
	removeUpdateDir(nested)
	if !exists(keep) || !exists(other) || !exists(nested) {
		t.Fatal("removeUpdateDir removed a folder that is not a download folder of ours")
	}

	// Left behind by a run closed during a download: swept after a day.
	old, fresh, kept := mkDownload(), mkDownload(), mkDownload()
	past := now.Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(kept, past, past)
	sweepUpdateDirs(kept, now)
	if exists(old) || !exists(fresh) || !exists(kept) {
		t.Fatalf("sweep: old %v fresh %v kept %v", exists(old), exists(fresh), exists(kept))
	}
}

// The private folder: this user, SYSTEM and the administrators only.
func TestMakePrivateDir(t *testing.T) {
	root := t.TempDir()
	d, err := makePrivateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(d) != root || !strings.HasPrefix(filepath.Base(d), updateTempPrefix) || len(filepath.Base(d)) != len(updateTempPrefix)+16 {
		t.Fatalf("folder %s", d)
	}
	out, err := exec.Command("icacls", d).CombinedOutput()
	if err != nil {
		t.Skipf("icacls: %v", err)
	}
	sid, _ := currentUserSID()
	lines := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, ":(") {
			lines++
			if strings.Contains(l, "(I)") {
				t.Fatalf("inherited entry: %s", l)
			}
		}
	}
	if lines != 3 {
		t.Fatalf("want 3 entries (user %s, SYSTEM, Administrators):\n%s", sid, out)
	}
	// Files created inside are the user's (it can write them).
	if err := os.WriteFile(filepath.Join(d, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The main window's close path for the update: every session window of the
// manager is closed its own way, and pending opens are cancelled.
func TestSessionWindowsCloseAll(t *testing.T) {
	_, _, m, _, closed := testMainBridge(t)
	other := &sessionWindows{}
	for h, mgr := range map[uintptr]*sessionWindows{0x9101: m, 0x9102: m, 0x9103: other} {
		sessionWindowByHwnd[h] = &detachedWindow{webHost: webHost{hwnd: h}, mgr: mgr}
	}
	defer func() {
		for _, h := range []uintptr{0x9101, 0x9102, 0x9103} {
			delete(sessionWindowByHwnd, h)
		}
	}()
	m.opening["5"] = true
	gen := m.gen
	if n := m.closeAll(); n != 2 || len(*closed) != 2 || m.gen != gen+1 || len(m.opening) != 0 {
		t.Fatalf("closeAll: %d %v gen %d opening %v", n, *closed, m.gen, m.opening)
	}
	for _, h := range *closed {
		if h == 0x9103 {
			t.Fatal("closed another manager's window")
		}
	}
}
