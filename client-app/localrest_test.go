package main

// localrest_test.go — the rest of the local connections' parity: the
// shell's folder without /proc (macOS, BSD: ps then lsof / pwdx /
// procstat), the listing script's shell loop (no GNU find) run in a real
// shell, a file saved by the app itself in Downloads (sftp_save.go:
// streamed, a free name, cancelled = no partial file, ended with its
// session), a local RDP session moving between windows (rdp_park.go), the
// Info panel's live view parsed (localconn_info.go), and the page wiring
// (favourites / recents of local connections, the RDP tab, Info).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── The shell's folder without /proc ────────────────────────────────────────

func TestParsePsShell(t *testing.T) {
	// macOS: ps -A -o pid= -o ppid= -o tty= -o stat= (this exec's own shell: no tty).
	mac := "P 4100\n  4101  4100 ttys003  Ss+\n  4188  4100 ??       Ss\n  4200  4101 ttys003  S\n   1     0 ??  Ss\n"
	pid, prompt, ok := parsePsShell([]byte(mac))
	if !ok || pid != 4101 || !prompt {
		t.Fatalf("mac at its prompt: %d %v %v", pid, prompt, ok)
	}
	// A command running in it (vim in the foreground: the shell has no '+').
	busy := "P 4100\n  4101  4100 ttys003  Ss\n  4300  4101 ttys003  S+\n"
	if pid, prompt, ok := parsePsShell([]byte(busy)); !ok || pid != 4101 || prompt {
		t.Fatalf("busy: %d %v %v", pid, prompt, ok)
	}
	// FreeBSD: no terminal is "-"; Linux procps: "?".
	bsd := "P 900\n 901 900 pts/1 Is+\n 950 900 - Ss\n"
	if pid, _, ok := parsePsShell([]byte(bsd)); !ok || pid != 901 {
		t.Fatalf("bsd: %d %v", pid, ok)
	}
	// Two terminals on one connection (multiplexed): ambiguous, asked.
	two := "P 900\n 901 900 pts/1 Ss+\n 902 900 pts/2 Ss+\n"
	if _, _, ok := parsePsShell([]byte(two)); ok {
		t.Fatal("two shells: not sure which")
	}
	for _, bad := range []string{"", "P x\n", "P 1\n 2 1 pts/0 S+\n", "nothing\n"} {
		if _, _, ok := parsePsShell([]byte(bad)); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParsePidCwd(t *testing.T) {
	cases := map[string]string{
		// macOS lsof -Fn
		"@lsof\np4101\nfcwd\nn/Users/op/My Projects\n@pwdx\n@procstat\n": "/Users/op/My Projects",
		// Linux / Solaris pwdx
		"@lsof\n@pwdx\n4101: /srv/www\n@procstat\n": "/srv/www",
		// FreeBSD procstat -h -f
		"@lsof\n@pwdx\n@procstat\n 4101 zsh   cwd v d -------- - - - /home/op/src dir\n 4101 zsh  root v d -------- - - - /\n": "/home/op/src dir",
		"@lsof\n@pwdx\n@procstat\n":          "",
		"@lsof\nnrelative\n@pwdx\n4101: x\n": "",
	}
	for in, want := range cases {
		if got := parsePidCwd([]byte(in)); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// scriptedSSHClient answers each exec by the first matching rule.
type scriptedSSHClient struct {
	mu    sync.Mutex
	rules []struct {
		has string
		out string
		err error
	}
	cmds []string
}

func (c *scriptedSSHClient) on(has, out string, err error) *scriptedSSHClient {
	c.rules = append(c.rules, struct {
		has string
		out string
		err error
	}{has, out, err})
	return c
}

func (c *scriptedSSHClient) OpenShell(int, int, func([]byte), func(error)) (localShellConn, error) {
	return nil, errors.New("no shell")
}

func (c *scriptedSSHClient) Exec(cmd string, _ time.Duration) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmds = append(c.cmds, cmd)
	for _, r := range c.rules {
		if strings.Contains(cmd, r.has) {
			return []byte(r.out), r.err
		}
	}
	return nil, errors.New("exit 127")
}

func (c *scriptedSSHClient) Close() error { return nil }

func TestLocalCwdWithoutProc(t *testing.T) {
	reg := newLocalShellRegistry()
	// macOS: the /proc script fails (no /proc), ps + lsof answer.
	c := (&scriptedSSHClient{}).
		on("SSH_CONNECTION", "", errors.New("exit 4")).
		on("ps -A -o pid=", "P 700\n 701 700 ttys001 Ss+\n", nil).
		on("lsof -a -p 701", "@lsof\np701\nfcwd\nn/Users/op/work\n@pwdx\n@procstat\n", nil)
	reg.setLive("tok", c, "vault1")
	got := reg.cwd("tok")
	if got.Path != "/Users/op/work" || !got.Sure {
		t.Fatalf("mac cwd: %+v (cmds %q)", got, c.cmds)
	}
	// Both under /bin/sh, whatever the account's shell (fish, csh...).
	for _, cmd := range c.cmds[1:] {
		if !strings.HasPrefix(cmd, "/bin/sh -c '") {
			t.Errorf("not under /bin/sh: %q", cmd)
		}
	}
	// Linux with /proc: the first script answers, nothing more is asked.
	l := (&scriptedSSHClient{}).on("SSH_CONNECTION", "1\n/var/www\n", nil)
	reg.setLive("tok2", l, "vault2")
	if got := reg.cwd("tok2"); got.Path != "/var/www" || !got.Sure || len(l.cmds) != 1 {
		t.Fatalf("linux cwd: %+v (%d commands)", got, len(l.cmds))
	}
	// Nothing answers (no ps, no lsof): unknown, the drop asks.
	n := (&scriptedSSHClient{})
	reg.setLive("tok3", n, "vault3")
	if got := reg.cwd("tok3"); got.Path != "" {
		t.Fatalf("unknown: %+v", got)
	}
	// Its vault entry finds it (the Info panel).
	if reg.liveOf("vault1") != c || reg.liveOf("nope") != nil {
		t.Fatal("liveOf")
	}
	reg.dropLive("tok", c)
	if reg.liveOf("vault1") != nil {
		t.Fatal("liveOf after its end")
	}
}

// The listing script's loop (busybox, BSD: no GNU find), run in a real
// shell with find hidden: the same records.
func TestScpListScriptLoopInRealShell(t *testing.T) {
	sh, bin, posix := gitShell(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a file.txt"), []byte("12345"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	run := func(script string) []byte {
		c := exec.Command(sh, "-c", "find() { return 1; }\n"+script)
		c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := c.Output()
		if err != nil {
			t.Fatalf("script: %v", err)
		}
		return out
	}
	infos, exists, err := parseScpListing(run(scpListScript(posix(dir), false)))
	if err != nil || !exists || len(infos) != 3 {
		t.Fatalf("loop listing: %v %v %d", err, exists, len(infos))
	}
	by := map[string]os.FileInfo{}
	for _, i := range infos {
		by[i.Name()] = i
	}
	if f := by["a file.txt"]; f == nil || f.Size() != 5 || f.IsDir() || f.ModTime().IsZero() {
		t.Fatalf("file: %+v", f)
	}
	if d := by["sub"]; d == nil || !d.IsDir() {
		t.Fatal("folder")
	}
	if by[".hidden"] == nil {
		t.Fatal("dot file")
	}
	one, exists, _ := parseScpListing(run(scpListScript(posix(dir)+"/a file.txt", true)))
	if !exists || len(one) != 1 || one[0].Size() != 5 {
		t.Fatalf("one: %+v", one)
	}
	if _, exists, _ := parseScpListing(run(scpListScript(posix(dir)+"/none", true))); exists {
		t.Fatal("a missing path exists")
	}
}

// ── A file saved by the app itself (sftp_save.go) ───────────────────────────

// memFS: an explorerFS in memory; a file's reads can be held (gate).
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
	gate  chan struct{} // reads wait on it when set
}

type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() os.FileMode {
	if i.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (i memInfo) ModTime() time.Time { return time.Unix(1700000000, 0) }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() interface{}   { return nil }

func (m *memFS) RealPath(string) (string, error)       { return "/", nil }
func (m *memFS) ReadDir(string) ([]os.FileInfo, error) { return nil, nil }
func (m *memFS) Lstat(p string) (os.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p == "/dir" {
		return memInfo{name: "dir", dir: true}, nil
	}
	b, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return memInfo{name: filepath.Base(p), size: int64(len(b))}, nil
}
func (m *memFS) Mkdir(string) error                    { return nil }
func (m *memFS) Rename(string, string) error           { return nil }
func (m *memFS) Remove(string) error                   { return nil }
func (m *memFS) RemoveDirectory(string) error          { return nil }
func (m *memFS) Create(string) (io.WriteCloser, error) { return nil, errors.New("read only") }

type gatedReader struct {
	r      io.Reader
	gate   chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (g *gatedReader) Read(b []byte) (int, error) {
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-g.closed:
			return 0, io.ErrClosedPipe
		}
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	return g.r.Read(b)
}

func (g *gatedReader) Close() error { g.once.Do(func() { close(g.closed) }); return nil }

func (m *memFS) Open(p string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &gatedReader{r: bytes.NewReader(b), gate: m.gate, closed: make(chan struct{})}, nil
}

func withDownloads(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, oldMark := saveDownloadsDir, saveMarkFromElsewhere
	saveDownloadsDir = func() (string, error) { return dir, nil }
	saveMarkFromElsewhere = func(string) {}
	t.Cleanup(func() { saveDownloadsDir, saveMarkFromElsewhere = old, oldMark })
	return dir
}

func saveRunCmd(t *testing.T, fs explorerFS, st *sftpStreams, typ string, in interface{}) (map[string]interface{}, error) {
	t.Helper()
	b, _ := json.Marshal(in)
	res, handled, err := saveCmd(fs, st, typ, b)
	if !handled {
		t.Fatalf("%s not handled", typ)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	j, _ := json.Marshal(res)
	json.Unmarshal(j, &out)
	return out, nil
}

func saveWait(t *testing.T, fs explorerFS, st *sftpStreams, job string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := saveRunCmd(t, fs, st, "save_status", map[string]string{"job": job})
		if err != nil {
			t.Fatalf("save_status: %v", err)
		}
		if s["state"] != "running" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("still running")
	return nil
}

func TestSaveToDownloadsStreamed(t *testing.T) {
	dl := withDownloads(t)
	data := bytes.Repeat([]byte("stream-me "), 300000) // 3 MB, read 4 KB at a time
	fs := &memFS{files: map[string][]byte{"/srv/report.pdf": data, "/srv/bad:name?.txt": []byte("x")}}
	st := newSftpStreams()
	o, err := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/srv/report.pdf", "name": "report.pdf"})
	if err != nil {
		t.Fatalf("save_open: %v", err)
	}
	if o["size"].(float64) != float64(len(data)) {
		t.Fatalf("size %v", o["size"])
	}
	s := saveWait(t, fs, st, o["job"].(string))
	if s["state"] != "done" || s["name"] != "report.pdf" || s["received"].(float64) != float64(len(data)) {
		t.Fatalf("status: %v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "report.pdf")); !bytes.Equal(b, data) {
		t.Fatalf("saved %d bytes", len(b))
	}
	// Its end told once: the job is gone.
	if _, err := saveRunCmd(t, fs, st, "save_status", map[string]string{"job": o["job"].(string)}); err == nil {
		t.Fatal("job kept after its end was told")
	}
	// Again: never over the first one ("report (2).pdf").
	o2, _ := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/srv/report.pdf"})
	if s := saveWait(t, fs, st, o2["job"].(string)); s["name"] != "report (2).pdf" {
		t.Fatalf("second: %v", s)
	}
	// A name Windows refuses: made safe; a folder or a missing file: refused.
	o3, _ := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/srv/bad:name?.txt"})
	if s := saveWait(t, fs, st, o3["job"].(string)); s["name"] != "bad_name_.txt" {
		t.Fatalf("safe name: %v", s)
	}
	if _, err := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/dir"}); err == nil {
		t.Fatal("a folder saved")
	}
	if _, err := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "relative"}); err == nil {
		t.Fatal("a relative path")
	}
	// No temporary file left behind.
	ents, _ := os.ReadDir(dl)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".oblireach-part") {
			t.Errorf("left: %s", e.Name())
		}
	}
}

