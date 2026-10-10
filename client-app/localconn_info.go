package main

// localconn_info.go — the Info panel of a LOCAL SSH connection (the vault's
// ssh entries, localconn_tools.go): what the connection is (from the vault,
// never its secret) and, while it is open (its terminal or its files, in any
// window), a light live view asked of the server over that same connection
// (an exec channel, no PTY: never seen in a terminal; nothing is installed
// there): its OS (uname, os-release / sw_vers), uptime and load, CPU overall
// and per core (/proc/stat twice, a second apart; macOS: top), memory
// (/proc/meminfo; macOS: hw.memsize + vm_stat), disks (df -kP). Nothing of it
// is stored. No connection open: nothing is asked (the panel says so).
//
// The page asks reachLocalInfo(vaultId) every few seconds while the panel
// shows the connection (localConnInfoJS below draws it with the remote Info
// panel's own gauges: agent_tools.go infoLiveBar).
//
// Its consts follow localconn_tools.go's in the page (appended to them: the
// same injection by ui.go's buildUI, not through Sprintf: '%' is safe): no
// backtick in them.

import (
	"bufio"
	"math"
	"strconv"
	"strings"
	"time"
)

const localInfoMaxDisks = 8

// localInfoScript runs under /bin/sh whatever the account's shell.
const localInfoScript = `echo @uname; uname -srm 2>/dev/null
echo @host; hostname 2>/dev/null || uname -n 2>/dev/null
echo @osrel; grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null; sw_vers 2>/dev/null
echo @uptime; cat /proc/uptime 2>/dev/null
echo @boottime; sysctl -n kern.boottime 2>/dev/null
echo @now; date +%s
echo @load; cat /proc/loadavg 2>/dev/null || sysctl -n vm.loadavg 2>/dev/null
echo @stat1; grep '^cpu' /proc/stat 2>/dev/null
[ -r /proc/stat ] && sleep 1
echo @stat2; grep '^cpu' /proc/stat 2>/dev/null
echo @topmac; [ -r /proc/stat ] || { top -l 2 -n 0 -s 1 2>/dev/null | grep 'CPU usage' | tail -n 1; }
echo @ncpu; sysctl -n hw.ncpu 2>/dev/null
echo @mem; grep -E '^(MemTotal|MemAvailable|MemFree|Buffers|Cached):' /proc/meminfo 2>/dev/null
echo @memsize; sysctl -n hw.memsize 2>/dev/null
echo @vmstat; [ -r /proc/meminfo ] || vm_stat 2>/dev/null
echo @df; df -kP 2>/dev/null
echo @end`

type localInfoCPU struct {
	Percent *float64  `json:"percent,omitempty"`
	Cores   []float64 `json:"cores,omitempty"`
	Count   int       `json:"count,omitempty"`
}

type localInfoMem struct {
	Percent float64 `json:"percent"`
	TotalMb float64 `json:"totalMb"`
	UsedMb  float64 `json:"usedMb"`
}

type localInfoDisk struct {
	Mount   string  `json:"mount"`
	Percent float64 `json:"percent"`
	TotalGb float64 `json:"totalGb"`
	UsedGb  float64 `json:"usedGb"`
}

// localInfo: reachLocalInfo's answer (Open false: no connection open).
type localInfo struct {
	Open      bool            `json:"open"`
	Hostname  string          `json:"hostname,omitempty"`
	OS        string          `json:"os,omitempty"`     // "Ubuntu 22.04.4 LTS", "macOS 14.5"
	Kernel    string          `json:"kernel,omitempty"` // uname -srm
	UptimeSec int64           `json:"uptimeSec,omitempty"`
	Load      string          `json:"load,omitempty"`
	CPU       *localInfoCPU   `json:"cpu,omitempty"`
	Memory    *localInfoMem   `json:"memory,omitempty"`
	Disks     []localInfoDisk `json:"disks,omitempty"`
}

