//go:build nt6

package supportlink

import "time"

// relayHoldBackStep: ICE v2 (pion/webrtc v3) does not rank the relay
// candidates by transport; the TCP and TLS ones are gathered this much
// later so UDP wins when it works (see relayHoldBack).
const relayHoldBackStep = time.Second
