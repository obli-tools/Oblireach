package main

// Self-update guard: at most ONE self-update at a time, a minimum delay
// between two attempts, and a back-off after failures. The state is persisted
// (configDir/update-state.json) because a failed MSI upgrade usually restarts
// the service: an in-memory guard alone would retry on the very first
// heartbeat of the restarted process, i.e. about once a minute.
//
// The server sends an "update" command on EVERY heartbeat (30 s) while the
// reported version is older than its own: the guard is what turns that flood
// into a single, observable attempt.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	// selfUpdateMinGap: floor between the starts of two attempts, whatever
	// their outcome or target.
	selfUpdateMinGap = 2 * time.Minute
	// selfUpdateNoRestartCooldown: msiexec reported success but this process
	// was not replaced (reboot pending, not running as the service…).
	selfUpdateNoRestartCooldown = 10 * time.Minute
	// selfUpdateUnsupportedCooldown: platforms without a self-update path.
	selfUpdateUnsupportedCooldown = 6 * time.Hour
	// selfUpdateStaleInFlight: an in-flight attempt older than this is
	// considered stuck (safety net; download and wait are bounded anyway).
	selfUpdateStaleInFlight = 60 * time.Minute
	// selfUpdatePendingWait: on start, how long to wait for the previous
	// msiexec (launched by the process that was replaced) to finish its log.
	selfUpdatePendingWait = 15 * time.Minute
	// selfUpdatePendingMaxAge: an attempt older than this is concluded from
	// whatever its log says, without waiting.
	selfUpdatePendingMaxAge = time.Hour
	// selfUpdateSkipLogEvery: identical "skipped" lines are logged at most
	// this often (the server repeats the command every 30 s).
	selfUpdateSkipLogEvery = 10 * time.Minute

	selfUpdateKeepLogs      = 5
	selfUpdateLogMaxAge     = 14 * 24 * time.Hour
	selfUpdateCleanupMinAge = 20 * time.Minute
	selfUpdateFilePrefix    = "oblireach-update-"
)

// Back-off after a failed attempt (download, verification, msiexec error),
// per target version. The last step repeats.
var selfUpdateFailureSteps = []time.Duration{
	2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 60 * time.Minute,
}

// Back-off while Windows Installer is busy (another MSI transaction, e.g. the
// Obliance agent installing ObliReach): short, the condition is transient.
// Not counted as a failure.
var selfUpdateBusySteps = []time.Duration{
	2 * time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute,
}

func backoffStep(steps []time.Duration, n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(steps) {
		n = len(steps)
	}
	return steps[n-1]
}

