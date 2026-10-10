package main

// vault_hello.go — the password vault's Windows Hello bindings
// (the model: vault.go UnlockWithPresence; the platform's verifier:
// presenceVerifier, platform.go — the WinRT calls: hello_windows.go).
//
//   reachVaultUnlockHello(tok, reqID)      every window: the prompt, parented
//                                          to THIS window, then the unlock
//   reachVaultUnlockHelloAuto(tok, reqID, promptID)
//                                          every window: the same, launched by
//                                          the page as a prompt opens, under
//                                          hello_auto.go's rules (else
//                                          "hello_skipped": the code field)
//   reachVaultHelloSet(tok, reqID, on)     main window: the switch (turning it
//                                          on asks Hello once, from an
//                                          unlocked vault)
//
// reachVaultState says whether Hello can be offered ({hello: {available,
// enabled}}): availability is Windows' own answer (a device, set up for this
// account, not disabled by policy), cached a little (helloAvailable).

import (
	"errors"
	"sync"
	"time"
)

// Test seams: the platform's presence verifier (platformPresence,
// platform.go: Windows Hello's WinRT calls, hello_windows.go), and whether a
// window is on screen in the foreground.
var (
	helloProbeFn  = platformPresence.available
	helloVerifyFn = platformPresence.verify
	helloShownFn  = platformPresence.shown
)

// One prompt at a time for the whole app, and the automatic tries
// (hello_auto.go).
var (
	helloOne  helloFlight
	helloAuto helloAutoBook
)

// The answers of presenceVerifier.verify (Windows'
// UserConsentVerificationResult values, which a platform maps its own
// verifier's answers to).
const (
	helloVerified         = 0
	helloDeviceNotPresent = 1
	helloNotConfigured    = 2
	helloDisabledByPolicy = 3
	helloDeviceBusy       = 4
	helloRetriesExhausted = 5
	helloResultCanceled   = 6
)

// ... and its errors: no answer in time, the prompt canceled.
var (
	errHelloTimeout  = errors.New("windows hello: no answer")
	errHelloCanceled = errors.New("windows hello: canceled")
)

const (
	helloPromptTimeout = 3 * time.Minute // the operator in front of the prompt
	helloAvailTTL      = 30 * time.Second
	helloAvailWait     = 1500 * time.Millisecond // a page asking waits this long for a fresh answer
)

var (
	errHelloOff         = errors.New("hello_off")
	errHelloNotAvail    = errors.New("hello_unavailable")
	errHelloUserCancel  = errors.New("hello_canceled")
	errHelloRetries     = errors.New("hello_retries")
	errHelloBusy        = errors.New("hello_busy")
	errHelloNotVerified = errors.New("hello_failed")
	// errHelloSkipped: no automatic prompt (hello_auto.go's rules): the page
	// shows the code field, without an error.
	errHelloSkipped = errors.New("hello_skipped")
)

// helloAvail caches Windows' availability answer.
var helloAvail struct {
	mu      sync.Mutex
	known   bool
	ok      bool
	at      time.Time
	probing chan struct{} // closed when the probe on its way answers
}

// helloAvailable: Windows Hello can verify this user (cached helloAvailTTL;
// a stale answer is refreshed, waited for helloAvailWait at most).
func helloAvailable() bool {
	helloAvail.mu.Lock()
	if helloAvail.known && time.Since(helloAvail.at) < helloAvailTTL {
		ok := helloAvail.ok
		helloAvail.mu.Unlock()
		return ok
	}
	ch := helloAvail.probing
	if ch == nil {
		ch = make(chan struct{})
		helloAvail.probing = ch
		go func() {
			ok, err := helloProbeFn()
			helloAvail.mu.Lock()
			helloAvail.known, helloAvail.ok, helloAvail.at = true, ok && err == nil, time.Now()
			helloAvail.probing = nil
			helloAvail.mu.Unlock()
			close(ch)
		}()
	}
	helloAvail.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(helloAvailWait):
	}
	helloAvail.mu.Lock()
	defer helloAvail.mu.Unlock()
	return helloAvail.known && helloAvail.ok
}

// helloForget: the next question asks Windows again (after a failed prompt:
// the device may be gone).
func helloForget() {
	helloAvail.mu.Lock()
	helloAvail.known = false
	helloAvail.mu.Unlock()
}

