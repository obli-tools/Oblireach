package main

// localconn_explorer_test.go: the explorer's commands on a local SSH
// connection (localconn_explorer.go), against the REAL in-process SFTP
// server of sftp_session_test.go, with the agent's result shapes the
// explorer reads (explorer_viewer.go explorerItems, explorerDownload,
// explorerUpload's "already exists"); and the routing of a local connection
// in the page (localconn_tools.go: never a server).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path"
	"strings"
	"testing"
)

func explorerRun(t *testing.T, fs explorerFS, typ string, payload interface{}) (map[string]interface{}, error) {
	t.Helper()
	b, _ := json.Marshal(payload)
	res, err := localExplorerCmd(fs, typ, b)
	if err != nil {
		return nil, err
	}
	j, _ := json.Marshal(res)
	var out map[string]interface{}
	_ = json.Unmarshal(j, &out)
	return out, nil
}

// The SFTP server of the tests answers Windows paths as "/C:/..." (pkg/sftp
// on Windows): the explorer's paths are what its listing returned.
func explorerHome(t *testing.T, fs explorerFS) string {
	t.Helper()
	out, err := explorerRun(t, fs, "list_directory", map[string]string{"path": ""})
	if err != nil {
		t.Fatalf("list home: %v", err)
	}
	p, _ := out["path"].(string)
	if !strings.HasPrefix(p, "/") {
		t.Fatalf("home path %q: not absolute", p)
	}
	return p
}

func TestLocalExplorerCmdRoundTrip(t *testing.T) {
	srv := startTestSFTPServer(t, "u", "p")
	sess := withEd25519HostKeyDial(t, srv, "u", "p")
	fs := sftpExplorerFS{sess}
	home := explorerHome(t, fs)

	dir := explorerJoinPath(home, "work")
	if _, err := explorerRun(t, fs, "create_directory", map[string]string{"path": dir}); err != nil {
		t.Fatalf("create_directory: %v", err)
	}
	if _, err := explorerRun(t, fs, "create_directory", map[string]string{"path": dir}); !errors.Is(err, errExplorerExists) {
		t.Fatalf("create_directory twice: %v", err)
	}
	file := explorerJoinPath(dir, "a.txt")
	data := base64.StdEncoding.EncodeToString([]byte("hello sftp"))
	if _, err := explorerRun(t, fs, "upload_file", map[string]interface{}{"path": file, "data": data}); err != nil {
		t.Fatalf("upload_file: %v", err)
	}
	// Already there without overwrite: the explorer's "already exists" (it then asks).
	_, err := explorerRun(t, fs, "upload_file", map[string]interface{}{"path": file, "data": data})
	if err == nil || !strings.Contains(strings.ToLower(explorerCmdErrText(err)), "already exists") {
		t.Fatalf("upload_file again: %v", err)
	}
	data2 := base64.StdEncoding.EncodeToString([]byte("replaced"))
	if _, err := explorerRun(t, fs, "upload_file", map[string]interface{}{"path": file, "data": data2, "overwrite": true}); err != nil {
		t.Fatalf("upload_file overwrite: %v", err)
	}
	out, err := explorerRun(t, fs, "download_file", map[string]string{"path": file})
	if err != nil {
		t.Fatalf("download_file: %v", err)
	}
	if got, _ := base64.StdEncoding.DecodeString(out["data"].(string)); string(got) != "replaced" {
		t.Fatalf("download_file data %q", got)
	}
	// The listing: the agent's shape.
	out, err = explorerRun(t, fs, "list_directory", map[string]string{"path": dir})
	if err != nil {
		t.Fatalf("list_directory: %v", err)
	}
	files, _ := out["files"].([]interface{})
	if out["path"] != dir || len(files) != 1 {
		t.Fatalf("listing %v", out)
	}
	f0 := files[0].(map[string]interface{})
	if f0["name"] != "a.txt" || f0["path"] != file || f0["isDir"] != false || f0["size"].(float64) != 8 ||
		f0["modified"] == "" || !strings.HasPrefix(f0["mode"].(string), "-") {
		t.Fatalf("entry %v", f0)
	}
	renamed := explorerJoinPath(dir, "b.txt")
	if _, err := explorerRun(t, fs, "rename_file", map[string]string{"oldPath": file, "newPath": renamed}); err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	// A folder with contents: only recursive.
	if _, err := explorerRun(t, fs, "delete_file", map[string]interface{}{"path": dir, "recursive": false}); !errors.Is(err, errExplorerNotEmpty) {
		t.Fatalf("delete non-empty folder: %v", err)
	}
	if _, err := explorerRun(t, fs, "delete_file", map[string]interface{}{"path": dir, "recursive": true}); err != nil {
		t.Fatalf("delete recursive: %v", err)
	}
	out, _ = explorerRun(t, fs, "list_directory", map[string]string{"path": home})
	for _, x := range out["files"].([]interface{}) {
		if x.(map[string]interface{})["name"] == "work" {
			t.Fatal("folder still there")
		}
	}
	// Through the bridge's wrapper: {ok, result} / {ok:false, error}.
	r := sftpRunExplorerCmd(sess, "list_directory", []byte(`{"path":""}`))
	if r["ok"] != true {
		t.Fatalf("wrapper ok: %v", r)
	}
	r = sftpRunExplorerCmd(sess, "nope", nil)
	if r["ok"] != false || r["error"] != "unknown command" {
		t.Fatalf("wrapper unknown: %v", r)
	}
	sess.Close()
	if r = sftpRunExplorerCmd(sess, "list_directory", nil); r["gone"] != true {
		t.Fatalf("wrapper closed: %v", r)
	}
}