// ── Versions ─────────────────────────────────────────────────────────────────

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// isNewerVersion reports whether target is strictly newer than current.
// Unparseable versions (e.g. a "dev" build) are "newer" unless identical, so
// a dev build still follows the server as before.
func isNewerVersion(target, current string) bool {
	t, okT := parseVersion(target)
	c, okC := parseVersion(current)
	if !okT || !okC {
		return strings.TrimSpace(target) != strings.TrimSpace(current)
	}
	for i := 0; i < len(t) || i < len(c); i++ {
		var a, b int
		if i < len(t) {
			a = t[i]
		}
		if i < len(c) {
			b = c[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

// ── State ────────────────────────────────────────────────────────────────────

// selfUpdateAttempt is one self-update attempt. Once msiexec is launched it
// is persisted as "pending" so the process that starts after the upgrade (the
// new version, or the old one after a rollback) can log the result.
type selfUpdateAttempt struct {
	ID        string    `json:"id"`
	Target    string    `json:"target"`
	From      string    `json:"from"`
	StartedAt time.Time `json:"startedAt"`
	MSI       string    `json:"msi,omitempty"`
	Log       string    `json:"log,omitempty"`
	PID       int       `json:"pid,omitempty"`
}

type selfUpdateState struct {
	Target        string             `json:"target,omitempty"` // version the counters refer to
	Failures      int                `json:"failures,omitempty"`
	BusyRetries   int                `json:"busyRetries,omitempty"`
	NextAttemptAt time.Time          `json:"nextAttemptAt,omitempty"`
	LastStartAt   time.Time          `json:"lastStartAt,omitempty"`
	LastError     string             `json:"lastError,omitempty"`
	LastResult    string             `json:"lastResult,omitempty"`
	LastResultAt  time.Time          `json:"lastResultAt,omitempty"`
	LastOutcome   string             `json:"lastOutcome,omitempty"` // selfUpdateOutcome.String() of the last result
	LastReason    string             `json:"lastReason,omitempty"`  // machine-readable cause of the last failure (sha2_unsupported, wrong_flavor)
	Pending       *selfUpdateAttempt `json:"pending,omitempty"`     // msiexec launched, result not known yet
}

type selfUpdateOutcome int

const (
	outcomeInstalled   selfUpdateOutcome = iota // the target version is running
	outcomeFailed                               // download / verification / msiexec error
	outcomeBusy                                 // Windows Installer busy (another MSI, 1618)
	outcomeNoRestart                            // msiexec said OK but this process was not replaced
	outcomeUnsupported                          // no self-update on this platform
)

func (o selfUpdateOutcome) String() string {
	switch o {
	case outcomeInstalled:
		return "installed"
	case outcomeFailed:
		return "failed"
	case outcomeBusy:
		return "busy"
	case outcomeNoRestart:
		return "not_restarted"
	case outcomeUnsupported:
		return "unsupported"
	}
	return "unknown"
}

type selfUpdateResult struct {
	Outcome selfUpdateOutcome
	Stage   string // precheck, download, verify, signature, launch, msiexec
	Code    int    // msiexec exit / log status, -1 = unknown
	Detail  string
	// Reason: machine-readable cause when the server / device page can act
	// on it: "sha2_unsupported" (Windows 7 / 2008 R2 without KB4474419 /
	// KB3033929 cannot verify the SHA-256 signature), "wrong_flavor" (the
	// package is not of this Windows' build line). "" otherwise.
	Reason string
}

func (r selfUpdateResult) summary() string {
	s := r.Outcome.String()
	if r.Stage != "" {
		s += " at " + r.Stage
	}
	if r.Code >= 0 && (r.Stage == "msiexec" || r.Outcome == outcomeNoRestart) {
		s += fmt.Sprintf(" (msiexec status %d: %s)", r.Code, msiExitText(r.Code))
	}
	if r.Detail != "" {
		s += ": " + r.Detail
	}
	return s
}

// msiExitText gives the meaning of the msiexec exit codes seen in practice.
func msiExitText(code int) string {
	switch code {
	case 0:
		return "success"
	case 1602:
		return "cancelled"
	case 1603:
		return "fatal error during installation"
	case 1618:
		return "another installation is already in progress"
	case 1619:
		return "package could not be opened"
	case 1620:
		return "package is invalid or corrupt"
	case 1625:
		return "installation forbidden by system policy"
	case 1633:
		return "platform not supported"
	case 1638:
		return "another version of this product is already installed"
	case 1641:
		return "success, reboot initiated"
	case 3010:
		return "success, reboot required"
	}
	return "see the msiexec log"
}

// ── Guard ────────────────────────────────────────────────────────────────────

type updateDecision struct {
	OK      bool
	Reason  string // not_newer, in_progress, cooldown
	Detail  string
	Log     bool // false: identical skip already logged recently
	Attempt *selfUpdateAttempt
}

type selfUpdateGuard struct {
	mu     sync.Mutex
	now    func() time.Time
	pathFn func() string // state file; "" = memory only
	loaded bool
	st     selfUpdateState

	inFlight      *selfUpdateAttempt
	inFlightSince time.Time

	lastSkipKey string
	lastSkipLog time.Time
}

func newSelfUpdateGuard(pathFn func() string) *selfUpdateGuard {
	return &selfUpdateGuard{now: time.Now, pathFn: pathFn}
}

func (g *selfUpdateGuard) path() string {
	if g.pathFn == nil {
		return ""
	}
	return g.pathFn()
}

func (g *selfUpdateGuard) ensureLoadedLocked() {
	if g.loaded {
		return
	}
	g.loaded = true
	p := g.path()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var st selfUpdateState
	if json.Unmarshal(data, &st) == nil {
		g.st = st
	}
}

func (g *selfUpdateGuard) saveLocked() error {
	p := g.path()
	if p == "" {
		return nil
	}
	data, err := json.MarshalIndent(g.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// skipLocked builds a "skipped" decision. An identical skip (same key) is
// logged again only after selfUpdateSkipLogEvery, or never when !repeat.
func (g *selfUpdateGuard) skipLocked(reason, key, detail string, now time.Time, repeat bool) updateDecision {
	key = reason + "|" + key
	logIt := key != g.lastSkipKey || (repeat && now.Sub(g.lastSkipLog) >= selfUpdateSkipLogEvery)
	if logIt {
		g.lastSkipKey = key
		g.lastSkipLog = now
	}
	return updateDecision{Reason: reason, Detail: detail, Log: logIt}
}

// begin decides whether an "update" command may start an attempt now. When
// it returns OK, the caller owns the attempt and MUST end it with finish()
// (or launched() then finish()).
func (g *selfUpdateGuard) begin(id, current, target string) updateDecision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	now := g.now()

	if target != "" && !isNewerVersion(target, current) {
		return g.skipLocked("not_newer", target,
			fmt.Sprintf("v%s is not newer than the running v%s", target, current), now, true)
	}
	if g.inFlight == nil && g.st.Pending != nil {
		// msiexec launched by the previous process, result not concluded yet
		// (selfUpdateOnStart is on it): same as an attempt in progress.
		cp := *g.st.Pending
		g.inFlight = &cp
		g.inFlightSince = now
	}
	if g.inFlight != nil {
		age := now.Sub(g.inFlightSince)
		if age < selfUpdateStaleInFlight {
			return g.skipLocked("in_progress", g.inFlight.ID,
				fmt.Sprintf("an update to v%s is already in progress (id=%s, started %s ago)",
					g.inFlight.Target, g.inFlight.ID, age.Round(time.Second)), now, true)
		}
		// Stuck attempt: forget it (its late finish() will be ignored).
		if g.st.Pending != nil && g.st.Pending.ID == g.inFlight.ID {
			g.st.Pending = nil
		}
		g.inFlight = nil
	}
	if target != g.st.Target {
		// New target: the failures of the previous one no longer apply
		// (typically a fixed build). The minimum gap still does.
		g.st.Target = target
		g.st.Failures = 0
		g.st.BusyRetries = 0
		g.st.NextAttemptAt = time.Time{}
		g.st.LastError = ""
	}
	wait := g.st.NextAttemptAt
	if isManualUpdateID(id) {
		// The "Update Reach" button (an operator's explicit retry): the
		// failure / busy back-off does not apply; one attempt at a time and
		// the minimum gap still do.
		wait = time.Time{}
	}
	if gap := g.st.LastStartAt.Add(selfUpdateMinGap); gap.After(wait) {
		wait = gap
	}
	if wait.Sub(now) > selfUpdateUnsupportedCooldown+time.Hour {
		// Longer than any back-off: the clock went back. Do not stay blocked.
		wait = time.Time{}
	}
	if now.Before(wait) {
		detail := fmt.Sprintf("next attempt not before %s", wait.Format("15:04:05"))
		if g.st.LastError != "" {
			detail += " (last error: " + g.st.LastError + ")"
		}
		// Logged once per waiting window (the server repeats every 30 s).
		return g.skipLocked("cooldown", target+"|"+wait.Format(time.RFC3339), detail, now, false)
	}

	att := &selfUpdateAttempt{ID: id, Target: target, From: current, StartedAt: now}
	cp := *att // the caller's copy is written by the pipeline without the lock
	g.inFlight = &cp
	g.inFlightSince = now
	g.st.LastStartAt = now
	g.lastSkipKey = ""
	_ = g.saveLocked()
	return updateDecision{OK: true, Attempt: att, Log: true}
}

// launched records that msiexec is running for att: from now on the result
// may only be known by the NEXT process (the installer stops this service).
func (g *selfUpdateGuard) launched(att *selfUpdateAttempt) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	cp := *att
	g.st.Pending = &cp
	return g.saveLocked()
}

// adoptPending is called once at start: an attempt launched by the previous
// process becomes this process' in-flight attempt until its result is known.
func (g *selfUpdateGuard) adoptPending() *selfUpdateAttempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	if g.st.Pending == nil {
		return nil
	}
	if g.inFlight == nil || g.inFlight.ID != g.st.Pending.ID {
		// (begin() may already have adopted it — keep its start time then)
		held := *g.st.Pending
		g.inFlight = &held
		g.inFlightSince = g.now()
	}
	cp := *g.st.Pending
	return &cp
}

// finish ends att with res and returns the earliest time of the next attempt.
func (g *selfUpdateGuard) finish(att *selfUpdateAttempt, res selfUpdateResult) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	now := g.now()

	current := g.inFlight != nil && g.inFlight.ID == att.ID
	pending := g.st.Pending != nil && g.st.Pending.ID == att.ID
	if !current && !pending {
		// Superseded (stale) attempt: informational only.
		return g.nextLocked()
	}
	if current {
		g.inFlight = nil
	}
	if pending {
		g.st.Pending = nil
	}
	if att.Target != g.st.Target {
		g.st.Target = att.Target
		g.st.Failures = 0
		g.st.BusyRetries = 0
	}

	switch res.Outcome {
	case outcomeInstalled:
		g.st.Failures = 0
		g.st.BusyRetries = 0
		g.st.NextAttemptAt = time.Time{}
		g.st.LastError = ""
	case outcomeBusy:
		g.st.BusyRetries++
		g.st.NextAttemptAt = now.Add(backoffStep(selfUpdateBusySteps, g.st.BusyRetries))
		g.st.LastError = res.summary()
	case outcomeNoRestart:
		g.st.Failures++
		d := backoffStep(selfUpdateFailureSteps, g.st.Failures)
		if d < selfUpdateNoRestartCooldown {
			d = selfUpdateNoRestartCooldown
		}
		g.st.NextAttemptAt = now.Add(d)
		g.st.LastError = res.summary()
	case outcomeUnsupported:
		g.st.NextAttemptAt = now.Add(selfUpdateUnsupportedCooldown)
		g.st.LastError = ""
	default: // outcomeFailed
		g.st.Failures++
		g.st.NextAttemptAt = now.Add(backoffStep(selfUpdateFailureSteps, g.st.Failures))
		g.st.LastError = res.summary()
	}
	g.st.LastResult = fmt.Sprintf("v%s -> v%s: %s", att.From, att.Target, res.summary())
	g.st.LastResultAt = now
	g.st.LastOutcome = res.Outcome.String()
	g.st.LastReason = res.Reason
	_ = g.saveLocked()
	return g.nextLocked()
}

func (g *selfUpdateGuard) nextLocked() time.Time {
	next := g.st.NextAttemptAt
	if gap := g.st.LastStartAt.Add(selfUpdateMinGap); gap.After(next) {
		next = gap
	}
	return next
}

func (g *selfUpdateGuard) snapshot() selfUpdateState {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	st := g.st
	if st.Pending != nil {
		cp := *st.Pending
		st.Pending = &cp
	}
	return st
}

// isManualUpdateID: the server names the command of the "Update Reach"
// button manual_update_<ms> (automatic ones: auto_update[_ws]_<ms>).
func isManualUpdateID(id string) bool {
	return strings.HasPrefix(id, "manual_update_")
}

// selfUpdateStatus is the self-update part of the heartbeat: the server logs
// its changes and the device page shows why an update is not there yet
// (attempt in progress, last failure, next automatic attempt).
type selfUpdateStatus struct {
	InProgress    bool   `json:"inProgress,omitempty"`
	Target        string `json:"target,omitempty"`        // in progress: its target; else the version the counters refer to
	Since         string `json:"since,omitempty"`         // in progress: when it started
	LastOutcome   string `json:"lastOutcome,omitempty"`   // installed | failed | busy | not_restarted | unsupported
	LastResult    string `json:"lastResult,omitempty"`    // "v1 -> v2: failed at signature: …"
	LastResultAt  string `json:"lastResultAt,omitempty"`  // RFC 3339
	LastError     string `json:"lastError,omitempty"`     // cleared by a success
	LastReason    string `json:"lastReason,omitempty"`    // sha2_unsupported | wrong_flavor (cleared by a success)
	Failures      int    `json:"failures,omitempty"`      // failed attempts in a row for Target
	NextAttemptAt string `json:"nextAttemptAt,omitempty"` // automatic attempts not before (a manual request only waits for the minimum gap)
}

const selfUpdateStatusMaxText = 300

// status returns the heartbeat's self-update block (nil when there is
// nothing to tell: no attempt ever made).
func (g *selfUpdateGuard) status() *selfUpdateStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLoadedLocked()
	now := g.now()
	s := &selfUpdateStatus{
		Target:      g.st.Target,
		LastOutcome: g.st.LastOutcome,
		LastResult:  capText(g.st.LastResult, selfUpdateStatusMaxText),
		LastError:   capText(g.st.LastError, selfUpdateStatusMaxText),
		LastReason:  g.st.LastReason,
		Failures:    g.st.Failures,
	}
	if !g.st.LastResultAt.IsZero() {
		s.LastResultAt = g.st.LastResultAt.UTC().Format(time.RFC3339)
	}
	switch {
	case g.inFlight != nil && now.Sub(g.inFlightSince) < selfUpdateStaleInFlight:
		s.InProgress, s.Target = true, g.inFlight.Target
		s.Since = g.inFlight.StartedAt.UTC().Format(time.RFC3339)
	case g.inFlight == nil && g.st.Pending != nil && now.Sub(g.st.Pending.StartedAt) < selfUpdateStaleInFlight:
		// msiexec launched by the previous process, not concluded yet.
		s.InProgress, s.Target = true, g.st.Pending.Target
		s.Since = g.st.Pending.StartedAt.UTC().Format(time.RFC3339)
	}
	if !s.InProgress && s.LastOutcome != outcomeInstalled.String() {
		if next := g.nextLocked(); next.After(now) {
			s.NextAttemptAt = next.UTC().Format(time.RFC3339)
		}
	}
	if !s.InProgress && s.Target == "" && s.LastResultAt == "" {
		return nil
	}
	return s
}

func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Do not cut a UTF-8 sequence in half.
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

func (g *selfUpdateGuard) inFlightAttempt() *selfUpdateAttempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight == nil {
		return nil
	}
	cp := *g.inFlight
	return &cp
}