func TestSaveToDownloadsCancelled(t *testing.T) {
	dl := withDownloads(t)
	fs := &memFS{files: map[string][]byte{"/big.iso": make([]byte, 1<<20)}, gate: make(chan struct{})}
	st := newSftpStreams()
	o, err := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/big.iso"})
	if err != nil {
		t.Fatal(err)
	}
	fs.gate <- struct{}{} // (a first chunk)
	if _, err := saveRunCmd(t, fs, st, "save_cancel", map[string]string{"job": o["job"].(string)}); err != nil {
		t.Fatal(err)
	}
	if s := saveWait(t, fs, st, o["job"].(string)); s["state"] != "cancelled" {
		t.Fatalf("cancel: %v", s)
	}
	eventually(t, "no partial file", func() bool { ents, _ := os.ReadDir(dl); return len(ents) == 0 })
	// The session closing ends its saves the same way.
	o2, _ := saveRunCmd(t, fs, st, "save_open", map[string]string{"path": "/big.iso"})
	j := st.saveGet(o2["job"].(string))
	st.closeAll(nil)
	eventually(t, "ended with its session", func() bool { s := j.snapshot(); return s["state"] == "cancelled" })
	eventually(t, "no partial file after the session", func() bool { ents, _ := os.ReadDir(dl); return len(ents) == 0 })
}

