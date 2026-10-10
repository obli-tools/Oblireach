//go:build linux

package engine

// portalBus over the user's session D-Bus (godbus, pure Go). Inside a
// Flatpak the portal is on the session bus without any extra permission.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

type godbusPortalBus struct {
	conn  *dbus.Conn
	obj   dbus.BusObject
	sigCh chan *dbus.Signal

	mu   sync.Mutex
	next int
	subs map[int]*godbusSub
}

type godbusSub struct {
	path dbus.ObjectPath
	name string // iface.member
	ch   chan portalSignal
}

// dialPortalBus opens a private connection to the session bus (closed with
// the portal session).
func dialPortalBus() (portalBus, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoSessionBus, err)
	}
	if !conn.SupportsUnixFDs() {
		_ = conn.Close()
		return nil, errors.New("the session D-Bus connection cannot pass file descriptors (PipeWire needs one)")
	}
	b := &godbusPortalBus{
		conn:  conn,
		obj:   conn.Object(portalDest, portalPath),
		sigCh: make(chan *dbus.Signal, 64),
		subs:  map[int]*godbusSub{},
	}
	conn.Signal(b.sigCh)
	go b.dispatch()
	return b, nil
}

func (b *godbusPortalBus) dispatch() {
	for sig := range b.sigCh {
		b.mu.Lock()
		for _, s := range b.subs {
			if s.path == sig.Path && s.name == sig.Name {
				select {
				case s.ch <- portalSignal(sig.Body):
				default: // a subscriber reads one answer: never block the bus
				}
			}
		}
		b.mu.Unlock()
	}
}

func (b *godbusPortalBus) UniqueName() string {
	if n := b.conn.Names(); len(n) > 0 {
		return n[0]
	}
	return ""
}

func (b *godbusPortalBus) Call(ctx context.Context, iface, method string, args ...interface{}) ([]interface{}, error) {
	call := b.obj.CallWithContext(ctx, iface+"."+method, 0, args...)
	if call.Err != nil {
		return nil, call.Err
	}
	return call.Body, nil
}

func (b *godbusPortalBus) Send(iface, method string, args ...interface{}) error {
	return b.obj.Go(iface+"."+method, dbus.FlagNoReplyExpected, nil, args...).Err
}

func (b *godbusPortalBus) CallPath(ctx context.Context, path dbus.ObjectPath, iface, method string, args ...interface{}) error {
	return b.conn.Object(portalDest, path).CallWithContext(ctx, iface+"."+method, 0, args...).Err
}

func (b *godbusPortalBus) Property(iface, name string) (dbus.Variant, error) {
	return b.obj.GetProperty(iface + "." + name)
}

func (b *godbusPortalBus) Subscribe(path dbus.ObjectPath, iface, member string) (*portalSub, error) {
	opts := []dbus.MatchOption{
		dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(iface), dbus.WithMatchMember(member),
	}
	if err := b.conn.AddMatchSignal(opts...); err != nil {
		return nil, err
	}
	s := &godbusSub{path: path, name: iface + "." + member, ch: make(chan portalSignal, 4)}
	b.mu.Lock()
	b.next++
	id := b.next
	b.subs[id] = s
	b.mu.Unlock()
	var once sync.Once
	return &portalSub{C: s.ch, Cancel: func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			_ = b.conn.RemoveMatchSignal(opts...)
		})
	}}, nil
}

func (b *godbusPortalBus) Close() error {
	return b.conn.Close()
}
