//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

type fakeRunKey struct {
	value       string
	present     bool
	approvedVal []byte
	apPresent   bool
	sets        []string
	failSet     error
}

func (f *fakeRunKey) get() (string, bool, error) { return f.value, f.present, nil }
func (f *fakeRunKey) set(v string) error {
	if f.failSet != nil {
		return f.failSet
	}
	f.value, f.present = v, true
	f.sets = append(f.sets, v)
	return nil
}
func (f *fakeRunKey) remove() error                   { f.value, f.present = "", false; return nil }
func (f *fakeRunKey) approved() ([]byte, bool, error) { return f.approvedVal, f.apPresent, nil }
func (f *fakeRunKey) clearApproved() error            { f.approvedVal, f.apPresent = nil, false; return nil }

func newTestStartup(reg runKeyStore) (*startupCtl, *[2]bool) {
	prefs := &[2]bool{}
	return &startupCtl{
		exe:      `C:\Program Files\OblireachClient\ObliReach.exe`,
		reg:      reg,
		prefs:    func() (bool, bool) { return prefs[0], prefs[1] },
		setPrefs: func(m, a bool) { prefs[0], prefs[1] = m, a },
	}, prefs
}

func TestStartupSettings(t *testing.T) {
	reg := &fakeRunKey{}
	c, prefs := newTestStartup(reg)
	exeQ := `"C:\Program Files\OblireachClient\ObliReach.exe"`

	if st, err := c.state(); err != nil || st.RunAtLogin || st.DisabledInTM || st.OtherCopy != "" || st.Minimized {
		t.Fatalf("initial: %+v %v", st, err)
	}
	// On: the quoted path of this exe.
	st, err := c.set("runAtLogin", true)
	if err != nil || !st.RunAtLogin || reg.value != exeQ {
		t.Fatalf("on: %+v %v %q", st, err, reg.value)
	}
	// Start minimized: the Run value gets the flag; the preference is saved.
	st, _ = c.set("minimized", true)
	if !st.Minimized || !prefs[0] || reg.value != exeQ+" --minimized" {
		t.Fatalf("minimized: %+v %q", st, reg.value)
	}
	// Every launch: the preference only.
	nsets := len(reg.sets)
	st, _ = c.set("minimizedAlways", true)
	if !st.MinimizedAlways || !prefs[1] || len(reg.sets) != nsets {
		t.Fatalf("always: %+v", st)
	}
	// Off: the value goes.
	st, _ = c.set("runAtLogin", false)
	if st.RunAtLogin || reg.present {
		t.Fatalf("off: %+v", st)
	}
	// Minimized while off: no Run value appears.
	c.set("minimized", false)
	if reg.present {
		t.Fatal("a Run value appeared")
	}
	// On again, with the minimized choice.
	c.set("minimized", true)
	c.set("runAtLogin", true)
	if reg.value != exeQ+" --minimized" {
		t.Fatalf("on with minimized: %q", reg.value)
	}

	// Turned off in Task Manager: shown as off; on again here re-enables it.
	reg.approvedVal, reg.apPresent = []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, true
	if st, _ := c.state(); st.RunAtLogin || !st.DisabledInTM {
		t.Fatalf("disabled in Task Manager: %+v", st)
	}
	// ... changing "Start minimized" keeps Task Manager's choice.
	c.set("minimized", false)
	if !reg.apPresent || reg.value != exeQ {
		t.Fatalf("minimized off kept TM state: %v %q", reg.apPresent, reg.value)
	}
	if st, _ := c.set("runAtLogin", true); !st.RunAtLogin || reg.apPresent {
		t.Fatalf("re-enabled: %+v", st)
	}

	// Another copy registered: shown; its value is not rewritten by
	// "Start minimized"; "on" points it at this one.
	reg.value = `"D:\Old\ObliReach.exe" --minimized`
	if st, _ := c.state(); !st.RunAtLogin || st.OtherCopy != `D:\Old\ObliReach.exe` {
		t.Fatalf("other copy: %+v", st)
	}
	c.set("minimized", true)
	if reg.value != `"D:\Old\ObliReach.exe" --minimized` {
		t.Fatalf("another copy's value rewritten: %q", reg.value)
	}
	if st, _ := c.set("runAtLogin", true); st.OtherCopy != "" || reg.value != exeQ+" --minimized" {
		t.Fatalf("taken over: %+v %q", st, reg.value)
	}
	// Same exe, other case / slashes: this copy.
	reg.value = `"c:/program files/oblireachclient/OBLIREACH.EXE"`
	if st, _ := c.state(); st.OtherCopy != "" {
		t.Fatalf("same path, other spelling: %+v", st)
	}

	// Errors surface, with the state as it is.
	reg.failSet = errors.New("access denied")
	if _, err := c.set("runAtLogin", true); err == nil {
		t.Fatal("a failed write reported success")
	}
	if _, err := c.set("nope", true); err != errBadArgument {
		t.Fatalf("unknown choice: %v", err)
	}
}

