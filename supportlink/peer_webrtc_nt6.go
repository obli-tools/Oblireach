//go:build nt6

// nt6 build line (Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 / 2012 R2,
// Go 1.20.14, go120.mod): the same peer as peer_webrtc.go on pion/webrtc
// v3.3.6, the last line that builds with Go 1.20 (v4 needs Go 1.24). Every
// method used here has the same signature and behaviour in v3 (ICE v2 dials
// TURN over TCP and TLS through the proxy dialer, reports the relay protocol
// in the candidate stats, and honours the relay acceptance wait), so this
// file only differs by its import. Keep it in step with peer_webrtc.go.
//
// Wire compatibility with the modern line (v3 <-> v4): same SDP, same
// pre-negotiated data channel (id 0, ordered, reliable), DTLS 1.2 with
// ECDSA P-256 certificates on both sides, SCTP over DTLS.

package supportlink

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"

	webrtc "github.com/pion/webrtc/v3"

	"github.com/oblireach/supportlink/wire"
)

// The data channel is pre-negotiated with a fixed stream id on both sides:
// no in-band DCEP open handshake, no OnDataChannel race, and the same setup
// works with a browser peer (negotiated: true, id: 0). One ordered, reliable
// channel carries text and binary frames (see the framing in session.go).
const (
	linkLabel           = "oblireach"
	linkStreamID uint16 = 0
)

type pionPeer struct {
	pc       *webrtc.PeerConnection
	link     *pionChannel
	forceTCP bool
	relays   *relayTracker

	mu           sync.Mutex
	onFail       func(string)
	releaseAfter time.Duration
	releaseTimer *time.Timer
	closed       bool
}

// newRTCPeer builds a PeerConnection from the ICE servers handed out by the
// rendezvous server after acceptance, and creates the data channel. so
// gives the proxy and the TLS settings, shared with the signalling.
func newRTCPeer(ice *wire.ICEInfo, o ICEOptions, so SignalOptions) (rtcPeer, error) {
	servers := iceServers(ice, o)
	proxy, err := resolveProxy(so.Proxy)
	if err != nil {
		return nil, err
	}
	rt := &relayTracker{}
	var urls []string
	if ice != nil {
		rt.user, rt.pass, urls = ice.Username, ice.Credential, ice.URLs
	}
	se := webrtc.SettingEngine{}
	tn, err := newTrackingNet(rt)
	if err != nil {
		return nil, err
	}
	se.SetNet(tn)
	// TURN over TCP and TLS: our dialer (proxy, TLS settings, tracking).
	td := &turnDialer{proxy: proxy, tlsHost: turnTLSHosts(urls), tlsConf: so.TLSConfig, t: rt}
	td.tcpDelay, td.tlsDelay = relayHoldBack(ice, o)
	se.SetICEProxyDialer(td)
	wait := o.RelayAcceptanceMinWait
	if wait == 0 {
		wait = DefaultRelayAcceptanceMinWait
	}
	if wait > 0 {
		se.SetRelayAcceptanceMinWait(wait)
	}
	// Detect a dead path quickly; the session is interactive.
	se.SetICETimeouts(5*time.Second, 15*time.Second, 2*time.Second)
	if o.IncludeLoopback {
		se.SetIncludeLoopbackCandidate(true)
	}
	if o.IPFilter != nil {
		se.SetIPFilter(o.IPFilter)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	cfg := webrtc.Configuration{ICEServers: servers}
	if o.ForceRelay {
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	pc, err := api.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	p := &pionPeer{pc: pc, forceTCP: o.TURNTCPOnly, relays: rt, releaseAfter: o.RelayReleaseAfter}
	if p.releaseAfter == 0 {
		p.releaseAfter = DefaultRelayReleaseAfter
	}
	if p.releaseAfter > 0 {
		pc.SCTP().Transport().ICETransport().OnSelectedCandidatePairChange(func(*webrtc.ICECandidatePair) { p.scheduleRelease() })
	}
	ordered, negotiated, id := true, true, linkStreamID
	dc, err := pc.CreateDataChannel(linkLabel, &webrtc.DataChannelInit{Ordered: &ordered, Negotiated: &negotiated, ID: &id})
	if err != nil {
		pc.Close()
		return nil, err
	}
	p.link = &pionChannel{dc: dc}
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			p.mu.Lock()
			f := p.onFail
			p.mu.Unlock()
			if f != nil {
				f("peer connection " + s.String())
			}
		}
	})
	return p, nil
}

// iceServers turns the server's URL list into STUN and TURN entries.
// TURNTCPOnly keeps the TURN URLs over TCP and TLS; TURNTLSOnly only the
// turns: ones. Both drop STUN.
func iceServers(ice *wire.ICEInfo, o ICEOptions) []webrtc.ICEServer {
	if ice == nil {
		return nil
	}
	var stun, turn []string
	for _, u := range ice.URLs {
		switch {
		case strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:"):
			if !o.TURNTCPOnly && !o.TURNTLSOnly {
				stun = append(stun, u)
			}
		case strings.HasPrefix(u, "turns:"):
			turn = append(turn, u)
		case strings.HasPrefix(u, "turn:"):
			if o.TURNTLSOnly || (o.TURNTCPOnly && !strings.Contains(u, "transport=tcp")) {
				continue
			}
			turn = append(turn, u)
		}
	}
	var out []webrtc.ICEServer
	if len(stun) > 0 {
		out = append(out, webrtc.ICEServer{URLs: stun})
	}
	if len(turn) > 0 {
		out = append(out, webrtc.ICEServer{URLs: turn, Username: ice.Username, Credential: ice.Credential})
	}
	return out
}

func (p *pionPeer) channel() dataChannel { return p.link }

