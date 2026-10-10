//go:build linux

package main

// dbusfake_linux_test.go — a private dbus-daemon per test and fakes of the
// services the Linux platform talks to: the Secret Service
// (org.freedesktop.secrets) and the desktop portal
// (org.freedesktop.portal.Desktop: FileChooser, OpenURI, Notification,
// Background). The tests are skipped where dbus-daemon is not installed.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// testBus: a private session bus; dbusDial points at it for the test.
type testBus struct {
	addr string
	cmd  *exec.Cmd
}

func startTestBus(t *testing.T) *testBus {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=`+filepath.Join(dir, "bus")+`</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600)
	cmd := exec.Command(daemon, "--config-file="+conf, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cmd.Process.Kill()
		t.Fatalf("dbus-daemon: %v", err)
	}
	b := &testBus{addr: strings.TrimSpace(line), cmd: cmd}
	resetSessionBus()
	old := dbusDial
	dbusDial = func() (*dbus.Conn, error) { return b.connect() }
	t.Cleanup(func() {
		resetSessionBus()
		dbusDial = old
		cmd.Process.Kill()
		cmd.Wait()
	})
	return b
}

func (b *testBus) connect() (*dbus.Conn, error) { return dbus.Connect(b.addr) }

func (b *testBus) mustConnect(t *testing.T) *dbus.Conn {
	t.Helper()
	c, err := b.connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ownName(t *testing.T, c *dbus.Conn, name string) {
	t.Helper()
	r, err := c.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", name, r, err)
	}
}

// fakeProps: org.freedesktop.DBus.Properties.Get of an object.
type fakeProps struct {
	get func(iface, name string) (dbus.Variant, bool)
}

