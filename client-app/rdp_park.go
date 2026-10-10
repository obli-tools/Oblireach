package main

// rdp_park.go — a local RDP session moving to another window (its tab torn
// off or merged: rdp_view.go, terminal_viewer.go toolTearOff), like a
// terminal's parked shell (term_park.go): its window parks it
// (reachRdpPark: its picture hidden, held by the main window) under a
// one-time token, the new window's page takes it over (reachRdpAttach: the
// picture placed in that window, its events told there). A token never taken
// over in time ends the session (the remote session is not left running
// unseen).

import (
	"errors"
	"sync"
	"time"
)

const (
	rdpParkTTL = 30 * time.Second
	rdpParkMax = 16
)

var errRdpParkFull = errors.New("rdp: too many sessions moving")

type rdpParkReg struct {
	mu     sync.Mutex
	byTok  map[string]*rdpParked
	ttl    time.Duration
	after  func(d time.Duration, f func()) func() bool
	newTok func() string
}

type rdpParked struct {
	connID string
	stop   func() bool
}

func newRdpParkReg() *rdpParkReg {
	return &rdpParkReg{byTok: map[string]*rdpParked{}, ttl: rdpParkTTL,
		after:  func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		newTok: randomToken}
}

var appRdpParks = newRdpParkReg()

// park: connID's one-time token; expire(connID) runs if no window takes it
// over in time.
func (r *rdpParkReg) park(connID string, expire func(connID string)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.byTok {
		if p.connID == connID {
			return "", errors.New("rdp: already moving")
		}
	}
	if len(r.byTok) >= rdpParkMax {
		return "", errRdpParkFull
	}
	tok := r.newTok()
	p := &rdpParked{connID: connID}
	p.stop = r.after(r.ttl, func() {
		r.mu.Lock()
		if r.byTok[tok] != p {
			r.mu.Unlock()
			return
		}
		delete(r.byTok, tok)
		r.mu.Unlock()
		if expire != nil {
			expire(connID)
		}
	})
	r.byTok[tok] = p
	return tok, nil
}

// take consumes a token: its connection id ("" when unknown or expired).
func (r *rdpParkReg) take(tok string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.byTok[tok]
	if p == nil {
		return ""
	}
	delete(r.byTok, tok)
	if p.stop != nil {
		p.stop()
	}
	return p.connID
}

// dropConn: the session ended while parked (its token goes).
func (r *rdpParkReg) dropConn(connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for tok, p := range r.byTok {
		if p.connID == connID {
			delete(r.byTok, tok)
			if p.stop != nil {
				p.stop()
			}
		}
	}
}

func (r *rdpParkReg) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byTok)
}