// localInfoText: a bounded line of the server's (shown as text by the page).
func localInfoText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// cpuTimes: one "cpu..." line of /proc/stat: busy and total jiffies.
func cpuTimes(fields []string) (busy, total float64, ok bool) {
	if len(fields) < 5 {
		return 0, 0, false
	}
	var v []float64
	for _, f := range fields[1:] {
		n, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return 0, 0, false
		}
		v = append(v, n)
	}
	// user nice system idle iowait irq softirq steal (guest: inside user)
	if len(v) > 8 {
		v = v[:8]
	}
	for _, n := range v {
		total += n
	}
	idle := v[3]
	if len(v) > 4 {
		idle += v[4]
	}
	return total - idle, total, true
}

func cpuPct(b1, t1, b2, t2 float64) (float64, bool) {
	dt := t2 - t1
	if dt <= 0 {
		return 0, false
	}
	p := (b2 - b1) / dt * 100
	return math.Max(0, math.Min(100, math.Round(p*10)/10)), true
}

// parseLocalInfo reads localInfoScript's output.
func parseLocalInfo(out []byte) localInfo {
	info := localInfo{Open: true}
	sec := map[string][]string{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(l, "@") && !strings.Contains(l, " ") {
			cur = l[1:]
			continue
		}
		if cur != "" && len(sec[cur]) < 512 {
			sec[cur] = append(sec[cur], l)
		}
	}
	first := func(k string) string {
		for _, l := range sec[k] {
			if strings.TrimSpace(l) != "" {
				return l
			}
		}
		return ""
	}
	info.Kernel = localInfoText(first("uname"), 160)
	info.Hostname = localInfoText(first("host"), 128)
	// OS: os-release's PRETTY_NAME, or macOS' sw_vers.
	var prod, ver string
	for _, l := range sec["osrel"] {
		switch {
		case strings.HasPrefix(l, "PRETTY_NAME="):
			info.OS = localInfoText(strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), `"'`), 128)
		case strings.HasPrefix(l, "ProductName:"):
			prod = strings.TrimSpace(strings.TrimPrefix(l, "ProductName:"))
		case strings.HasPrefix(l, "ProductVersion:"):
			ver = strings.TrimSpace(strings.TrimPrefix(l, "ProductVersion:"))
		}
	}
	if info.OS == "" && prod != "" {
		info.OS = localInfoText(prod+" "+ver, 128)
	}
	// Uptime: /proc/uptime, or now - kern.boottime ("{ sec = 1700000000, usec = 0 } ...").
	if f := strings.Fields(first("uptime")); len(f) > 0 {
		if s, err := strconv.ParseFloat(f[0], 64); err == nil && s > 0 {
			info.UptimeSec = int64(s)
		}
	}
	if info.UptimeSec == 0 {
		bt := first("boottime")
		if i := strings.Index(bt, "sec = "); i >= 0 {
			rest := bt[i+6:]
			if j := strings.IndexAny(rest, ", }"); j > 0 {
				rest = rest[:j]
			}
			boot, e1 := strconv.ParseInt(rest, 10, 64)
			now, e2 := strconv.ParseInt(strings.TrimSpace(first("now")), 10, 64)
			if e1 == nil && e2 == nil && now > boot && boot > 0 {
				info.UptimeSec = now - boot
			}
		}
	}
	// Load: "0.10 0.20 0.30 ..." or macOS' "{ 1.23 1.10 1.00 }".
	if f := strings.Fields(strings.Trim(first("load"), "{} ")); len(f) >= 3 {
		info.Load = localInfoText(f[0]+" "+f[1]+" "+f[2], 64)
	}
	// CPU: /proc/stat a second apart (overall and per core).
	parseStat := func(k string) map[string][2]float64 {
		m := map[string][2]float64{}
		for _, l := range sec[k] {
			f := strings.Fields(l)
			if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
				continue
			}
			if b, t, ok := cpuTimes(f); ok {
				m[f[0]] = [2]float64{b, t}
			}
		}
		return m
	}
	s1, s2 := parseStat("stat1"), parseStat("stat2")
	if len(s1) > 0 && len(s2) > 0 {
		cpu := &localInfoCPU{}
		if a, ok1 := s1["cpu"]; ok1 {
			if b, ok2 := s2["cpu"]; ok2 {
				if p, ok := cpuPct(a[0], a[1], b[0], b[1]); ok {
					cpu.Percent = &p
				}
			}
		}
		for i := 0; i < 1024; i++ {
			k := "cpu" + strconv.Itoa(i)
			a, ok1 := s1[k]
			b, ok2 := s2[k]
			if !ok1 || !ok2 {
				break
			}
			p, ok := cpuPct(a[0], a[1], b[0], b[1])
			if !ok {
				p = 0
			}
			cpu.Cores = append(cpu.Cores, p)
		}
		cpu.Count = len(cpu.Cores)
		if cpu.Percent != nil || cpu.Count > 0 {
			info.CPU = cpu
		}
	} else if l := first("topmac"); l != "" {
		// "CPU usage: 5.12% user, 3.4% sys, 91.43% idle"
		if i := strings.Index(l, "% idle"); i > 0 {
			j := strings.LastIndexAny(l[:i], " ,:")
			if idle, err := strconv.ParseFloat(strings.TrimSpace(l[j+1:i]), 64); err == nil && idle >= 0 && idle <= 100 {
				p := math.Round((100-idle)*10) / 10
				info.CPU = &localInfoCPU{Percent: &p}
				if n, err := strconv.Atoi(strings.TrimSpace(first("ncpu"))); err == nil && n > 0 && n < 4096 {
					info.CPU.Count = n
				}
			}
		}
	}
	// Memory: /proc/meminfo (kB), or macOS' hw.memsize + vm_stat pages.
	mi := map[string]float64{}
	for _, l := range sec["mem"] {
		f := strings.Fields(l)
		if len(f) >= 2 {
			if n, err := strconv.ParseFloat(f[1], 64); err == nil {
				mi[strings.TrimSuffix(f[0], ":")] = n
			}
		}
	}
	if tot := mi["MemTotal"]; tot > 0 {
		avail, ok := mi["MemAvailable"]
		if !ok {
			avail = mi["MemFree"] + mi["Buffers"] + mi["Cached"]
		}
		used := math.Max(0, tot-avail)
		info.Memory = &localInfoMem{Percent: math.Round(used/tot*1000) / 10, TotalMb: math.Round(tot / 1024), UsedMb: math.Round(used / 1024)}
	} else if tb, err := strconv.ParseFloat(strings.TrimSpace(first("memsize")), 64); err == nil && tb > 0 {
		page := 4096.0
		pages := map[string]float64{}
		for _, l := range sec["vmstat"] {
			if i := strings.Index(l, "page size of "); i >= 0 {
				if f := strings.Fields(l[i+13:]); len(f) > 0 {
					if n, err := strconv.ParseFloat(f[0], 64); err == nil && n > 0 {
						page = n
					}
				}
				continue
			}
			if i := strings.LastIndexByte(l, ':'); i > 0 {
				if n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(l[i+1:]), "."), 64); err == nil {
					pages[strings.TrimSpace(l[:i])] = n
				}
			}
		}
		used := (pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page
		if used > 0 && used <= tb {
			info.Memory = &localInfoMem{Percent: math.Round(used/tb*1000) / 10, TotalMb: math.Round(tb / 1048576), UsedMb: math.Round(used / 1048576)}
		}
	}
	// Disks: df -kP, real file systems (a device path, or /).
	for _, l := range sec["df"] {
		f := strings.Fields(l)
		if len(f) < 6 || f[0] == "Filesystem" {
			continue
		}
		mount := strings.Join(f[5:], " ")
		if !strings.HasPrefix(f[0], "/") && mount != "/" {
			continue
		}
		if strings.HasPrefix(mount, "/System/Volumes/") && mount != "/System/Volumes/Data" {
			continue // (macOS' own volumes)
		}
		if strings.HasPrefix(mount, "/snap/") || strings.HasPrefix(f[0], "/dev/loop") {
			continue
		}
		blocks, e1 := strconv.ParseFloat(f[1], 64)
		used, e2 := strconv.ParseFloat(f[2], 64)
		avail, e3 := strconv.ParseFloat(f[3], 64)
		if e1 != nil || e2 != nil || e3 != nil || blocks <= 0 || used+avail <= 0 {
			continue
		}
		info.Disks = append(info.Disks, localInfoDisk{
			Mount: localInfoText(mount, 64), Percent: math.Round(used/(used+avail)*1000) / 10,
			TotalGb: math.Round(blocks/1048576*10) / 10, UsedGb: math.Round(used/1048576*10) / 10,
		})
		if len(info.Disks) >= localInfoMaxDisks {
			break
		}
	}
	return info
}

