package main

// reach_tunnels.go — the password vault's session registry and WebSocket
// frame injector. Portable: it only manipulates already-masked
// WebSocket frame bytes and never depends on a Windows API, so it is fully
// unit-tested with an in-memory pipe standing in for the real upstream
// connection (reach_tunnels_test.go).
//
// Two pieces:
//   - reachTunnels: a small registry, keyed by the ObliReach session token,
//     of which (origin, tenant) a live session belongs to and how to inject
//     a frame into its client→server direction.
//   - wsInjector + relayClientFrames: proxy.go's handleWSProxy normally just
//     io.Copy's the client→upstream direction of a WebSocket. For an
//     ObliReach tunnel, it instead runs relayClientFrames, which forwards
//     frames exactly as they arrive AND lets the vault splice in one
//     complete `type_text` frame between two of the page's own messages —
//     without ever unmasking or inspecting their payload.

import (
	"bufio"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	reachTunnelMax = 256
	reachTunnelTTL = 12 * time.Hour
)

// reachTunnel is one registered ObliReach session.
type reachTunnel struct {
	origin     string
	tenant     int64 // 0 = unknown (typing is refused with 'scope_unknown')
	deviceID   int64
	deviceUUID string // "" until the server's answer carries it (deviceUuid)
	vm         bool   // a Hyper-V VM console (protocol 'vmconsole'): typed into its own way (vaultTypeVm)
	vmID       string // that VM's GUID (lower case) from the server's answer; "" when it carried no valid one
	created    time.Time
	inj        *wsInjector
}

// vaultSession is the vault's view of this session's scope (vault.go
// VaultSession): server origin, device tenant, device id / uuid — all from
// the server's own answer.
func (t reachTunnel) vaultSession() VaultSession {
	return VaultSession{Origin: t.origin, TenantID: uint32(t.tenant), DeviceID: t.deviceID, DeviceUUID: t.deviceUUID, VM: t.vm, VMID: t.vmID}
}

// reachTunnels is the process-wide registry, filled by proxy.go's capture of
// POST /api/remote/sessions responses and consumed by the vault bindings.
type reachTunnels struct {
	mu      sync.Mutex
	byToken map[string]*reachTunnel
}

func newReachTunnels() *reachTunnels { return &reachTunnels{byToken: map[string]*reachTunnel{}} }

// register records a session's scope before its WebSocket is even opened
// (the POST answer arrives first). Evicts the oldest entries past
// reachTunnelMax or reachTunnelTTL so a registry left running for days does
// not grow without bound (reconnects and abandoned tabs both leave entries
// behind — there is no explicit unregister on tab close in this pass).
func (t *reachTunnels) register(token, origin string, tenant, deviceID int64) {
	t.registerDevice(token, origin, tenant, deviceID, "")
}

// registerDevice is register with the device's uuid too (a server whose
// session answer carries deviceUuid; "" otherwise).
func (t *reachTunnels) registerDevice(token, origin string, tenant, deviceID int64, deviceUUID string) {
	t.registerSession(token, origin, tenant, deviceID, deviceUUID, false)
}

// registerSession is registerDevice for an ObliReach session (vm false) or a
// Hyper-V VM console (vm true: the device is its host).
func (t *reachTunnels) registerSession(token, origin string, tenant, deviceID int64, deviceUUID string, vm bool) {
	t.registerTunnel(token, origin, tenant, deviceID, deviceUUID, vm, "")
}

// registerVMConsole registers a Hyper-V VM console with its VM's GUID (lower
// case, already validated by the caller; "" when the server's answer carried
// no valid one: then only the vault's unrestricted entries are offered).
func (t *reachTunnels) registerVMConsole(token, origin string, tenant, deviceID int64, deviceUUID, vmID string) {
	t.registerTunnel(token, origin, tenant, deviceID, deviceUUID, true, vmID)
}

func (t *reachTunnels) registerTunnel(token, origin string, tenant, deviceID int64, deviceUUID string, vm bool, vmID string) {
	if token == "" {
		return
	}
	if !vm {
		vmID = ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.evictLocked()
	t.byToken[token] = &reachTunnel{origin: origin, tenant: tenant, deviceID: deviceID, deviceUUID: deviceUUID, vm: vm, vmID: vmID, created: time.Now()}
}

func (t *reachTunnels) evictLocked() {
	now := time.Now()
	for k, v := range t.byToken {
		if now.Sub(v.created) > reachTunnelTTL {
			delete(t.byToken, k)
		}
	}
	if len(t.byToken) < reachTunnelMax {
		return
	}
	// Drop the oldest single entry — simple and rare (only hit under sustained
	// long-lived-token churn well past normal operator use).
	var oldestKey string
	var oldestAt time.Time
	for k, v := range t.byToken {
		if oldestKey == "" || v.created.Before(oldestAt) {
			oldestKey, oldestAt = k, v.created
		}
	}
	if oldestKey != "" {
		delete(t.byToken, oldestKey)
	}
}

// attach binds the live WS injector for token, once handleWSProxy has
// completed the upgrade. Returns a detach func the caller defers.
func (t *reachTunnels) attach(token string, inj *wsInjector) func() {
	t.mu.Lock()
	tun := t.byToken[token]
	if tun != nil {
		tun.inj = inj
	}
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		if tun != nil && tun.inj == inj {
			tun.inj = nil
		}
		t.mu.Unlock()
	}
}

// lookup returns the scope and live injector for token, or ok=false when the
// token is unknown (never registered, or evicted).
func (t *reachTunnels) lookup(token string) (tun reachTunnel, inj *wsInjector, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := t.byToken[token]
	if v == nil {
		return reachTunnel{}, nil, false
	}
	return *v, v.inj, true
}