func TestStartupBindings(t *testing.T) {
	b, _, _, _, _ := testMainBridge(t)
	if _, err := b.startupGet("x"); err != errNotAllowed {
		t.Fatal("get without token")
	}
	if _, err := b.startupSet("", "runAtLogin", true); err != errNotAllowed {
		t.Fatal("set without token")
	}
	if _, err := b.startupGet(b.token); err == nil {
		t.Fatal("no startup settings in this window: accepted")
	}
	reg := &fakeRunKey{}
	b.startup, _ = newTestStartup(reg)
	if st, err := b.startupSet(b.token, "runAtLogin", true); err != nil || !st.RunAtLogin || !strings.HasPrefix(reg.value, `"C:\Program Files\`) {
		t.Fatalf("set: %+v %v", st, err)
	}
	if st, err := b.startupGet(b.token); err != nil || !st.RunAtLogin || st.Exe == "" {
		t.Fatalf("get: %+v %v", st, err)
	}
}

// The real registry, under a throw-away key of HKCU (never the real Run key).
func TestRegRunKeyReal(t *testing.T) {
	id, _ := randomHandle()
	root := `Software\OblireachTest-` + id[:12]
	defer func() {
		p, _ := syscall.UTF16PtrFromString(root)
		modAdvapi32.NewProc("RegDeleteTreeW").Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(p)))
	}()
	r := regRunKey{runKey: root + `\Run`, approvedKey: root + `\Approved`, name: runValueName}
	if v, present, err := r.get(); err != nil || present || v != "" {
		t.Fatalf("empty: %q %v %v", v, present, err)
	}
	if err := r.remove(); err != nil {
		t.Fatalf("remove of nothing: %v", err)
	}
	want := `"C:\Program Files\Oblireach Client (é)\ObliReach.exe" --minimized`
	if err := r.set(want); err != nil {
		t.Fatal(err)
	}
	if v, present, err := r.get(); err != nil || !present || v != want {
		t.Fatalf("read back: %q %v %v", v, present, err)
	}
	if _, present, _ := r.approved(); present {
		t.Fatal("approved value out of nowhere")
	}
	disabled := []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if err := regWrite(r.approvedKey, r.name, regBinary, unsafe.Pointer(&disabled[0]), uint32(len(disabled))); err != nil {
		t.Fatal(err)
	}
	if data, present, err := r.approved(); err != nil || !present || startupApprovedEnabled(data, present) {
		t.Fatalf("approved: %v %v %v", data, present, err)
	}
	if err := r.clearApproved(); err != nil {
		t.Fatal(err)
	}
	if _, present, _ := r.approved(); present {
		t.Fatal("approved value kept")
	}
	if err := r.remove(); err != nil {
		t.Fatal(err)
	}
	if _, present, _ := r.get(); present {
		t.Fatal("Run value kept")
	}
}