// ── A fake for the refusals ─────────────────────────────────────────────────

type fakeExplorerFS struct{ explorerFS }

func (fakeExplorerFS) RealPath(string) (string, error)       { return "/home/u", nil }
func (fakeExplorerFS) ReadDir(string) ([]os.FileInfo, error) { return nil, nil }
func (fakeExplorerFS) Lstat(p string) (os.FileInfo, error)   { return nil, os.ErrNotExist }
func (fakeExplorerFS) Remove(string) error                   { return os.ErrPermission }
func (fakeExplorerFS) RemoveDirectory(string) error          { return nil }

func TestLocalExplorerCmdRefusals(t *testing.T) {
	fs := fakeExplorerFS{}
	for _, c := range []struct {
		typ     string
		payload string
		want    error
	}{
		{"list_directory", `{"path":"relative/dir"}`, errExplorerBadPath},
		{"list_directory", `{"path":"/a\u0000b"}`, errExplorerBadPath},
		{"create_directory", `{"path":""}`, errExplorerBadPath},
		{"create_directory", `{"path":"/"}`, errExplorerBadPath},
		{"delete_file", `{"path":"/","recursive":true}`, errExplorerBadPath},
		{"rename_file", `{"oldPath":"/a","newPath":"b"}`, errExplorerBadPath},
		{"upload_file", `{"path":"/x","data":"not base64!"}`, errExplorerBadPath},
		{"format_disk", `{}`, errExplorerBadCmd},
		{"list_directory", `not json`, errExplorerBadPath},
	} {
		if _, err := localExplorerCmd(fs, c.typ, []byte(c.payload)); !errors.Is(err, c.want) {
			t.Errorf("%s %s: %v, want %v", c.typ, c.payload, err, c.want)
		}
	}
	// "" lists the home (the server's "."), its path said back.
	out, err := explorerRun(t, fs, "list_directory", map[string]string{"path": ""})
	if err != nil || out["path"] != "/home/u" {
		t.Fatalf("home listing: %v %v", out, err)
	}
	// The server's failures in words the explorer quotes, bounded.
	_, err = localExplorerCmd(fs, "delete_file", []byte(`{"path":"/f"}`))
	if err == nil {
		t.Fatal("delete of a missing file")
	}
	if explorerCmdErrText(err) != "not found" {
		t.Fatalf("not found text: %q", explorerCmdErrText(err))
	}
	if explorerCmdErrText(os.ErrPermission) != "permission denied" || len(explorerCmdErrText(errors.New(strings.Repeat("x", 900)))) != 300 {
		t.Fatal("error text")
	}
	if explorerJoinPath("/", "a") != "/a" || explorerJoinPath("/x/", "a") != "/x/a" || path.Clean("/x/../y") != "/y" {
		t.Fatal("join")
	}
}

