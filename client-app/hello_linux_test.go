//go:build linux

package main

// hello_linux_test.go — the fingerprint unlock against a FAKE fprintd on a
// private bus (dbusfake_linux_test.go's dbus-daemon; skipped without it).

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const fakeFprintDev = dbus.ObjectPath("/net/reactivated/Fprint/Device/0")

type fakeFprint struct {
	conn *dbus.Conn

	mu        sync.Mutex
	noDevice  bool
	fingers   []string
	claimErr  string   // Claim's error name suffix ("" = ok)
	results   []string // what each VerifyStart reports, in order ("result,done")
	starts    int
	stops     int
	claimed   bool
	released  int
	listUsers []string
}

func startFakeFprint(t *testing.T) *fakeFprint {
	t.Helper()
	b := startTestBus(t)
	c := b.mustConnect(t)
	f := &fakeFprint{conn: c, fingers: []string{"right-index-finger"}}
	c.ExportMethodTable(map[string]interface{}{
		"GetDefaultDevice": func() (dbus.ObjectPath, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.noDevice {
				return "", &dbus.Error{Name: fprintErrPrefix + "NoSuchDevice", Body: []interface{}{"none"}}
			}
			return fakeFprintDev, nil
		},
	}, fprintManagerPath, fprintManagerIf)
	c.ExportMethodTable(map[string]interface{}{
		"ListEnrolledFingers": func(u string) ([]string, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.listUsers = append(f.listUsers, u)
			if len(f.fingers) == 0 {
				return nil, &dbus.Error{Name: fprintErrPrefix + "NoEnrolledPrints", Body: []interface{}{"none"}}
			}
			return f.fingers, nil
		},
		"Claim": func(u string) *dbus.Error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.claimErr != "" {
				return &dbus.Error{Name: fprintErrPrefix + f.claimErr, Body: []interface{}{"no"}}
			}
			f.claimed = true
			return nil
		},
		"Release": func() *dbus.Error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.claimed = false
			f.released++
			return nil
		},
		"VerifyStart": func(finger string) *dbus.Error {
			f.mu.Lock()
			i := f.starts
			f.starts++
			var r string
			if i < len(f.results) {
				r = f.results[i]
			}
			f.mu.Unlock()
			if r == "" {
				return nil // (no answer: the prompt times out)
			}
			go func() {
				time.Sleep(20 * time.Millisecond)
				for _, one := range splitResults(r) {
					f.conn.Emit(fakeFprintDev, fprintDeviceIf+".VerifyStatus", one.result, one.done)
				}
			}()
			return nil
		},
		"VerifyStop": func() *dbus.Error {
			f.mu.Lock()
			f.stops++
			f.mu.Unlock()
			return nil
		},
	}, fakeFprintDev, fprintDeviceIf)
	ownName(t, c, fprintBusName)
	old := fprintDial
	fprintDial = func() (*dbus.Conn, error) { return b.connect() }
	t.Cleanup(func() { fprintDial = old })
	return f
}

type fpResult struct {
	result string
	done   bool
}

// splitResults: "verify-retry-scan;verify-match!" (! = done).
func splitResults(s string) []fpResult {
	var out []fpResult
	cur := ""
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ';' {
			if cur != "" {
				r := fpResult{result: cur}
				if cur[len(cur)-1] == '!' {
					r.result, r.done = cur[:len(cur)-1], true
				}
				out = append(out, r)
			}
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	return out
}

func TestFprintAvailable(t *testing.T) {
	f := startFakeFprint(t)
	p := linuxPresence{}
	if ok, err := p.available(); !ok || err != nil {
		t.Fatalf("available %v %v", ok, err)
	}
	if f.listUsers[0] != fprintUser() {
		t.Fatalf("listed for %q", f.listUsers[0])
	}
	f.mu.Lock()
	f.fingers = nil
	f.mu.Unlock()
	if ok, _ := p.available(); ok {
		t.Fatal("available without an enrolled finger")
	}
	f.mu.Lock()
	f.noDevice = true
	f.mu.Unlock()
	if ok, _ := p.available(); ok {
		t.Fatal("available without a reader")
	}
	// No fprintd at all (no system bus here): unavailable, no error.
	fprintDial = func() (*dbus.Conn, error) { return nil, errors.New("no bus") }
	if ok, err := p.available(); ok || err != nil {
		t.Fatalf("no bus: %v %v", ok, err)
	}
	if r, err := p.verify(1, "x", time.Second); r != helloDeviceNotPresent || err != nil {
		t.Fatalf("no bus verify: %d %v", r, err)
	}
	if p.shown(1) {
		t.Fatal("shown without a UI host")
	}
}

func TestFprintVerify(t *testing.T) {
	f := startFakeFprint(t)
	p := linuxPresence{}
	// A bad scan, then a match: verified; the reader let go.
	f.results = []string{"verify-retry-scan;verify-match!"}
	if r, err := p.verify(1, "x", 5*time.Second); r != helloVerified || err != nil {
		t.Fatalf("match: %d %v", r, err)
	}
	if f.claimed || f.released != 1 || f.stops == 0 {
		t.Fatalf("reader not released: %+v", f)
	}
	// Three wrong fingers: retries exhausted.
	f.starts, f.results = 0, []string{"verify-no-match!", "verify-no-match!", "verify-no-match!"}
	if r, err := p.verify(1, "x", 5*time.Second); r != helloRetriesExhausted || err != nil || f.starts != 3 {
		t.Fatalf("no match: %d %v starts=%d", r, err, f.starts)
	}
	// One miss, then a match.
	f.starts, f.results = 0, []string{"verify-no-match!", "verify-match!"}
	if r, _ := p.verify(1, "x", 5*time.Second); r != helloVerified {
		t.Fatalf("miss then match: %d", r)
	}
	// The reader unplugged.
	f.starts, f.results = 0, []string{"verify-disconnected!"}
	if r, _ := p.verify(1, "x", 5*time.Second); r != helloDeviceNotPresent {
		t.Fatalf("disconnected: %d", r)
	}
	// Nobody touches it: timeout (errHelloTimeout: helloVerify says cancelled).
	f.starts, f.results = 0, nil
	if _, err := p.verify(1, "x", 300*time.Millisecond); !errors.Is(err, errHelloTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	if f.claimed {
		t.Fatal("still claimed after the timeout")
	}
	// The reader held elsewhere (the lock screen, sudo): busy; refused by polkit.
	f.claimErr = "AlreadyInUse"
	if r, _ := p.verify(1, "x", time.Second); r != helloDeviceBusy {
		t.Fatalf("busy: %d", r)
	}
	f.claimErr = "PermissionDenied"
	if r, _ := p.verify(1, "x", time.Second); r != helloDisabledByPolicy {
		t.Fatalf("denied: %d", r)
	}
	// Through the vault's mapping (vault_hello.go helloVerify).
	f.claimErr, f.starts, f.results = "", 0, []string{"verify-match!"}
	oldV := helloVerifyFn
	helloVerifyFn = p.verify
	defer func() { helloVerifyFn = oldV }()
	if err := helloVerify(1, "unlock"); err != nil {
		t.Fatalf("helloVerify: %v", err)
	}
	f.starts, f.results = 0, []string{"verify-no-match!", "verify-no-match!", "verify-no-match!"}
	if err := helloVerify(1, "unlock"); !errors.Is(err, errHelloRetries) {
		t.Fatalf("helloVerify retries: %v", err)
	}
}