// ── Result of a previous attempt ─────────────────────────────────────────────

// concludePending turns what the next process sees into a result: the running
// version first (whoever installed it — this attempt or the Obliance agent),
// then the status in the msiexec log.
func concludePending(att *selfUpdateAttempt, running string, status int, found bool) selfUpdateResult {
	if att.Target == "" || !isNewerVersion(att.Target, running) {
		code := -1
		if found {
			code = status
		}
		return selfUpdateResult{Outcome: outcomeInstalled, Stage: "", Code: code}
	}
	if !found {
		return selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: -1,
			Detail: "result unknown (msiexec log missing or incomplete)"}
	}
	switch status {
	case 1618:
		return selfUpdateResult{Outcome: outcomeBusy, Stage: "msiexec", Code: status}
	case 0, 1641, 3010:
		return selfUpdateResult{Outcome: outcomeNoRestart, Stage: "msiexec", Code: status,
			Detail: fmt.Sprintf("v%s is still running", running)}
	}
	return selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: status}
}

var (
	reMSIStatus    = regexp.MustCompile(`Installation success or error status: (\d+)`)
	reMSIReturning = regexp.MustCompile(`MainEngineThread is returning (\d+)`)
)

// decodeMSILog returns the text of an msiexec log (UTF-16LE with a BOM on
// current Windows, ANSI on old ones).
func decodeMSILog(b []byte) string {
	utf16le := false
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		utf16le = true
		b = b[2:]
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return string(b[3:])
	case len(b) >= 4 && b[0] != 0 && b[1] == 0 && b[2] != 0 && b[3] == 0:
		utf16le = true
	}
	if !utf16le {
		return string(b)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// parseMSILog extracts the installation status of an msiexec /l*v log.
// done is true once msiexec closed the log.
func parseMSILog(b []byte) (status int, found bool, done bool) {
	text := decodeMSILog(b)
	done = strings.Contains(text, "=== Verbose logging stopped") ||
		strings.Contains(text, "=== Logging stopped")
	if m := reMSIStatus.FindAllStringSubmatch(text, -1); len(m) > 0 {
		if n, err := strconv.Atoi(m[len(m)-1][1]); err == nil {
			return n, true, done
		}
	}
	if m := reMSIReturning.FindAllStringSubmatch(text, -1); len(m) > 0 {
		if n, err := strconv.Atoi(m[len(m)-1][1]); err == nil {
			return n, true, done
		}
	}
	return 0, false, done
}

// ── Files ────────────────────────────────────────────────────────────────────

// isSelfUpdateFileName: names this agent creates for its self-update
// (oblireach-update-<random>.msi/.log) plus the pre-1.0.200 fixed names.
func isSelfUpdateFileName(name string) bool {
	lower := strings.ToLower(name)
	if lower == "oblireach-update.msi" || lower == "oblireach-update.log" {
		return true
	}
	if !strings.HasPrefix(lower, selfUpdateFilePrefix) {
		return false
	}
	if !strings.HasSuffix(lower, ".msi") && !strings.HasSuffix(lower, ".log") {
		return false
	}
	return !strings.ContainsAny(name, `/\:`)
}

// isOwnUpdateFile: a path read from the state file is only used (read or
// deleted) when it is one of our own file names directly inside dir.
func isOwnUpdateFile(path, dir string) bool {
	if path == "" || dir == "" || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if !strings.EqualFold(filepath.Dir(clean), filepath.Clean(dir)) {
		return false
	}
	return isSelfUpdateFileName(filepath.Base(clean))
}

// cleanupSelfUpdateFiles removes leftover packages and old logs from dir.
// keep: paths to leave alone. Files modified less than selfUpdateCleanupMinAge
// ago are left alone too (an attempt may be downloading right now: the
// start-up cleanup runs concurrently with the first "update" command); the
// package of a finished attempt is removed explicitly by its owner.
// Failed-attempt logs are kept for diagnosis: the newest selfUpdateKeepLogs,
// at most 14 days.
func cleanupSelfUpdateFiles(dir string, keep map[string]bool, now time.Time) (removed []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	isKept := func(p string) bool {
		for k := range keep {
			if strings.EqualFold(filepath.Clean(k), filepath.Clean(p)) {
				return true
			}
		}
		return false
	}
	type logFile struct {
		path string
		mod  time.Time
	}
	var logs []logFile
	for _, e := range entries {
		if e.IsDir() || !isSelfUpdateFileName(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if isKept(p) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".msi") {
			if now.Sub(info.ModTime()) >= selfUpdateCleanupMinAge && os.Remove(p) == nil {
				removed = append(removed, p)
			}
			continue
		}
		logs = append(logs, logFile{p, info.ModTime()})
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod.After(logs[j].mod) })
	for i, l := range logs {
		if now.Sub(l.mod) < selfUpdateCleanupMinAge {
			continue
		}
		if i >= selfUpdateKeepLogs || now.Sub(l.mod) > selfUpdateLogMaxAge {
			if os.Remove(l.path) == nil {
				removed = append(removed, l.path)
			}
		}
	}
	return removed
}