// The page routes a local connection to the app, never to a server
// (localconn_tools.go): each transport choke point hands a local tab over
// before anything server-side, and devApi refuses one outright.
func TestLocalConnRouting(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	// "body" must appear in "fn" before "after" (the server path).
	before := func(src, fn, guard, after string) {
		t.Helper()
		i := strings.Index(src, fn)
		if i < 0 {
			t.Fatalf("%s: missing", fn)
		}
		rest := src[i:]
		g, a := strings.Index(rest, guard), strings.Index(rest, after)
		if g < 0 || (a >= 0 && a < g) {
			t.Fatalf("%s: %q must come before %q", fn, guard, after)
		}
	}
	term, exp, ui, tools := read("terminal_viewer.go"), read("explorer_viewer.go"), read("ui.go"), read("agent_tools.go")
	// The terminal: ONE code path, its transport chosen by the device (the
	// agent's: its server's session; a local connection's: the app's
	// relay, ssh_relay.go), never a server for a local one.
	before(term, "function termTransport(tab) {", "devIsLocal(tab.device) ? 'local' : 'agent'", "TERM_TRANSPORTS.agent")
	before(term, "async function termStart(tab, o) {", "termTransport(tab).open(", "new WebSocket(")
	if i := strings.Index(term, "async function termStart(tab, o) {"); i < 0 || strings.Contains(term[i:strings.Index(term[i:], "\n}\n")+i], "devApi(") {
		t.Fatal("termStart calls a server itself (only the agent transport does)")
	}
	lcAll := read("localconn_tools.go")
	li := strings.Index(lcAll, "TERM_TRANSPORTS.local = {")
	if li < 0 {
		t.Fatal("no local terminal transport")
	}
	lt := lcAll[li : li+strings.Index(lcAll[li:], "\n};\n")]
	if !strings.Contains(lt, "'reachSshConnect'") || !strings.Contains(lt, "'/local/ssh-tunnel/' + token") || strings.Contains(lt, "devApi(") {
		t.Fatalf("local transport: %s", lt)
	}
	// Moving between windows: the same for both (no local refusal any more).
	for _, gone := range []string{"cxnSshSendBytes", "cxnSshResize", "cxnSshStop", "termStartLocal", "__reachSshEvent", "a local connection stays in the main window"} {
		if strings.Contains(term, gone) || strings.Contains(lcAll, gone) {
			t.Errorf("%s: still there", gone)
		}
	}
	before(exp, "async function explorerSend(tab, type, payload, ms) {", "if (devIsLocal(dev)) return cxnSftpSend(", "explorerSocket(")
	before(exp, "async function explorerCmd(tab, type, payload, ms, batch, opts) {", "if (devIsLocal(tab.device)) return explorerSend(", "explorerStepUp(")
	before(exp, "async function explorerOpen(tab) {", "!devIsLocal(tab.device)", "open-explorer")
	before(exp, "async function explorerCtxOpen(ctx) {", "devIsLocal(ctx.device)) return;", "open-explorer")
	before(ui, "async function devApi(dev, method, path, body, pinned) {", "if (devIsLocal(dev)) throw", "devBase(")
	// (a local connection's recents: the app's own list, LOCAL_PID's key, never a server's)
	before(ui, "function addRecent(device) {", "const pid = devPid(device);", "serverStoreKeyFor(pid, 'oblireach_recents')")
	before(ui, "function selectDeviceInto(dev, area) {", "cxnSelectInto(dev, area)", "toolsHeaderGroup(")
	before(ui, "function sidePanelSync() {", "devIsLocal(selectedDevice)", "sidePanelRender(")
	before(tools, "async function privacyEnsure(dev, feature, opts) {", "if (devIsLocal(dev)) return true;", "privacyUnlocksRefresh(")
	before(tools, "async function privacyRetryGate(dev, feature) {", "if (devIsLocal(dev)) return dev;", "loadOverview(")
	before(tools, "function toolState(d, kind) {", "if (devIsLocal(d))", "devAgentReachable(")
	// The synthetic identity can never be a server profile's (16 hex characters).
	lc := read("localconn_tools.go")
	if !strings.Contains(lc, "var LOCAL_PID = '__local__';") || !strings.Contains(lc, "dev._local === true && dev._pid === LOCAL_PID") {
		t.Fatal("local identity")
	}
	if p := newProfileID(); p == "__local__" || len(p) != 16 {
		t.Fatalf("profile id %q", p)
	}
	// The old in-page views are gone: one explorer, one terminal.
	for _, f := range []string{"ssh_terminal.go", "sftp_page.go", "sftp_dialog_windows.go"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s still there", f)
		}
	}
}
