package main

// hello_auto.go — the "Windows Hello at once" rule of the code prompts:
// an unlock prompt of the vault, when Hello is turned on and
// available, launches Windows Hello by itself as it opens, without a click.
// Hello canceled or failed: the code field (and the Hello button, to try
// again). This file is the pure part (no Windows call): the decision and its
// bookkeeping; the bindings are vault_hello.go's, the page's
// vault_ui.go's (vaultHelloOffer).
//
// The rules, each one a guard against a prompt the operator did not ask for:
//   - once per opening of a prompt: the page sends an id per opening, an id
//     already tried is never launched again (a failure or a cancel never
//     launches Hello again by itself; the button still does);
//   - only for a window on screen and in the foreground (not minimized, not
//     behind another one): a prompt opened in a background window shows the
//     code field;
//   - not again right after an automatic try was canceled or failed
//     (helloAutoQuietAfterFail): a prompt drawn again by the page (another
//     opening, same intent) does not bring Windows' prompt straight back;
//   - one Windows Hello prompt at a time for the whole app
//     (vault_hello.go helloVerifyOnce): two prompts at once share it.

import (
	"errors"
	"sync"
	"time"
)

// errHelloAborted: the prompt's run ended without an answer (a panic): the
// callers sharing it are never told "verified".
var errHelloAborted = errors.New("windows hello: aborted")

const (
	helloAutoQuietAfterFail = 8 * time.Second
	helloAutoTriedMax       = 64
	helloAutoTriedTTL       = 10 * time.Minute
)

// helloAutoIn: what the decision reads.
type helloAutoIn struct {
	Locked    bool          // the vault is locked (nothing to unlock otherwise)
	Enabled   bool          // the operator turned "unlock with Windows Hello" on
	Available bool          // Windows can verify this user now
	Shown     bool          // the asking window is visible, not minimized, in the foreground
	Tried     bool          // this opening of the prompt already had its automatic try
	SinceFail time.Duration // since the last automatic try canceled or failed (<0: none)
}

// helloAutoDecide: true when Windows Hello is launched by itself; false: the
// code field (the current behaviour, Hello's button when it is offered).
func helloAutoDecide(in helloAutoIn) bool {
	if !in.Locked || !in.Enabled || !in.Available || !in.Shown || in.Tried {
		return false
	}
	if in.SinceFail >= 0 && in.SinceFail < helloAutoQuietAfterFail {
		return false
	}
	return true
}

// helloAutoBook: the automatic tries made (by prompt opening) and the last
// one that did not unlock.
type helloAutoBook struct {
	mu       sync.Mutex
	tried    map[string]time.Time
	lastFail time.Time
}

// claim: the opening id gets its automatic try now (true), or had it
// already (false). Old ids go (helloAutoTriedTTL, helloAutoTriedMax).
func (b *helloAutoBook) claim(id string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tried == nil {
		b.tried = map[string]time.Time{}
	}
	if _, ok := b.tried[id]; ok {
		return false
	}
	for k, at := range b.tried {
		if now.Sub(at) > helloAutoTriedTTL {
			delete(b.tried, k)
		}
	}
	for len(b.tried) >= helloAutoTriedMax {
		var oldK string
		var oldT time.Time
		for k, at := range b.tried {
			if oldK == "" || at.Before(oldT) {
				oldK, oldT = k, at
			}
		}
		delete(b.tried, oldK)
	}
	b.tried[id] = now
	return true
}

// wasTried: the opening id had its automatic try.
func (b *helloAutoBook) wasTried(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.tried[id]
	return ok
}

// failed: an automatic try was canceled or did not verify.
func (b *helloAutoBook) failed(now time.Time) {
	b.mu.Lock()
	b.lastFail = now
	b.mu.Unlock()
}

// sinceFail: time since the last automatic try that did not unlock (<0: none).
func (b *helloAutoBook) sinceFail(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastFail.IsZero() {
		return -1
	}
	return now.Sub(b.lastFail)
}

// helloFlight: one Windows Hello prompt at a time (helloVerifyOnce). A
// caller with the same purpose (message) while one is up shares its answer;
// another purpose waits for it to end, then has its own.
type helloFlight struct {
	mu  sync.Mutex
	cur *helloCall
}

type helloCall struct {
	msg  string
	done chan struct{}
	err  error
}

// do runs verify (the prompt) unless one is up: see helloFlight.
func (f *helloFlight) do(msg string, verify func() error) error {
	for {
		f.mu.Lock()
		if c := f.cur; c != nil {
			f.mu.Unlock()
			<-c.done
			if c.msg == msg {
				return c.err
			}
			continue
		}
		c := &helloCall{msg: msg, done: make(chan struct{}), err: errHelloAborted}
		f.cur = c
		f.mu.Unlock()
		func() {
			defer func() {
				f.mu.Lock()
				f.cur = nil
				f.mu.Unlock()
				close(c.done)
			}()
			c.err = verify()
		}()
		return c.err
	}
}

// busy: a prompt is up.
func (f *helloFlight) busy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur != nil
}