func (p *pionPeer) createOffer() (string, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	return offer.SDP, nil
}

func (p *pionPeer) acceptOffer(sdp string) (string, error) {
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return "", err
	}
	ans, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	if err := p.pc.SetLocalDescription(ans); err != nil {
		return "", err
	}
	return ans.SDP, nil
}

func (p *pionPeer) setAnswer(sdp string) error {
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

func (p *pionPeer) addCandidate(c string) error {
	var ci webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(c), &ci); err != nil {
		return err
	}
	return p.pc.AddICECandidate(ci)
}

func (p *pionPeer) onCandidate(f func(string)) {
	p.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		b, err := json.Marshal(c.ToJSON())
		if err == nil {
			f(string(b))
		}
	})
}

func (p *pionPeer) onFailed(f func(string)) {
	p.mu.Lock()
	p.onFail = f
	p.mu.Unlock()
}

// path reports the selected candidate pair. The nominated pair from the
// stats carries the relay transport (client->TURN over udp or tcp).
func (p *pionPeer) path() (Path, string) {
	var pair *webrtc.ICECandidatePairStats
	cands := map[string]webrtc.ICECandidateStats{}
	for _, s := range p.pc.GetStats() {
		switch v := s.(type) {
		case webrtc.ICECandidatePairStats:
			if v.Nominated && v.State == webrtc.StatsICECandidatePairStateSucceeded {
				vv := v
				pair = &vv
			}
		case webrtc.ICECandidateStats:
			cands[v.ID] = v
		}
	}
	if pair == nil {
		return PathUnknown, ""
	}
	l, r := cands[pair.LocalCandidateID], cands[pair.RemoteCandidateID]
	side := func(c webrtc.ICECandidateStats) string {
		d := c.CandidateType.String() + "/" + c.Protocol
		if c.CandidateType == webrtc.ICECandidateTypeRelay && c.RelayProtocol != "" {
			d += " via " + c.RelayProtocol
		}
		return d
	}
	detail := side(l) + " <-> " + side(r)
	switch {
	case l.CandidateType == webrtc.ICECandidateTypeRelay:
		switch {
		case strings.EqualFold(l.RelayProtocol, "tls"):
			return PathRelayTLS, detail
		case strings.EqualFold(l.RelayProtocol, "tcp") || (l.RelayProtocol == "" && p.forceTCP):
			return PathRelayTCP, detail
		}
		return PathRelayUDP, detail
	case r.CandidateType == webrtc.ICECandidateTypeRelay:
		// Only the peer is relayed; this side talks UDP to the relay address.
		return PathRelayUDP, detail
	default:
		return PathDirect, detail
	}
}

func (p *pionPeer) close() error {
	p.mu.Lock()
	p.closed = true
	if p.releaseTimer != nil {
		p.releaseTimer.Stop()
	}
	p.mu.Unlock()
	return p.pc.Close()
}

// scheduleRelease (re)starts the release delay on every change of the
// selected pair.
func (p *pionPeer) scheduleRelease() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.releaseTimer != nil {
		p.releaseTimer.Stop()
	}
	p.releaseTimer = time.AfterFunc(p.releaseAfter, p.releaseUnused)
}

// releaseUnused releases the relay allocations the selected pair does not
// use: all of them on a direct pair, all but the selected one when this
// side is relayed.
func (p *pionPeer) releaseUnused() {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || p.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		return
	}
	ict := p.pc.SCTP().Transport().ICETransport()
	pair, err := ict.GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil {
		return
	}
	var keep *net.UDPAddr
	keepTCP := false
	if l := pair.Local; l.Typ == webrtc.ICECandidateTypeRelay {
		keep = &net.UDPAddr{IP: net.ParseIP(l.RelatedAddress), Port: int(l.RelatedPort)}
		if keep.IP == nil {
			return // cannot tell which socket carries it: keep everything
		}
		st, ok := ict.GetSelectedCandidatePairStats()
		keepTCP = ok && p.isTCPRelay(st.LocalCandidateID)
	}
	p.relays.releaseExcept(keep, keepTCP)
}

// isTCPRelay tells whether a local relay candidate reaches TURN over TCP
// or TLS (its related address is then a TCP socket).
func (p *pionPeer) isTCPRelay(id string) bool {
	for _, s := range p.pc.GetStats() {
		if c, ok := s.(webrtc.ICECandidateStats); ok && c.ID == id {
			return strings.EqualFold(c.RelayProtocol, "tcp") || strings.EqualFold(c.RelayProtocol, "tls")
		}
	}
	return false
}

// relaysHeld is the number of relay allocations this side still holds.
func (p *pionPeer) relaysHeld() int { return p.relays.held() }

// pionChannel adapts *webrtc.DataChannel to dataChannel.
type pionChannel struct{ dc *webrtc.DataChannel }

func (c *pionChannel) send(b []byte) error                    { return c.dc.Send(b) }
func (c *pionChannel) bufferedAmount() uint64                 { return c.dc.BufferedAmount() }
func (c *pionChannel) setBufferedAmountLowThreshold(n uint64) { c.dc.SetBufferedAmountLowThreshold(n) }
func (c *pionChannel) onBufferedAmountLow(f func())           { c.dc.OnBufferedAmountLow(f) }
func (c *pionChannel) onOpen(f func())                        { c.dc.OnOpen(f) }
func (c *pionChannel) onClose(f func())                       { c.dc.OnClose(f) }
func (c *pionChannel) onMessage(f func([]byte)) {
	c.dc.OnMessage(func(m webrtc.DataChannelMessage) { f(m.Data) })
}
func (c *pionChannel) close() error { return c.dc.Close() }
