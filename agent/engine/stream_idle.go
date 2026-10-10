package engine

import "time"

// Inactivity timeout of a direct-path stream (stream.go run): the stream ends
// after streamIdleTimeout without input, monitor / codec switch or viewer
// keepalive, and warns the viewer streamIdleWarning before
// (inactivity_warning{seconds}, then inactivity_timeout).
const (
	streamIdleTimeout = 10 * time.Minute
	streamIdleWarning = 30 * time.Second
)

// idleWatch is that timeout: warning() fires warnBefore ahead of expired(),
// and reset() — any input, a monitor / codec switch, a viewer keepalive —
// starts both over. Used from ONE goroutine (the stream's capture loop).
type idleWatch struct {
	timeout    time.Duration
	warnBefore time.Duration
	idle       *time.Timer
	warn       *time.Timer
	warned     bool
}

func newIdleWatch(timeout, warnBefore time.Duration) *idleWatch {
	if warnBefore <= 0 || warnBefore >= timeout {
		warnBefore = timeout / 2
	}
	return &idleWatch{
		timeout:    timeout,
		warnBefore: warnBefore,
		idle:       time.NewTimer(timeout),
		warn:       time.NewTimer(timeout - warnBefore),
	}
}

// expired fires when the stream has been idle for the whole timeout.
func (w *idleWatch) expired() <-chan time.Time { return w.idle.C }

// warning fires warnBefore ahead of expired.
func (w *idleWatch) warning() <-chan time.Time { return w.warn.C }

// takeWarning is called when warning() fired: true only the first time since
// the last reset (the viewer is warned once per idle period).
func (w *idleWatch) takeWarning() bool {
	if w.warned {
		return false
	}
	w.warned = true
	return true
}

// reset starts the idle period over. A timer that already fired but was not
// consumed by the loop is drained, so a stale fire never ends a stream that
// just got activity.
func (w *idleWatch) reset() {
	stopAndDrain(w.idle)
	w.idle.Reset(w.timeout)
	stopAndDrain(w.warn)
	w.warn.Reset(w.timeout - w.warnBefore)
	w.warned = false
}

func (w *idleWatch) stop() {
	w.idle.Stop()
	w.warn.Stop()
}

func stopAndDrain(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