// ── An open connection of a vault entry ─────────────────────────────────────

// localInfoClient: a live connection of vault entry id (its terminal's, else
// its explorer's), or nil.
func localInfoClient(id string) sshClientLike {
	if c := appLocalShells.liveOf(id); c != nil {
		return c
	}
	return appSFTPRegistry.clientOf(id)
}

func (r *localShellRegistry) liveOf(label string) sshClientLike {
	r.mu.Lock()
	defer r.mu.Unlock()
	for tok, c := range r.live {
		if r.labels[tok] == label {
			return c
		}
	}
	return nil
}

func (r *sftpSessionRegistry) clientOf(vaultID string) sshClientLike {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sess {
		if s.vaultID == vaultID && s.client != nil && !s.isClosed() {
			return realSSHClient{c: s.client}
		}
	}
	return nil
}

// localInfoOf: the live view of vault entry id ({open: false}: none open).
func localInfoOf(id string, find func(string) sshClientLike) localInfo {
	c := find(id)
	if c == nil {
		return localInfo{}
	}
	out, err := c.Exec("/bin/sh -c "+shQuote(localInfoScript), 15*time.Second)
	if err != nil && len(out) == 0 {
		return localInfo{Open: true}
	}
	return parseLocalInfo(out)
}

const localConnInfoJS = `
// ── A local SSH connection's Info panel (localconn_info.go) ─────────────────
// What it is (the vault's description, never its secret), then while it is
// open (its terminal or its files, any window) a live view asked over it
// every few seconds, drawn with the remote panel's gauges (agent_tools.go
// infoLiveBar). Per core: a compact grid of its own (cxnInfoCores), to be
// replaced by a shared per-core renderer once the remote panel has one.
var CXN_INFO_POLL_MS = 5000, CXN_INFO_HIST = 24;
var cxnInfo = null; // { key, dev, el, timer, busy, data, at, cpu: [], ram: [] }
function cxnInfoClose() {
  var st = cxnInfo;
  if (!st) return;
  cxnInfo = null;
  clearInterval(st.timer);
}
function cxnInfoRow(key, val) { return '<div><span style="color:var(--muted2)">' + TH('info.rowLabel', { label: T(key) }) + ' </span>' + val + '</div>'; }
function cxnRenderInfo(tc, dev) {
  var n = (dev && dev._node) || {};
  var h = '<div style="padding:16px;display:flex;flex-direction:column;gap:10px;font-size:13px">';
  h += cxnInfoRow('info.hostname', esc(dev.hostname || ''));
  h += cxnInfoRow('connections.info.type', esc(cxnTypeName(dev)));
  h += cxnInfoRow('connections.field.host', esc(String(n.host || '')));
  h += cxnInfoRow('connections.field.port', esc(String(n.port || '')));
  if (n.username) h += cxnInfoRow('connections.field.username', esc(String(n.username)));
  h += cxnInfoRow('connections.field.authMethod', TH(n.authMethod === CXN_AUTH_KEY ? 'connections.auth.key' : 'connections.auth.password'));
  h += cxnInfoRow('info.status', TH(devStateChip(dev).key));
  h += '</div>';
  tc.innerHTML = h;
  if (!cxnIsSsh(dev)) { cxnInfoClose(); return; }
  var el = document.createElement('div'); el.className = 'info-live'; el.id = 'cxn-info-live';
  // (the shared per-thread view's fold, as on the remote panel: kept on this computer)
  el.addEventListener('click', function (e) {
    var b = e && e.target && e.target.closest ? e.target.closest('[data-il-threads]') : null;
    if (!b || !cxnInfoSharedThreads()) return;
    infoThreadsOpen = !infoThreadsOpen;
    try { if (typeof INFO_THREADS_PREF === 'string') localStorage.setItem(INFO_THREADS_PREF, infoThreadsOpen ? '1' : '0'); } catch (x) {}
    cxnInfoDraw();
  });
  tc.appendChild(el);
  var key = devKey(dev);
  if (cxnInfo && cxnInfo.key === key) { cxnInfo.el = el; cxnInfo.dev = dev; cxnInfoDraw(); return; }
  cxnInfoClose();
  var st = cxnInfo = { key: key, dev: dev, el: el, timer: null, busy: false, data: null, at: 0, cpu: [], ram: [] };
  cxnInfoDraw();
  cxnInfoPoll(st);
  st.timer = setInterval(function () { cxnInfoPoll(st); }, CXN_INFO_POLL_MS);
}
async function cxnInfoPoll(st) {
  if (cxnInfo !== st || st.busy) return;
  if (st.el && st.el.isConnected === false) { cxnInfoClose(); return; }
  st.busy = true;
  var d = null;
  try { d = await vaultAsyncCallWithin(20000, 'reachLocalInfo', [String(st.dev._node.id)]); } catch (e) { d = { failed: true }; }
  st.busy = false;
  if (cxnInfo !== st) return;
  st.data = (d && typeof d === 'object') ? d : { failed: true };
  st.at = Date.now();
  var c = st.data.cpu && infoLivePct(st.data.cpu.percent), r = st.data.memory && infoLivePct(st.data.memory.percent);
  if (st.data.open && c !== null && c !== undefined) { st.cpu.push(c); if (st.cpu.length > CXN_INFO_HIST) st.cpu.shift(); }
  if (st.data.open && r !== null && r !== undefined) { st.ram.push(r); if (st.ram.length > CXN_INFO_HIST) st.ram.shift(); }
  if (!st.data.open) { st.cpu = []; st.ram = []; }
  cxnInfoDraw();
}
function cxnInfoUptime(sec) {
  sec = Math.max(0, Math.floor(Number(sec) || 0));
  return T('connections.info.uptimeFmt', { days: Math.floor(sec / 86400), hours: Math.floor(sec % 86400 / 3600), minutes: Math.floor(sec % 3600 / 60) });
}
// The remote panel's per-thread renderer, when this build has it.
function cxnInfoSharedThreads() {
  return typeof infoThreadsHtml === 'function' && typeof infoCoresOf === 'function' && typeof infoThreadsOpen !== 'undefined';
}
// Per core (without it): one small bar each (the server's numbers, in a grid).
function cxnInfoCores(cores) {
  var h = '<div class="il-sect">' + TH('connections.info.perCore') + '</div><div class="cxn-cores">';
  cores.slice(0, 256).forEach(function (v, i) {
    var p = infoLivePct(v);
    if (p === null) return;
    h += '<div class="cxn-core" title="' + i18nEsc(T('connections.info.core', { n: i, pct: i18nNum(p, 0) })) + '"><span class="cxn-core-fill' + infoLiveLevel(p) + '" style="height:' + p.toFixed(0) + '%"></span></div>';
  });
  return h + '</div>';
}
function cxnInfoDraw() {
  var st = cxnInfo;
  if (!st || !st.el) return;
  var d = st.data, live = !!(d && d.open) && Date.now() - st.at < CXN_INFO_POLL_MS * 3;
  var h = '<div class="il-head"><span class="il-pulse' + (live ? ' on' : '') + '" id="il-pulse"></span>' + TH('info.live') + '</div>';
  // (every poll updates the section in place: dom_patch.go domMorph)
  if (!d) { domMorph(st.el, h + '<div class="il-wait" id="il-wait">' + TH('info.liveWaiting') + '</div>'); return; }
  if (d.failed) { domMorph(st.el, h + '<div class="il-wait" id="il-wait">' + TH('connections.info.failed') + '</div>'); return; }
  if (!d.open) { domMorph(st.el, h + '<div class="il-wait" id="il-wait">' + TH('connections.info.notOpen') + '</div>'); return; }
  // (the server's own words: escaped)
  var rows = '';
  if (d.os) rows += cxnInfoRow('info.os', esc(String(d.os)));
  if (d.kernel) rows += cxnInfoRow('connections.info.kernel', esc(String(d.kernel)));
  if (d.hostname) rows += cxnInfoRow('connections.info.remoteName', esc(String(d.hostname)));
  if (d.uptimeSec) rows += cxnInfoRow('connections.info.uptime', esc(cxnInfoUptime(d.uptimeSec)));
  if (d.load) rows += cxnInfoRow('connections.info.load', esc(String(d.load)));
  if (rows) h += '<div style="display:flex;flex-direction:column;gap:6px;font-size:12px">' + rows + '</div>';
  var cpu = d.cpu && infoLivePct(d.cpu.percent), mem = d.memory || {}, ram = infoLivePct(mem.percent);
  if (cpu !== null && cpu !== undefined) h += infoLiveBar('cpu', TH('info.cpu'), cpu, d.cpu.count ? TH('connections.info.cpus', { n: d.cpu.count }) : '', st.cpu);
  if (ram !== null) {
    var mt = Number(mem.totalMb), mu = Number(mem.usedMb);
    h += infoLiveBar('ram', TH('info.ram'), ram, (isFinite(mt) && mt > 0 && isFinite(mu)) ? infoLiveSize(mu * 1048576, mt * 1048576) : '', st.ram);
  }
  if (d.cpu && Array.isArray(d.cpu.cores) && d.cpu.cores.length > 1) {
    // (the remote panel's per-thread view when this build has it: agent_tools.go
    // infoThreadsHtml, the same {cpu: {cores}} shape; else this one's own grid)
    h += cxnInfoSharedThreads() ? infoThreadsHtml(infoCoresOf(d.cpu), false) : cxnInfoCores(d.cpu.cores);
  }
  var disks = (Array.isArray(d.disks) ? d.disks : []).filter(function (x) { return x && typeof x === 'object' && infoLivePct(x.percent) !== null; });
  if (disks.length) {
    h += '<div class="il-sect">' + TH('info.disks') + '</div>';
    disks.slice(0, 8).forEach(function (x, i) {
      var tg = Number(x.totalGb), ug = Number(x.usedGb);
      h += infoLiveBar('disk' + i, i18nEsc(String(x.mount || '?').slice(0, 64)), infoLivePct(x.percent),
        (isFinite(tg) && tg > 0 && isFinite(ug)) ? infoLiveSize(ug * 1073741824, tg * 1073741824) : '', null);
    });
  }
  domMorph(st.el, h);
}
`

const localConnInfoCSS = `
/* ── A local SSH connection's Info panel (localconn_info.go) ── */
.cxn-cores{display:grid;grid-template-columns:repeat(auto-fill,minmax(10px,1fr));gap:3px;height:auto}
.cxn-core{height:26px;border-radius:3px;background:rgb(var(--ink) / .08);display:flex;align-items:flex-end;overflow:hidden}
.cxn-core-fill{display:block;width:100%;background:var(--info);transition:height .4s ease}
.cxn-core-fill.warn{background:var(--warn)}
.cxn-core-fill.crit{background:var(--danger)}
`