func (p fakeProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if v, ok := p.get(iface, name); ok {
		return v, nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %s.%s", iface, name))
}

// ── Fake Secret Service ─────────────────────────────────────────────────────

type fakeSSItem struct {
	attrs  map[string]string
	secret []byte
	label  string
}

type fakeSS struct {
	t    *testing.T
	conn *dbus.Conn

	mu         sync.Mutex
	items      map[dbus.ObjectPath]*fakeSSItem
	n          int
	plainOnly  bool                       // OpenSession refuses DH
	sessions   map[dbus.ObjectPath][]byte // path -> AES key (nil: plain)
	locked     bool                       // the collection is locked
	dismiss    bool                       // its unlock prompt is dismissed
	noDefault  bool                       // no "default" alias until created
	prompts    int                        // prompts shown
	algos      []string                   // OpenSession algorithms asked
	collection dbus.ObjectPath
}

const fakeCollPath = dbus.ObjectPath("/org/freedesktop/secrets/collection/login")

func startFakeSS(t *testing.T, b *testBus) *fakeSS {
	t.Helper()
	c := b.mustConnect(t)
	f := &fakeSS{t: t, conn: c, items: map[dbus.ObjectPath]*fakeSSItem{}, sessions: map[dbus.ObjectPath][]byte{}, collection: fakeCollPath}
	c.ExportMethodTable(map[string]interface{}{
		"OpenSession":      f.openSession,
		"SearchItems":      f.searchItems,
		"Unlock":           f.unlock,
		"ReadAlias":        f.readAlias,
		"CreateCollection": f.createCollection,
	}, ssPath, ssIface)
	c.ExportMethodTable(map[string]interface{}{"CreateItem": f.createItem}, fakeCollPath, ssCollIface)
	c.Export(fakeProps{get: func(iface, name string) (dbus.Variant, bool) {
		if name == "Locked" {
			f.mu.Lock()
			defer f.mu.Unlock()
			return dbus.MakeVariant(f.locked), true
		}
		return dbus.Variant{}, false
	}}, fakeCollPath, "org.freedesktop.DBus.Properties")
	ownName(t, c, ssBusName)
	return f
}

func (f *fakeSS) openSession(algo string, in dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.algos = append(f.algos, algo)
	f.n++
	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/secrets/session/%d", f.n))
	switch {
	case algo == ssAlgoPlain:
		f.sessions[path] = nil
		f.conn.ExportMethodTable(map[string]interface{}{"Close": func() *dbus.Error { return nil }}, path, "org.freedesktop.Secret.Session")
		return dbus.MakeVariant(""), path, nil
	case algo == ssAlgoDH && !f.plainOnly:
		peer, _ := in.Value().([]byte)
		priv, pub, err := ssDHKeyPair(cryptoRand{})
		if err != nil {
			return dbus.Variant{}, "", dbus.MakeFailedError(err)
		}
		key, err := ssDHAESKey(priv, peer)
		if err != nil {
			return dbus.Variant{}, "", dbus.MakeFailedError(err)
		}
		f.sessions[path] = key
		f.conn.ExportMethodTable(map[string]interface{}{"Close": func() *dbus.Error { return nil }}, path, "org.freedesktop.Secret.Session")
		return dbus.MakeVariant(pub), path, nil
	}
	return dbus.Variant{}, "", &dbus.Error{Name: "org.freedesktop.DBus.Error.NotSupported", Body: []interface{}{"algorithm"}}
}

func (f *fakeSS) match(attrs map[string]string) []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for p, it := range f.items {
		ok := true
		for k, v := range attrs {
			if it.attrs[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeSS) searchItems(attrs map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.match(attrs)
	if f.locked {
		return []dbus.ObjectPath{}, append([]dbus.ObjectPath{}, m...), nil
	}
	return append([]dbus.ObjectPath{}, m...), []dbus.ObjectPath{}, nil
}

// newPrompt: a prompt that, once shown, completes (dismissed or not) and
// runs done when accepted.
func (f *fakeSS) newPrompt(result func() dbus.Variant, done func()) dbus.ObjectPath {
	f.n++
	p := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/secrets/prompt/%d", f.n))
	f.conn.ExportMethodTable(map[string]interface{}{
		"Prompt": func(window string) *dbus.Error {
			f.mu.Lock()
			f.prompts++
			dismiss := f.dismiss
			f.mu.Unlock()
			go func() {
				time.Sleep(20 * time.Millisecond)
				res := dbus.MakeVariant("")
				if !dismiss {
					done()
					res = result()
				}
				f.conn.Emit(p, ssPromptIface+".Completed", dismiss, res)
			}()
			return nil
		},
		"Dismiss": func() *dbus.Error { return nil },
	}, p, ssPromptIface)
	return p
}

func (f *fakeSS) unlock(objs []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.locked {
		return objs, ssNoPath, nil
	}
	p := f.newPrompt(func() dbus.Variant { return dbus.MakeVariant(objs) }, func() {
		f.mu.Lock()
		f.locked = false
		f.mu.Unlock()
	})
	return []dbus.ObjectPath{}, p, nil
}

func (f *fakeSS) readAlias(name string) (dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "default" || f.noDefault {
		return ssNoPath, nil
	}
	return f.collection, nil
}

func (f *fakeSS) createCollection(props map[string]dbus.Variant, alias string) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.newPrompt(func() dbus.Variant { return dbus.MakeVariant(fakeCollPath) }, func() {
		f.mu.Lock()
		f.noDefault = false
		f.mu.Unlock()
	})
	return ssNoPath, p, nil
}

func (f *fakeSS) createItem(props map[string]dbus.Variant, sec ssSecret, replace bool) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locked {
		return "", "", &dbus.Error{Name: "org.freedesktop.Secret.Error.IsLocked", Body: []interface{}{"locked"}}
	}
	key, ok := f.sessions[sec.Session]
	if !ok {
		return "", "", &dbus.Error{Name: "org.freedesktop.Secret.Error.NoSession", Body: []interface{}{"session"}}
	}
	plain, err := (&ssSession{path: sec.Session, aes: key}).decode(sec)
	if err != nil {
		return "", "", dbus.MakeFailedError(err)
	}
	attrs, _ := props[ssItemIface+".Attributes"].Value().(map[string]string)
	label, _ := props[ssItemIface+".Label"].Value().(string)
	f.n++
	path := dbus.ObjectPath(fmt.Sprintf("%s/%d", fakeCollPath, f.n))
	it := &fakeSSItem{attrs: attrs, secret: plain, label: label}
	f.items[path] = it
	f.conn.ExportMethodTable(map[string]interface{}{
		"GetSecret": func(session dbus.ObjectPath) (ssSecret, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.locked {
				return ssSecret{}, &dbus.Error{Name: "org.freedesktop.Secret.Error.IsLocked", Body: []interface{}{"locked"}}
			}
			k, ok := f.sessions[session]
			if !ok {
				return ssSecret{}, &dbus.Error{Name: "org.freedesktop.Secret.Error.NoSession", Body: []interface{}{"session"}}
			}
			s, err := (&ssSession{path: session, aes: k}).encode(it.secret, cryptoRand{})
			if err != nil {
				return ssSecret{}, dbus.MakeFailedError(err)
			}
			return s, nil
		},
		"Delete": func() (dbus.ObjectPath, *dbus.Error) {
			f.mu.Lock()
			delete(f.items, path)
			f.mu.Unlock()
			return ssNoPath, nil
		},
	}, path, ssItemIface)
	f.conn.Export(fakeProps{get: func(iface, name string) (dbus.Variant, bool) {
		if name == "Attributes" {
			return dbus.MakeVariant(it.attrs), true
		}
		return dbus.Variant{}, false
	}}, path, "org.freedesktop.DBus.Properties")
	return path, ssNoPath, nil
}

func (f *fakeSS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

// ── Fake portal ─────────────────────────────────────────────────────────────

type portalCallRec struct {
	iface, method string
	parent        string
	title         string
	fdPath        string
	options       map[string]dbus.Variant
	id            string
	notification  map[string]dbus.Variant
}

type fakePortal struct {
	t    *testing.T
	conn *dbus.Conn

	mu       sync.Mutex
	calls    []portalCallRec
	response uint32                  // the Response code to send
	results  map[string]dbus.Variant // its results
	silent   bool                    // never answer
}

func startFakePortal(t *testing.T, b *testBus) *fakePortal {
	t.Helper()
	c := b.mustConnect(t)
	p := &fakePortal{t: t, conn: c, results: map[string]dbus.Variant{}}
	c.ExportMethodTable(map[string]interface{}{
		"OpenFile": func(sender dbus.Sender, parent, title string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			return p.request(sender, portalCallRec{iface: portalFileChooser, method: "OpenFile", parent: parent, title: title, options: opts})
		},
		"SaveFile": func(sender dbus.Sender, parent, title string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			return p.request(sender, portalCallRec{iface: portalFileChooser, method: "SaveFile", parent: parent, title: title, options: opts})
		},
	}, portalPath, portalFileChooser)
	c.ExportMethodTable(map[string]interface{}{
		"OpenFile": func(sender dbus.Sender, parent string, fd dbus.UnixFD, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			return p.request(sender, portalCallRec{iface: portalOpenURI, method: "OpenFile", parent: parent, fdPath: fdTarget(fd), options: opts})
		},
		"OpenDirectory": func(sender dbus.Sender, parent string, fd dbus.UnixFD, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			return p.request(sender, portalCallRec{iface: portalOpenURI, method: "OpenDirectory", parent: parent, fdPath: fdTarget(fd), options: opts})
		},
	}, portalPath, portalOpenURI)
	c.ExportMethodTable(map[string]interface{}{
		"AddNotification": func(id string, n map[string]dbus.Variant) *dbus.Error {
			p.mu.Lock()
			p.calls = append(p.calls, portalCallRec{iface: portalNotification, method: "AddNotification", id: id, notification: n})
			p.mu.Unlock()
			return nil
		},
		"RemoveNotification": func(id string) *dbus.Error { return nil },
	}, portalPath, portalNotification)
	c.ExportMethodTable(map[string]interface{}{
		"RequestBackground": func(sender dbus.Sender, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			return p.request(sender, portalCallRec{iface: portalBackground, method: "RequestBackground", parent: parent, options: opts})
		},
	}, portalPath, portalBackground)
	ownName(t, c, portalBusName)
	return p
}

// fdTarget: what a received descriptor points at (then closed).
func fdTarget(fd dbus.UnixFD) string {
	s, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", int(fd)))
	syscallClose(int(fd))
	return s
}

func (p *fakePortal) request(sender dbus.Sender, rec portalCallRec) (dbus.ObjectPath, *dbus.Error) {
	token, _ := rec.options["handle_token"].Value().(string)
	s := strings.ReplaceAll(strings.TrimPrefix(string(sender), ":"), ".", "_")
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + s + "/" + token)
	p.mu.Lock()
	p.calls = append(p.calls, rec)
	code, res, silent := p.response, p.results, p.silent
	p.mu.Unlock()
	p.conn.ExportMethodTable(map[string]interface{}{"Close": func() *dbus.Error { return nil }}, path, portalRequest)
	if !silent {
		go func() {
			time.Sleep(10 * time.Millisecond)
			p.conn.Emit(path, portalRequest+".Response", code, res)
		}()
	}
	return path, nil
}

func (p *fakePortal) last() portalCallRec {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return portalCallRec{}
	}
	return p.calls[len(p.calls)-1]
}

func (p *fakePortal) answer(code uint32, results map[string]dbus.Variant) {
	p.mu.Lock()
	p.response, p.results = code, results
	p.mu.Unlock()
}
