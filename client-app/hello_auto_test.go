package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The decision: every condition is needed; a try already made for this
// opening, or a cancel moments ago, never launches Hello again by itself.
func TestHelloAutoDecide(t *testing.T) {
	ok := helloAutoIn{Locked: true, Enabled: true, Available: true, Shown: true, SinceFail: -1}
	if !helloAutoDecide(ok) {
		t.Fatal("all conditions met: no prompt")
	}
	for name, mut := range map[string]func(*helloAutoIn){
		"unlocked":       func(in *helloAutoIn) { in.Locked = false },
		"off":            func(in *helloAutoIn) { in.Enabled = false },
		"unavailable":    func(in *helloAutoIn) { in.Available = false },
		"background":     func(in *helloAutoIn) { in.Shown = false },
		"already tried":  func(in *helloAutoIn) { in.Tried = true },
		"just failed":    func(in *helloAutoIn) { in.SinceFail = time.Second },
		"failed at once": func(in *helloAutoIn) { in.SinceFail = 0 },
	} {
		in := ok
		mut(&in)
		if helloAutoDecide(in) {
			t.Fatalf("%s: prompt launched", name)
		}
	}
	in := ok
	in.SinceFail = helloAutoQuietAfterFail + time.Millisecond
	if !helloAutoDecide(in) {
		t.Fatal("long after a cancel: no prompt")
	}
}

// One automatic try per opening id; the book stays bounded.
func TestHelloAutoBook(t *testing.T) {
	var b helloAutoBook
	now := time.Now()
	if b.sinceFail(now) >= 0 {
		t.Fatal("no failure yet")
	}
	if b.wasTried("a") || !b.claim("a", now) || !b.wasTried("a") || b.claim("a", now) {
		t.Fatal("claim once")
	}
	b.failed(now)
	if d := b.sinceFail(now.Add(time.Second)); d != time.Second {
		t.Fatalf("sinceFail %v", d)
	}
	for i := 0; i < 3*helloAutoTriedMax; i++ {
		b.claim(string(rune('A'+i%26))+time.Duration(i).String(), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(b.tried) > helloAutoTriedMax {
		t.Fatalf("book grows: %d", len(b.tried))
	}
	// Old ids go after their TTL.
	b.claim("late", now.Add(helloAutoTriedTTL+time.Hour))
	if b.wasTried("a") {
		t.Fatal("expired id kept")
	}
}

// One prompt at a time: the same purpose shares the answer (one prompt for
// both), another purpose waits for it and then has its own.
func TestHelloFlight(t *testing.T) {
	var f helloFlight
	var prompts int32
	release := make(chan struct{})
	started := make(chan struct{})
	errA := errors.New("canceled")
	var wg sync.WaitGroup
	res := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res[0] = f.do("unlock", func() error {
			atomic.AddInt32(&prompts, 1)
			close(started)
			<-release
			return errA
		})
	}()
	<-started
	if !f.busy() {
		t.Fatal("not busy during a prompt")
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		res[1] = f.do("unlock", func() error { atomic.AddInt32(&prompts, 1); return nil })
	}()
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()
	if prompts != 1 || res[0] != errA || res[1] != errA {
		t.Fatalf("shared: prompts %d res %v", prompts, res)
	}
	// Another purpose: its own prompt, after.
	if err := f.do("enable", func() error { atomic.AddInt32(&prompts, 1); return nil }); err != nil || prompts != 2 || f.busy() {
		t.Fatalf("own prompt: %v %d", err, prompts)
	}
	// A panic in the prompt never tells the callers "verified".
	func() {
		defer func() { _ = recover() }()
		f.do("x", func() error { panic("boom") })
	}()
	if f.busy() {
		t.Fatal("busy after a panic")
	}
}