// helloVerify shows the prompt on window hwnd: nil only for "Verified".
func helloVerify(hwnd uintptr, message string) error {
	r, err := helloVerifyFn(hwnd, message, helloPromptTimeout)
	if err != nil {
		helloForget()
		if errors.Is(err, errHelloCanceled) || errors.Is(err, errHelloTimeout) {
			return errHelloUserCancel
		}
		return errHelloNotVerified
	}
	switch r {
	case helloVerified:
		return nil
	case helloResultCanceled:
		return errHelloUserCancel
	case helloRetriesExhausted:
		return errHelloRetries
	case helloDeviceBusy:
		return errHelloBusy
	case helloDeviceNotPresent, helloNotConfigured, helloDisabledByPolicy:
		helloForget()
		return errHelloNotAvail
	}
	return errHelloNotVerified
}

// helloVerifyOnce: helloVerify, one prompt at a time for the whole app (two
// unlock prompts at once share the same one: helloFlight).
func helloVerifyOnce(hwnd uintptr, message string) error {
	return helloOne.do(message, func() error { return helloVerify(hwnd, message) })
}

// vaultHelloJSON: what reachVaultState says of Windows Hello.
type vaultHelloJSON struct {
	Available bool `json:"available"` // Windows can verify this user
	Enabled   bool `json:"enabled"`   // ... and it may unlock this vault
}

func vaultHelloState(v *Vault, state string) vaultHelloJSON {
	if state != "locked" && state != "unlocked" {
		return vaultHelloJSON{}
	}
	return vaultHelloJSON{Available: helloAvailable(), Enabled: v.PresenceEnabled()}
}

// vaultUnlockHello (JS reachVaultUnlockHello, every window).
func (b *nativeBridge) vaultUnlockHello(tok, reqID string) (bool, error) {
	hwnd := b.hwnd
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if err := v.UnlockWithPresence(func() error {
			return helloVerifyOnce(hwnd, tr("vault.hello.prompt"))
		}); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// vaultUnlockHelloAuto (JS reachVaultUnlockHelloAuto, every window): an
// unlock prompt opening (promptID: one per opening) asks for Windows Hello
// at once. Launched only under hello_auto.go's rules (turned on, available,
// this window on screen in the foreground, not tried yet for this opening,
// not right after a canceled one); otherwise "hello_skipped" and the page
// shows the code field. A cancel or a failure is remembered (no automatic
// prompt again for a moment) and answered like the button's.
func (b *nativeBridge) vaultUnlockHelloAuto(tok, reqID, promptID string) (bool, error) {
	hwnd := b.hwnd
	if tok == b.token && !reqIDOK(promptID) {
		return false, errBadRequestID
	}
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		now := time.Now()
		st := v.State().State
		in := helloAutoIn{
			Locked:    st == "locked",
			Enabled:   st == "locked" && v.PresenceEnabled(),
			Tried:     helloAuto.wasTried(promptID),
			SinceFail: helloAuto.sinceFail(now),
		}
		if in.Enabled && !in.Tried {
			in.Available = helloAvailable()
			in.Shown = helloShownFn(hwnd)
		}
		if !helloAutoDecide(in) || !helloAuto.claim(promptID, now) {
			return nil, errHelloSkipped
		}
		if err := v.UnlockWithPresence(func() error {
			return helloVerifyOnce(hwnd, tr("vault.hello.prompt"))
		}); err != nil {
			if !errors.Is(err, errHelloBusy) {
				helloAuto.failed(time.Now())
			}
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// vaultHelloSet (JS reachVaultHelloSet, main window): the switch. On: from
// an unlocked vault, Windows available, and Hello verifies the operator now.
func (b *nativeBridge) vaultHelloSet(tok, reqID string, on bool) (bool, error) {
	hwnd := b.hwnd
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if v.State().State != "unlocked" {
			return nil, errVaultLocked
		}
		if on {
			if !helloAvailable() {
				return nil, errHelloNotAvail
			}
			if err := helloVerifyOnce(hwnd, tr("vault.hello.enablePrompt")); err != nil {
				return nil, err
			}
		}
		if err := v.SetPresence(on); err != nil {
			return nil, err
		}
		return map[string]bool{"enabled": on}, nil
	})
}