// ---------------------------------------------------------------------------
// wsInjector: splices one masked WebSocket frame into the client→server
// direction of a live tunnel, between two complete client messages.
// ---------------------------------------------------------------------------

var (
	errInjectorBusy     = errors.New("busy")
	errInjectorClosed   = errors.New("closed")
	errInjectorDisabled = errors.New("unavailable")
)

// wsInjector serializes writes to dst between relayClientFrames (forwarding
// the page's own frames) and Inject (splicing a vault frame at a boundary).
type wsInjector struct {
	mu       sync.Mutex
	dst      io.Writer
	midMsg   bool // a client data message (opcode 1/2) is between its first and final fragment
	closed   bool // the connection ended (EOF, error, or a close frame)
	disabled bool // an extension (e.g. permessage-deflate) or a malformed/unmasked frame was seen: never inject, just keep forwarding

	// nowFn / sleepFn: overridable in tests so Inject's 3s wait is not a real
	// 3 seconds in the test suite.
	nowFn   func() time.Time
	sleepFn func(time.Duration)
}

func newWSInjector(dst io.Writer) *wsInjector {
	return &wsInjector{dst: dst, nowFn: time.Now, sleepFn: time.Sleep}
}

const injectWaitTimeout = 3 * time.Second
const injectPollInterval = 5 * time.Millisecond

// Inject writes one complete masked WebSocket text/binary frame carrying
// payload, waiting for a message boundary first (never splitting a client
// message in two). payload is wiped by the caller; the frame itself is built
// in a fresh buffer that Inject wipes before returning.
func (w *wsInjector) Inject(payload []byte) error {
	deadline := w.nowFn().Add(injectWaitTimeout)
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return errInjectorClosed
		}
		if w.disabled {
			w.mu.Unlock()
			return errInjectorDisabled
		}
		if !w.midMsg {
			break // still holding the lock: fall through to write
		}
		w.mu.Unlock()
		if w.nowFn().After(deadline) {
			return errInjectorBusy
		}
		w.sleepFn(injectPollInterval)
	}
	defer w.mu.Unlock()
	frame, err := buildMaskedFrame(0x1, payload) // opcode 1 = text
	if err != nil {
		return err
	}
	defer wipeBytesLocal(frame)
	_, err = w.dst.Write(frame)
	return err
}

// buildMaskedFrame builds a single-frame (FIN=1), masked WebSocket frame —
// exactly the shape a real browser client sends, so the server cannot tell
// an injected frame from one the page sent itself.
func buildMaskedFrame(opcode byte, payload []byte) ([]byte, error) {
	n := len(payload)
	var header []byte
	switch {
	case n < 126:
		header = []byte{0x80 | opcode, 0x80 | byte(n)}
	case n <= 0xffff:
		header = []byte{0x80 | opcode, 0x80 | 126, byte(n >> 8), byte(n)}
	default:
		header = []byte{0x80 | opcode, 0x80 | 127, 0, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(header)+4+n)
	out = append(out, header...)
	out = append(out, mask...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out, nil
}

// relayClientFrames forwards the client's own WebSocket frames from src to
// inj's destination byte-for-byte (payload stays masked, never unmasked or
// inspected), while tracking message boundaries in inj so Inject knows when
// it may safely splice a frame in. It returns when src reaches EOF, a
// read/write error occurs, or a close frame (opcode 8) is forwarded.
func relayClientFrames(src *bufio.Reader, inj *wsInjector) error {
	defer func() {
		inj.mu.Lock()
		inj.closed = true
		inj.mu.Unlock()
	}()
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(src, hdr[:]); err != nil {
			return err
		}
		fin := hdr[0]&0x80 != 0
		opcode := hdr[0] & 0x0f
		masked := hdr[1]&0x80 != 0
		length := int64(hdr[1] & 0x7f)

		toWrite := append([]byte(nil), hdr[:]...)

		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(src, ext[:]); err != nil {
				return err
			}
			toWrite = append(toWrite, ext[:]...)
			length = int64(ext[0])<<8 | int64(ext[1])
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(src, ext[:]); err != nil {
				return err
			}
			toWrite = append(toWrite, ext[:]...)
			length = 0
			for _, b := range ext {
				length = length<<8 | int64(b)
			}
		}

		rsv := hdr[0] & 0x70
		if rsv != 0 || !masked {
			// An extension (e.g. permessage-deflate) or a non-conforming
			// client frame: stop reasoning about message boundaries and
			// permanently refuse to inject on this connection, but keep
			// relaying bytes so the session itself is unaffected.
			inj.mu.Lock()
			inj.disabled = true
			inj.mu.Unlock()
		}

		if masked {
			var maskKey [4]byte
			if _, err := io.ReadFull(src, maskKey[:]); err != nil {
				return err
			}
			toWrite = append(toWrite, maskKey[:]...)
		}

		inj.mu.Lock()
		if _, err := inj.dst.Write(toWrite); err != nil {
			inj.mu.Unlock()
			return err
		}
		if length > 0 {
			if _, err := io.CopyN(inj.dst, src, length); err != nil {
				inj.mu.Unlock()
				return err
			}
		}
		switch {
		case opcode == 0x8: // close
			inj.closed = true
			inj.mu.Unlock()
			return nil
		case opcode == 0x1 || opcode == 0x2: // text / binary start
			inj.midMsg = !fin
		case opcode == 0x0: // continuation
			if fin {
				inj.midMsg = false
			}
		}
		// Control frames 0x9 (ping) / 0xA (pong) do not change midMsg — they
		// may legally appear between fragments of a data message.
		inj.mu.Unlock()
	}
}
