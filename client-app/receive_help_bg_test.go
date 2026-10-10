package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRotHost: a registration that draws a new code on demand (same ID).
type fakeRotHost struct{ *fakeHelpHost }

var fakeRotMu sync.Mutex
var fakeRotCodes = map[*fakeHelpHost]string{}

func (r *fakeRotHost) Code() string {
	fakeRotMu.Lock()
	defer fakeRotMu.Unlock()
	if c, ok := fakeRotCodes[r.fakeHelpHost]; ok {
		return c
	}
	return r.fakeHelpHost.code
}

func (r *fakeRotHost) RotateCode() bool {
	fakeRotMu.Lock()
	n := len(fakeRotCodes[r.fakeHelpHost])
	c := "70000000" + string(rune('1'+n%9))
	fakeRotCodes[r.fakeHelpHost] = c
	fakeRotMu.Unlock()
	r.hooks.OnCredentials(r.id, c, "expired")
	return true
}

func newBgHelpHost(t *testing.T, store *memSupportStore) (*helpHost, *fakeHelpBackend) {
	t.Helper()
	b := newFakeHelpBackend()
	h := newHelpHost(&supportSettings{store: store}, b)
	h.ApprovalTimeout = 2 * time.Second
	h.Backoff = []time.Duration{40 * time.Millisecond}
	h.PathRefresh = 20 * time.Millisecond
	t.Cleanup(h.stop)
	return h, b
}

// From the app's start, with nothing set up: on by default, on the
// built-in public server, no key needed; the toggle off stops it and is
// kept; the anti-scam notice is acknowledged once.
func TestHelpBackgroundToggle(t *testing.T) {
	store := &memSupportStore{}
	h, b := newBgHelpHost(t, store)
	if on, ack := h.prefs(); !on || ack {
		t.Fatalf("defaults: incoming %v ack %v", on, ack)
	}
	h.autoStart()
	host := b.nextHost(t)
	if b.server != supportPublicURL {
		t.Fatalf("registered with %q", b.server)
	}
	v := waitHelpPhase(t, h, helpWaiting)
	if v.ID != host.id || v.Code != host.code || v.ServerID != supportPublicID || !v.Incoming || v.ScamAck {
		t.Fatalf("waiting %+v", v)
	}
	// The notice read: kept.
	if err := h.acknowledgeScam(); err != nil {
		t.Fatal(err)
	}
	if !h.view(0).ScamAck {
		t.Fatal("ack not in the view")
	}
	// Off: idle, the registration closed, kept off.
	if err := h.setIncoming(false, false); err != nil {
		t.Fatal(err)
	}
	if v := h.view(0); v.Phase != helpIdle || v.Incoming {
		t.Fatalf("off %+v", v)
	}
	select {
	case <-host.done:
	default:
		t.Fatal("the registration is still open")
	}
	// A new run of the app: off, not started; the notice still read.
	h2, b2 := newBgHelpHost(t, store)
	h2.autoStart()
	if on, ack := h2.prefs(); on || !ack {
		t.Fatalf("kept: incoming %v ack %v", on, ack)
	}
	select {
	case <-b2.hosts:
		t.Fatal("registered while off")
	case <-time.After(100 * time.Millisecond):
	}
	// On again.
	if err := h2.setIncoming(true, false); err != nil {
		t.Fatal(err)
	}
	b2.nextHost(t)
	waitHelpPhase(t, h2, helpWaiting)
	if err := h2.setIncoming(true, true); err != nil { // (already on: fine)
		t.Fatal(err)
	}
	// Unreadable settings: on (the public server), nothing kept.
	h3, b3 := newBgHelpHost(t, &memSupportStore{fail: true})
	h3.autoStart()
	b3.nextHost(t)
	if b3.server != supportPublicURL {
		t.Fatalf("broken settings: %q", b3.server)
	}
	if err := h3.setIncoming(false, false); err == nil || err.(*helpError).Code != "storage" {
		t.Fatalf("broken settings off: %v", err)
	}
	if h3.phase() != helpIdle {
		t.Fatal("not stopped")
	}
}

