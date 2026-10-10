package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	sl "github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

// dropHost cuts the signalling connection of the host holding id, as a
// network blip would.
func dropHost(t *testing.T, e *testEnv, id string) {
	t.Helper()
	e.srv.hub.mu.Lock()
	c := e.srv.hub.hosts[id]
	e.srv.hub.mu.Unlock()
	if c == nil {
		t.Fatalf("host %s not registered", id)
	}
	_ = c.conn.Close()
}

type stateRec struct {
	mu sync.Mutex
	st []sl.HostState
}

func (r *stateRec) add(s sl.HostState) {
	r.mu.Lock()
	r.st = append(r.st, s)
	r.mu.Unlock()
}

func (r *stateRec) get() []sl.HostState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sl.HostState(nil), r.st...)
}

func TestHostResumeSameID(t *testing.T) {
	e := newEnv(t, nil)
	states := &stateRec{}
	hr := startHost(t, e, loopback, allow, func(c *sl.HostConfig) { c.OnState = states.add })
	id, code := hr.h.ID(), hr.h.Code()

	dropHost(t, e, id)
	waitFor(t, func() bool { return len(states.get()) >= 1 })
	if s := states.get(); s[0] != sl.HostConnecting {
		t.Fatalf("states %v", s)
	}
	waitFor(t, func() bool { return hr.h.State() == sl.HostOnline })
	if got := states.get(); len(got) != 2 || got[1] != sl.HostOnline {
		t.Fatalf("states %v", got)
	}
	if hr.h.ID() != id || hr.h.Code() != code {
		t.Fatalf("ID or code changed by a blip: %s/%s -> %s/%s", id, code, hr.h.ID(), hr.h.Code())
	}
	if _, n := hr.lastCreds(); n != 0 {
		t.Fatal("credentials reported changed")
	}
	if e.metric(t, "rdv_resumed_total") != "1" || e.metric(t, "rdv_registered_total") != "1" {
		t.Fatalf("resumed %s registered %s", e.metric(t, "rdv_resumed_total"), e.metric(t, "rdv_registered_total"))
	}
	// The resumed host is fully usable with its ID and code.
	hc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, err := hr.h.WaitForOperator(ctx)
		if s != nil {
			defer s.Close()
		}
		hc <- err
	}()
	os, err := connect(t, opConfig(e, loopback, nil), id, code)
	if err != nil {
		t.Fatalf("connect after resume: %v", err)
	}
	if err := <-hc; err != nil {
		t.Fatal(err)
	}
	os.Close()
	// A second blip resumes again (the secret rotates at each resume).
	dropHost(t, e, id)
	waitFor(t, func() bool { return e.metric(t, "rdv_resumed_total") == "2" && hr.h.State() == sl.HostOnline })
	if hr.h.ID() != id {
		t.Fatal("second resume changed the ID")
	}
}

func TestHostResumeGraceOver(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ResumeGrace = time.Millisecond })
	hr := startHost(t, e, loopback, allow, nil)
	id := hr.h.ID()
	dropHost(t, e, id)
	waitFor(t, func() bool { _, n := hr.lastCreds(); return n == 1 && hr.h.State() == sl.HostOnline })
	c, _ := hr.lastCreds()
	if c.Reason != sl.ReasonNewID || c.ID == id || c.ID != hr.h.ID() || c.Code != hr.h.Code() {
		t.Fatalf("after the grace window: %+v (old ID %s)", c, id)
	}
}

func TestHostDisableReconnect(t *testing.T) {
	e := newEnv(t, nil)
	hr := startHost(t, e, loopback, allow, func(c *sl.HostConfig) { c.DisableReconnect = true })
	dropHost(t, e, hr.h.ID())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := hr.h.WaitForOperator(ctx); !errors.Is(err, sl.ErrHostClosed) || !errors.Is(err, sl.ErrUnreachable) {
		t.Fatalf("lost link: %v", err)
	}
	if hr.h.State() != sl.HostClosed {
		t.Fatal(hr.h.State())
	}
}

// rawRegister registers a raw host, optionally asking for id back.
func rawRegister(t *testing.T, e *testEnv, id, secret string) (*websocket.Conn, wire.Msg) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(e.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.WriteJSON(wire.Msg{T: wire.THello, Proto: wire.ProtocolVersion, Role: wire.RoleHost, Ver: "1.0.0"})
	_ = c.WriteJSON(wire.Msg{T: wire.TRegister, ID: id, Resume: secret})
	var m wire.Msg
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.ReadJSON(&m); err != nil || m.T != wire.TRegistered {
		t.Fatalf("register: %+v %v", m, err)
	}
	return c, m
}

func (e *testEnv) reserved(id string) bool {
	e.srv.hub.mu.Lock()
	defer e.srv.hub.mu.Unlock()
	_, ok := e.srv.hub.resumable[id]
	return ok
}