// ── A local RDP session moving between windows (rdp_park.go) ────────────────

func TestRdpParkRegistry(t *testing.T) {
	r := newRdpParkReg()
	timers := map[int]func(){}
	n := 0
	r.after = func(_ time.Duration, f func()) func() bool {
		n++
		id := n
		timers[id] = f
		return func() bool { delete(timers, id); return true }
	}
	expired := ""
	tok, err := r.park("c1", func(id string) { expired = id })
	if err != nil || !reParkToken.MatchString(tok) {
		t.Fatalf("park: %q %v", tok, err)
	}
	if _, err := r.park("c1", nil); err == nil {
		t.Fatal("parked twice")
	}
	if got := r.take(tok); got != "c1" || r.take(tok) != "" {
		t.Fatal("a token is single use")
	}
	if len(timers) != 0 {
		t.Fatal("its timer kept after the take-over")
	}
	// Never taken over: the session ends.
	tok2, _ := r.park("c2", func(id string) { expired = id })
	for _, f := range timers {
		f()
	}
	if expired != "c2" || r.take(tok2) != "" {
		t.Fatalf("expiry: %q", expired)
	}
	// The session ended while parked: its token goes.
	tok3, _ := r.park("c3", nil)
	r.dropConn("c3")
	if r.take(tok3) != "" || r.count() != 0 {
		t.Fatal("dropConn")
	}
	for i := 0; i < rdpParkMax; i++ {
		if _, err := r.park("x"+string(rune('a'+i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.park("over", nil); !errors.Is(err, errRdpParkFull) {
		t.Fatal("bounded")
	}
}

// ── The Info panel's live view (localconn_info.go) ──────────────────────────

func TestParseLocalInfoLinux(t *testing.T) {
	out := `@uname
Linux 5.15.0-112-generic x86_64
@host
web01
@osrel
PRETTY_NAME="Ubuntu 22.04.4 LTS"
@uptime
356420.51 1410000.20
@boottime
@now
1790000000
@load
0.52 0.40 0.31 2/345 9876
@stat1
cpu  1000 0 1000 8000 0 0 0 0 0 0
cpu0 500 0 500 4000 0 0 0 0 0 0
cpu1 500 0 500 4000 0 0 0 0 0 0
@stat2
cpu  1300 0 1100 8600 0 0 0 0 0 0
cpu0 800 0 500 4200 0 0 0 0 0 0
cpu1 500 0 600 4400 0 0 0 0 0 0
@topmac
@ncpu
@mem
MemTotal:       16384000 kB
MemFree:         1000000 kB
MemAvailable:    8192000 kB
Buffers:          100000 kB
Cached:          4000000 kB
@memsize
@vmstat
@df
Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/sda1         102400000 51200000  51200000      50% /
tmpfs                819200        0    819200       0% /run
/dev/loop3            65536    65536         0     100% /snap/core/1
/dev/sdb1        2048000000 1024000000 1024000000   50% /data with space
@end
`
	in := parseLocalInfo([]byte(out))
	if !in.Open || in.OS != "Ubuntu 22.04.4 LTS" || in.Hostname != "web01" || in.Kernel != "Linux 5.15.0-112-generic x86_64" {
		t.Fatalf("ids: %+v", in)
	}
	if in.UptimeSec != 356420 || in.Load != "0.52 0.40 0.31" {
		t.Fatalf("uptime/load: %d %q", in.UptimeSec, in.Load)
	}
	// overall: busy +400 of total +1000 -> 40 %; cpu0: +300/+500 = 60 %; cpu1: +100/+500 = 20 %.
	if in.CPU == nil || in.CPU.Percent == nil || *in.CPU.Percent != 40 || len(in.CPU.Cores) != 2 || in.CPU.Cores[0] != 60 || in.CPU.Cores[1] != 20 || in.CPU.Count != 2 {
		t.Fatalf("cpu: %+v", in.CPU)
	}
	if in.Memory == nil || in.Memory.Percent != 50 || in.Memory.TotalMb != 16000 || in.Memory.UsedMb != 8000 {
		t.Fatalf("mem: %+v", in.Memory)
	}
	if len(in.Disks) != 2 || in.Disks[0].Mount != "/" || in.Disks[0].Percent != 50 || in.Disks[1].Mount != "/data with space" {
		t.Fatalf("disks: %+v", in.Disks)
	}
}

func TestParseLocalInfoMac(t *testing.T) {
	out := "@uname\nDarwin 23.5.0 arm64\n@host\nmbp\n@osrel\nProductName:\t\tmacOS\nProductVersion:\t\t14.5\nBuildVersion:\t\t23F79\n@uptime\n@boottime\n{ sec = 1789990000, usec = 0 } Mon Sep 28 10:00:00 2026\n@now\n1790000000\n@load\n{ 1.50 1.20 1.00 }\n@stat1\n@stat2\n@topmac\nCPU usage: 7.50% user, 4.50% sys, 88.0% idle \n@ncpu\n10\n@mem\n@memsize\n17179869184\n@vmstat\nMach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               20000.\nPages active:                            400000.\nPages inactive:                          380000.\nPages wired down:                        100000.\nPages occupied by compressor:             24288.\n@df\nFilesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s1s1 971350180 10000000 500000000 2% /\n/dev/disk3s6 971350180 2000000 500000000 1% /System/Volumes/VM\n/dev/disk3s5 971350180 400000000 500000000 45% /System/Volumes/Data\n@end\n"
	in := parseLocalInfo([]byte(out))
	if in.OS != "macOS 14.5" || in.UptimeSec != 10000 || in.Load != "1.50 1.20 1.00" {
		t.Fatalf("mac ids: %+v", in)
	}
	if in.CPU == nil || in.CPU.Percent == nil || *in.CPU.Percent != 12 || in.CPU.Count != 10 {
		t.Fatalf("mac cpu: %+v", in.CPU)
	}
	if in.Memory == nil || in.Memory.TotalMb != 16384 || in.Memory.UsedMb != 8192 || in.Memory.Percent != 50 {
		t.Fatalf("mac mem: %+v", in.Memory)
	}
	if len(in.Disks) != 2 || in.Disks[1].Mount != "/System/Volumes/Data" {
		t.Fatalf("mac disks: %+v", in.Disks)
	}
}

func TestLocalInfoOnlyWhenOpen(t *testing.T) {
	if in := localInfoOf("v1", func(string) sshClientLike { return nil }); in.Open || in.CPU != nil {
		t.Fatalf("nothing open: %+v", in)
	}
	c := (&scriptedSSHClient{}).on("@uname", "@uname\nLinux 6.1 x86_64\n@end\n", nil)
	in := localInfoOf("v1", func(id string) sshClientLike {
		if id == "v1" {
			return c
		}
		return nil
	})
	if !in.Open || in.Kernel != "Linux 6.1 x86_64" {
		t.Fatalf("open: %+v", in)
	}
	if len(c.cmds) != 1 || !strings.HasPrefix(c.cmds[0], "/bin/sh -c '") {
		t.Fatalf("asked: %q", c.cmds)
	}
	// A control character of the server's never reaches the page.
	if s := localInfoText("a\x1b[31mb\x07", 64); s != "a[31mb" {
		t.Fatalf("text: %q", s)
	}
}

// ── The page's wiring ───────────────────────────────────────────────────────

func TestLocalRestPageWiring(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	ui, lc, rv, tv, ex, tools := read("ui.go"), read("localconn_tools.go"), read("rdp_view.go"), read("terminal_viewer.go"), read("explorer_viewer.go"), read("agent_tools.go")
	for _, c := range []struct {
		file, what string
		ok         bool
	}{
		// 4. favourites and recents of local connections (the app's own: LOCAL_PID's key)
		{"ui.go", "a star on a local row", strings.Contains(ui, "const isFav = favHas(dev); // (the app's own favourites: LOCAL_PID's)")},
		{"ui.go", "recents of local connections", !strings.Contains(ui, "if (devIsLocal(device)) return; // (a local connection is no server's device)")},
		{"ui.go", "the Dashboard lists them", strings.Contains(ui, "const dashDevs = allDevs.concat(localDevs);") && strings.Contains(ui, "ofPid(p.local ? LOCAL_PID : p.id)")},
		{"ui.go", "a local base has its Dashboard (and its Vault)", strings.Contains(ui, "const NAV_LOCAL_ROUTES = ['dashboard', 'devices', 'support', 'help', 'files', 'vault', 'settings'];")},
		// 5. RDP is a tab
		{"agent_tools.go", "rdp a tool kind", strings.Contains(tools, "var TOOL_KINDS = ['cmd', 'powershell', 'ssh', 'files', 'rdp'];")},
		{"rdp_view.go", "TOOL_IMPL.rdp", strings.Contains(rv, "TOOL_IMPL.rdp = rdpTabImpl;") && !strings.Contains(rv, "function cxnOpenRdpView(")},
		{"localconn_tools.go", "RDP opens as a tab", strings.Contains(lc, "if (n.kind === CXN_KIND_RDP) return toolOpen(cxnDevice(n), 'rdp', where || cxnWhere());")},
		{"terminal_viewer.go", "an RDP tab moves parked", strings.Contains(tv, "park = await rdpTabPark(tab);") && strings.Contains(tv, "tab.rdpPark = o.park;")},
		// 3. no Blob for a local connection's download
		{"explorer_viewer.go", "saved by the app", strings.Contains(ex, "saved = await explorerStreamSave(tab, it, t);") && !strings.Contains(ex, "explorerStreamDown(")},
		// 1. sizes for SCP
		{"explorer_viewer.go", "upload_open carries its size", strings.Contains(ex, "overwrite: !!overwrite, size: Number(f.size) || 0 }")},
		// 6. Info
		{"ui.go", "Info of a local connection", strings.Contains(ui, "if (devIsLocal(d)) { infoLiveClose(); cxnRenderInfo(tc, d); return; }")},
		{"localconn_tools.go", "its Info button", strings.Contains(lc, "ib.addEventListener('click', function () { sidePanelToggle('info'); });")},
	} {
		if !c.ok {
			t.Errorf("%s: %s", c.file, c.what)
		}
	}
	// The secrets never in the page: no reveal of the vault from these files.
	for _, f := range []string{"localconn_info.go", "rdp_view.go", "localconn_tools.go", "sftp_save.go", "scp.go"} {
		if s := read(f); strings.Contains(s, "reachVaultReveal") || strings.Contains(s, "v.Reveal(") {
			t.Errorf("%s reveals a secret", f)
		}
	}
	// The Go side accepts the new tool kind and its park token.
	if !toolKinds["rdp"] || !toolParkKind("rdp") || toolParkKind("files") || !toolParkKind("ssh") {
		t.Fatal("tool kinds")
	}
}
