package server

import (
	"sync"
	"time"
)

// Limits are the anti-bruteforce and rate-limit settings. The
// PAKE already reduces a guesser to one online try per code; these limits
// bound how many tries reach a host at all.
type Limits struct {
	ConnectPerMinIP float64 // connect requests per operator IP
	ConnectBurstIP  float64
	ConnectPerMinID float64 // connect requests per target ID
	ConnectBurstID  float64

	RegisterPerMinIP float64 // ID requests per host IP
	RegisterBurstIP  float64

	MsgPerSec float64 // signalling messages per connection (flood guard)
	MsgBurst  float64

	FailsPerID   int           // wrong codes on one ID within FailWindow...
	FailWindow   time.Duration //
	LockDuration time.Duration // ...lock the ID this long

	FailsPerIP    int           // failures (wrong code, bad operator key) from one IP...
	IPFailWindow  time.Duration //
	IPBanDuration time.Duration // ...ban the IP this long

	// Account pages and API (0 = the default of webDefaults).
	LoginPerMinIP      float64       // POST /v1/account/login per client IP
	LoginBurstIP       float64       //
	LoginFailsPerEmail int           // wrong passwords on one address within LoginFailWindow...
	LoginFailWindow    time.Duration // ...then logins to it are refused until the window passes
	SignupPerHourIP    float64       // POST /register per client IP
	SignupBurstIP      float64       //
	VerifyPerMinIP     float64       // GET/POST /verify per client IP
	VerifyBurstIP      float64       //
	APIPerMinIP        float64       // other /v1/account calls per client IP
	APIBurstIP         float64       //
	MailsPerHourEmail  float64       // messages sent to one address
	MailsBurstEmail    float64       //
	ReportPerHourIP    float64       // POST /v1/report per client IP (one report per session anyway)
	ReportBurstIP      float64       //
}

// webDefaults fills the account limits left at zero.
func (l *Limits) webDefaults() {
	set := func(p *float64, v float64) {
		if *p <= 0 {
			*p = v
		}
	}
	set(&l.LoginPerMinIP, 10)
	set(&l.LoginBurstIP, 10)
	if l.LoginFailsPerEmail <= 0 {
		l.LoginFailsPerEmail = 10
	}
	if l.LoginFailWindow <= 0 {
		l.LoginFailWindow = 15 * time.Minute
	}
	set(&l.SignupPerHourIP, 10)
	set(&l.SignupBurstIP, 5)
	set(&l.VerifyPerMinIP, 20)
	set(&l.VerifyBurstIP, 20)
	set(&l.APIPerMinIP, 60)
	set(&l.APIBurstIP, 60)
	set(&l.MailsPerHourEmail, 3)
	set(&l.MailsBurstEmail, 3)
	set(&l.ReportPerHourIP, 10)
	set(&l.ReportBurstIP, 5)
}

// syncBuckets is a bucketSet safe for concurrent use (HTTP handlers).
type syncBuckets struct {
	mu sync.Mutex
	b  *bucketSet
}

func newSyncBuckets(perMin, burst float64) *syncBuckets {
	return &syncBuckets{b: newBucketSet(perMin, burst)}
}