func TestResumeRules(t *testing.T) {
	e := newEnv(t, nil)
	a, ra := rawRegister(t, e, "", "")
	id := ra.ID
	if len(ra.Resume) != 32 || ra.TTL != 600 {
		t.Fatalf("registered %+v", ra)
	}
	a.Close()
	waitFor(t, func() bool { return e.reserved(id) })

	// While reserved, the ID looks offline to operators.
	if _, err := connect(t, opConfig(e, loopback, nil), id, "123456789"); !errors.Is(err, sl.ErrUnknownID) {
		t.Fatalf("reserved ID: %v", err)
	}
	// Wrong secret: a new ID, the reservation stays.
	_, rb := rawRegister(t, e, id, "00000000000000000000000000000000")
	if rb.ID == id || !e.reserved(id) {
		t.Fatalf("wrong secret got the ID: %+v", rb)
	}
	// Right secret: the same ID and a new secret.
	c, rc := rawRegister(t, e, id, ra.Resume)
	if rc.ID != id || rc.Resume == ra.Resume || e.reserved(id) {
		t.Fatalf("resume: %+v", rc)
	}
	// The old secret is spent.
	if _, rx := rawRegister(t, e, id, ra.Resume); rx.ID == id {
		t.Fatal("a spent secret resumed the ID")
	}
	// Takeover: the holder's connection is not seen as dead yet; the same
	// secret moves the ID to the new connection and closes the old one.
	d, rd := rawRegister(t, e, id, rc.Resume)
	if rd.ID != id {
		t.Fatalf("takeover: %+v", rd)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m wire.Msg
	if err := c.ReadJSON(&m); err == nil {
		t.Fatalf("replaced connection still open: %+v", m)
	}
	// An ID locked by wrong codes is never resumed.
	e.srv.hub.mu.Lock()
	e.srv.hub.locked[id] = time.Now().Add(time.Hour)
	e.srv.hub.mu.Unlock()
	d.Close()
	waitFor(t, func() bool { return e.reserved(id) })
	if _, rl := rawRegister(t, e, id, rd.Resume); rl.ID == id {
		t.Fatal("locked ID resumed")
	}

	// Resume off: no secret handed out.
	off := newEnv(t, func(c *Config) { c.ResumeGrace = -1 })
	if _, r := rawRegister(t, off, "", ""); r.Resume != "" || r.TTL != 0 {
		t.Fatalf("resume off: %+v", r)
	}
}

func TestRotateCode(t *testing.T) {
	e := newEnv(t, nil)
	release := make(chan struct{})
	hr := startHost(t, e, loopback, func(ctx context.Context, _ sl.ApprovalRequest) bool {
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	}, nil)
	id, code := hr.h.ID(), hr.h.Code()
	nc := hr.h.RotateCode()
	if !wire.ValidCode(nc) || nc == code || hr.h.Code() != nc || hr.h.ID() != id {
		t.Fatalf("rotate: %q -> %q (id %s -> %s)", code, nc, id, hr.h.ID())
	}
	if c, n := hr.lastCreds(); n != 1 || c.Reason != sl.ReasonManual || c.Code != nc || c.ID != id {
		t.Fatalf("credentials %+v", c)
	}
	// Refused while an approval is pending.
	hc := make(chan *sl.Session, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, _ := hr.h.WaitForOperator(ctx)
		hc <- s
	}()
	oc := make(chan *sl.Session, 1)
	go func() {
		s, _ := connect(t, opConfig(e, loopback, nil), id, nc)
		oc <- s
	}()
	waitFor(t, func() bool { hr.mu.Lock(); defer hr.mu.Unlock(); return len(hr.reqs) == 1 })
	if got := hr.h.RotateCode(); got != "" {
		t.Fatalf("rotated during an approval: %q", got)
	}
	close(release)
	os, hs := <-oc, <-hc
	if os == nil || hs == nil {
		t.Fatal("session not established")
	}
	// Refused during the session.
	if got := hr.h.RotateCode(); got != "" {
		t.Fatalf("rotated during a session: %q", got)
	}
	os.Close()
	<-hs.Done()
	waitFor(t, func() bool { return hr.h.RotateCode() != "" })
	hr.h.Close()
	if hr.h.RotateCode() != "" {
		t.Fatal("rotated after close")
	}
}

// TestHostResumeDuringSession: the server ends the session of a host whose
// signalling dropped; the host closes its side and comes back with the same
// ID.
func TestHostResumeDuringSession(t *testing.T) {
	e := newEnv(t, nil)
	hr, hs, os, _, _ := pair(t, e, loopback, loopback)
	id := hr.h.ID()
	dropHost(t, e, id)
	for _, s := range []*sl.Session{hs, os} {
		select {
		case <-s.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("session survived the host's signalling drop")
		}
	}
	waitFor(t, func() bool { return hr.h.State() == sl.HostOnline })
	if hr.h.ID() != id {
		t.Fatal("ID changed")
	}
	// The code was renewed at acceptance (used), or again if the drop cut
	// the session before it closed (failed): never the one that was used.
	if c, _ := hr.lastCreds(); (c.Reason != sl.ReasonUsed && c.Reason != sl.ReasonFailed) || c.Code != hr.h.Code() {
		t.Fatalf("code after a cut session: %+v", c)
	}
	if hr.h.RotateCode() == "" {
		t.Fatal("host still busy after the cut session")
	}
}