// The default server: a server added (it becomes the default) is followed;
// not while someone is being helped.
func TestHelpBackgroundFollowsDefault(t *testing.T) {
	store := &memSupportStore{}
	h, b := newBgHelpHost(t, store)
	h.autoStart()
	host := b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
	list, err := h.settings.save(supportServerInput{Label: "Desk", URL: "desk.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	h.settingsChanged()
	b.nextHost(t)
	v := waitHelpPhase(t, h, helpWaiting)
	if b.server != "https://desk.example.com" || v.Server != "Desk" {
		t.Fatalf("not following the new default: %q %+v", b.server, v)
	}
	select {
	case <-host.done:
	default:
		t.Fatal("the old registration is still open")
	}
	// A label edit: no new registration.
	sid := supportUserServers(list)[0].ID
	if _, err := h.settings.save(supportServerInput{ID: sid, Label: "Help desk", URL: "desk.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.settingsChanged()
	if v := h.view(0); v.Server != "Help desk" {
		t.Fatalf("label %+v", v)
	}
	select {
	case <-b.hosts:
		t.Fatal("re-registered for a label")
	default:
	}
}

// Reconnection: backoff, "try again now", the link lost (a new ID).
func TestHelpBackgroundReconnect(t *testing.T) {
	h, b := newBgHelpHost(t, &memSupportStore{})
	b.regErr = []error{errors.New("dial: refused"), errors.New("dial: refused")}
	h.Backoff = []time.Duration{time.Hour}
	h.autoStart()
	v := waitHelpPhase(t, h, helpUnreachable)
	if v.RetryIn < 3500 {
		t.Fatalf("backoff %+v", v)
	}
	if !h.retryNow() {
		t.Fatal("retryNow refused")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		left := len(b.regErr)
		b.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retryNow did not try again")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitHelpPhase(t, h, helpUnreachable)
	h.retryNow()
	host := b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
	if h.retryNow() {
		t.Fatal("retryNow while registered")
	}
	// The signalling link is lost: registered again, a new ID.
	host.Close()
	host2 := b.nextHost(t)
	v = waitHelpPhase(t, h, helpWaiting)
	if v.ID != host2.id || v.ID == host.id {
		t.Fatalf("after the link was lost %+v", v)
	}
}

// "New code": the registration's own (same ID) when it can, else a new
// registration; only while waiting.
func TestHelpBackgroundNewCode(t *testing.T) {
	h, b := newBgHelpHost(t, &memSupportStore{})
	b.rotate = true
	h.autoStart()
	host := b.nextHost(t)
	v := waitHelpPhase(t, h, helpWaiting)
	if !h.newCode() {
		t.Fatal("newCode refused")
	}
	v2 := h.view(0)
	if v2.ID != v.ID || v2.Code == v.Code || v2.Code == "" {
		t.Fatalf("rotated %+v -> %+v", v, v2)
	}
	select {
	case <-b.hosts:
		t.Fatal("a new registration for a new code")
	default:
	}
	// Not during an acceptance request.
	res := approveAsync(host, context.Background(), "N1")
	waitHelpPhase(t, h, helpApproval)
	if h.newCode() {
		t.Fatal("newCode during a request")
	}
	h.decide("N1", false, helpPerms{})
	<-res
	h.stop()

	// Without rotation: a new registration.
	h2, b2 := newBgHelpHost(t, &memSupportStore{})
	h2.autoStart()
	first := b2.nextHost(t)
	waitHelpPhase(t, h2, helpWaiting)
	if !h2.newCode() {
		t.Fatal("newCode refused")
	}
	second := b2.nextHost(t)
	v = waitHelpPhase(t, h2, helpWaiting)
	if v.Code != second.code || v.ID == first.id {
		t.Fatalf("new registration %+v", v)
	}
}

// The one-time code never leaves the process except on screen: nothing
// about it reaches the support settings (the only file this controller
// writes), whatever happens (sessions, new codes, toggles).
func TestHelpCodeNeverStored(t *testing.T) {
	store := &memSupportStore{}
	h, b := newBgHelpHost(t, store)
	b.rotate = true
	h.autoStart()
	host := b.nextHost(t)
	codes := map[string]bool{}
	v := waitHelpPhase(t, h, helpWaiting)
	codes[v.Code] = true
	_ = h.acknowledgeScam()
	h.newCode()
	codes[h.view(0).Code] = true
	host.hooks.OnCredentials(host.id, "246813579", "failed")
	codes["246813579"] = true
	res := approveAsync(host, context.Background(), "C1")
	waitHelpPhase(t, h, helpApproval)
	h.decide("C1", true, helpPerms{Control: true})
	<-res
	host.ops <- newFakeHelpSess("C1")
	r := b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	host.hooks.OnCredentials(host.id, "135792468", "used")
	codes["135792468"] = true
	r.end <- nil
	waitHelpNotice(t, h, helpNoticeEnded)
	_ = h.setIncoming(false, true)
	_ = h.setIncoming(true, false)
	b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
	codes[h.view(0).Code] = true
	if _, err := h.settings.save(supportServerInput{Label: "Desk", URL: "desk.example.com"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	data := string(store.data)
	store.mu.Unlock()
	if data == "" {
		t.Fatal("nothing saved: the test proves nothing")
	}
	for c := range codes {
		if c == "" {
			continue
		}
		if strings.Contains(data, c) {
			t.Errorf("the code %s is in the saved settings: %s", c, data)
		}
	}
	for _, k := range []string{`"code"`, "Code"} {
		if strings.Contains(data, k) {
			t.Errorf("a code field in the saved settings: %s", data)
		}
	}
}

// A signalling drop: supportlink reconnects by itself (OnState); the
// registration, the ID and the code stay; a new ID from the server is said.
func TestHelpBackgroundLinkResume(t *testing.T) {
	h, b := newBgHelpHost(t, &memSupportStore{})
	h.autoStart()
	host := b.nextHost(t)
	v := waitHelpPhase(t, h, helpWaiting)
	host.hooks.OnState(helpLinkConnecting)
	v2 := h.view(0)
	if v2.Phase != helpWaiting || v2.Link != helpLinkConnecting || v2.ID != v.ID || v2.Code != v.Code {
		t.Fatalf("during the drop %+v", v2)
	}
	host.hooks.OnState(helpLinkOnline)
	if v2 = h.view(0); v2.Link != "" {
		t.Fatalf("back online %+v", v2)
	}
	select {
	case <-b.hosts:
		t.Fatal("re-registered for a drop")
	case <-time.After(100 * time.Millisecond):
	}
	host.hooks.OnCredentials("777888999", "123123123", helpCredReasonNewID)
	if v2 = h.view(0); v2.ID != "777888999" || v2.Code != "123123123" || v2.Notice != helpNoticeNewID {
		t.Fatalf("new ID %+v", v2)
	}
	host.hooks.OnCredentials("777888999", "456456456", helpCredReasonManual)
	if v2 = h.view(0); v2.Code != "456456456" || v2.Notice != "" {
		t.Fatalf("manual %+v", v2)
	}
}