// take returns true when allowed, else the time until a token is back.
func (s *syncBuckets) take(key string, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.take(key, now) {
		return true, 0
	}
	b := s.b.m[key]
	wait := time.Duration((1 - b.tokens) / s.b.perSec * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

func (s *syncBuckets) gc(now time.Time) {
	s.mu.Lock()
	s.b.gc(now)
	s.mu.Unlock()
}

// syncFails is a failLog safe for concurrent use.
type syncFails struct {
	mu sync.Mutex
	f  *failLog
}

func (s *syncFails) count(key string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.f.m[key] {
		if now.Sub(t) < s.f.window {
			n++
		}
	}
	return n
}

func (s *syncFails) add(key string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.add(key, now)
}

func (s *syncFails) reset(key string) {
	s.mu.Lock()
	s.f.reset(key)
	s.mu.Unlock()
}

func (s *syncFails) gc(now time.Time) {
	s.mu.Lock()
	s.f.gc(now)
	s.mu.Unlock()
}

// DefaultLimits are the default values for a private instance.
func DefaultLimits() Limits {
	return Limits{
		ConnectPerMinIP: 10, ConnectBurstIP: 10,
		ConnectPerMinID: 5, ConnectBurstID: 5,
		RegisterPerMinIP: 30, RegisterBurstIP: 30,
		MsgPerSec: 50, MsgBurst: 200,
		FailsPerID: 5, FailWindow: 10 * time.Minute, LockDuration: 15 * time.Minute,
		FailsPerIP: 20, IPFailWindow: time.Hour, IPBanDuration: 24 * time.Hour,
	}
}

// bucket is a token bucket; callers hold the owning lock.
type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) take(now time.Time, perSec, burst float64) bool {
	if b.last.IsZero() {
		b.tokens, b.last = burst, now
	}
	b.tokens += now.Sub(b.last).Seconds() * perSec
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// full reports whether the bucket has refilled (it can be forgotten).
func (b *bucket) full(now time.Time, perSec, burst float64) bool {
	return b.tokens+now.Sub(b.last).Seconds()*perSec >= burst
}

// bucketSet is a keyed set of token buckets sharing one rate.
type bucketSet struct {
	perSec, burst float64
	m             map[string]*bucket
}

func newBucketSet(perMin, burst float64) *bucketSet {
	return &bucketSet{perSec: perMin / 60, burst: burst, m: map[string]*bucket{}}
}

func (s *bucketSet) take(key string, now time.Time) bool {
	b := s.m[key]
	if b == nil {
		b = &bucket{}
		s.m[key] = b
	}
	return b.take(now, s.perSec, s.burst)
}

func (s *bucketSet) gc(now time.Time) {
	for k, b := range s.m {
		if b.full(now, s.perSec, s.burst) {
			delete(s.m, k)
		}
	}
}

// failLog counts recent failures per key within a sliding window.
type failLog struct {
	window time.Duration
	m      map[string][]time.Time
}

func newFailLog(window time.Duration) *failLog {
	return &failLog{window: window, m: map[string][]time.Time{}}
}

// add records a failure and returns the count inside the window.
func (f *failLog) add(key string, now time.Time) int {
	keep := f.m[key][:0]
	for _, t := range f.m[key] {
		if now.Sub(t) < f.window {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	f.m[key] = keep
	return len(keep)
}

func (f *failLog) reset(key string) { delete(f.m, key) }

func (f *failLog) gc(now time.Time) {
	for k, ts := range f.m {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= f.window {
			delete(f.m, k)
		}
	}
}

// deadlines maps a key to the time a lock or ban ends.
type deadlines map[string]time.Time

func (d deadlines) active(key string, now time.Time) (time.Duration, bool) {
	until, ok := d[key]
	if !ok || !now.Before(until) {
		return 0, false
	}
	return until.Sub(now), true
}

func (d deadlines) gc(now time.Time) {
	for k, t := range d {
		if !now.Before(t) {
			delete(d, k)
		}
	}
}

// byteLimiter polices relayed traffic (bytes per second); packets over the
// budget are dropped, as a network policer would.
type byteLimiter struct {
	mu     sync.Mutex
	b      bucket
	perSec float64
	burst  float64
}

func newByteLimiter(kbps int) *byteLimiter {
	if kbps <= 0 {
		return nil
	}
	perSec := float64(kbps) * 1000 / 8
	burst := perSec / 4 // 250 ms worth of traffic
	if burst < 64<<10 {
		burst = 64 << 10
	}
	return &byteLimiter{perSec: perSec, burst: burst}
}

func (l *byteLimiter) allow(n int) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.b.last.IsZero() {
		l.b.tokens, l.b.last = l.burst, now
	}
	l.b.tokens += now.Sub(l.b.last).Seconds() * l.perSec
	if l.b.tokens > l.burst {
		l.b.tokens = l.burst
	}
	l.b.last = now
	if l.b.tokens < float64(n) {
		return false
	}
	l.b.tokens -= float64(n)
	return true
}
