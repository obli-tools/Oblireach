//go:build !nt6

package supportlink

import "time"

// relayHoldBackStep: none on the modern line, where ICE ranks the relay
// candidates by transport itself (see relayHoldBack).
const relayHoldBackStep time.Duration = 0
