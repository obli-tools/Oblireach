package server

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// metrics is a minimal Prometheus text exposition, without the client
// library: a handful of counters and gauges is all the server needs.
type metrics struct {
	pakeFailed     atomic.Int64
	idLocked       atomic.Int64
	accepted       atomic.Int64
	rejected       atomic.Int64
	registered     atomic.Int64
	resumed        atomic.Int64
	relayBytes     atomic.Int64
	relayDropped   atomic.Int64
	relayAllocs    atomic.Int64 // currently open allocations
	turnAuthFail   atomic.Int64
	turnRevoked    atomic.Int64
	relayRefused   atomic.Int64
	ipBanned       atomic.Int64
	relaySessions  atomic.Int64 // sessions with at least one open allocation
	relayReclaimed atomic.Int64
	muxDropped     atomic.Int64

	mu          sync.Mutex
	connect     map[string]int64 // by result
	rateLimited map[string]int64 // by key
	paths       map[string]int64 // by path
	ends        map[string]int64 // by outcome
	allocs      map[string]int64 // by client transport
	quota       map[string]int64 // connects refused by an account quota, by quota
	account     map[string]int64 // account events (register, verify, login...), by event and result
	admin       map[string]int64 // admin actions, by action
	reports     map[string]int64 // abuse reports, by result (ok_<reason>, invalid, duplicate...)
}

func newMetrics() *metrics {
	return &metrics{
		connect: map[string]int64{}, rateLimited: map[string]int64{},
		paths: map[string]int64{}, ends: map[string]int64{}, allocs: map[string]int64{},
		quota: map[string]int64{}, account: map[string]int64{}, admin: map[string]int64{},
		reports: map[string]int64{},
	}
}

func (m *metrics) inc(mp map[string]int64, label string) {
	m.mu.Lock()
	mp[label]++
	m.mu.Unlock()
}

// gauges are read from the hub at scrape time.
type gauges struct {
	hostsOnline int
	sessions    map[string]int // by state
}

func (m *metrics) write(w io.Writer, g gauges) {
	p := func(name, typ, help string) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	labeled := func(name, label string, mp map[string]int64) {
		keys := make([]string, 0, len(mp))
		for k := range mp {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s{%s=%q} %d\n", name, label, k, mp[k])
		}
	}

	p("rdv_hosts_online", "gauge", "Hosts registered with an ID.")
	fmt.Fprintf(w, "rdv_hosts_online %d\n", g.hostsOnline)
	p("rdv_sessions", "gauge", "Open sessions by state.")
	for _, st := range []string{"pake", "confirmed", "accepted"} {
		fmt.Fprintf(w, "rdv_sessions{state=%q} %d\n", st, g.sessions[st])
	}

	m.mu.Lock()
	p("rdv_connect_total", "counter", "Operator connect requests by result.")
	labeled("rdv_connect_total", "result", m.connect)
	p("rdv_rate_limited_total", "counter", "Requests refused by a rate limit, by key.")
	labeled("rdv_rate_limited_total", "key", m.rateLimited)
	p("rdv_session_path_total", "counter", "Established sessions by network path.")
	labeled("rdv_session_path_total", "path", m.paths)
	p("rdv_session_end_total", "counter", "Ended sessions by outcome.")
	labeled("rdv_session_end_total", "outcome", m.ends)
	p("rdv_relay_alloc_total", "counter", "TURN allocations created, by client transport (udp, tcp, tls).")
	labeled("rdv_relay_alloc_total", "transport", m.allocs)
	p("rdv_quota_refused_total", "counter", "Operator connects refused by an account quota, by quota.")
	labeled("rdv_quota_refused_total", "quota", m.quota)
	p("rdv_account_events_total", "counter", "Account events (register, verify, login, logout), by event and result.")
	labeled("rdv_account_events_total", "event", m.account)
	p("rdv_admin_actions_total", "counter", "Actions taken from the admin pages, by action.")
	labeled("rdv_admin_actions_total", "action", m.admin)
	p("rdv_reports_total", "counter", "Abuse reports received, by result (ok_<reason>, invalid, duplicate, rate_limited, bad_request, auto_disabled).")
	labeled("rdv_reports_total", "result", m.reports)
	m.mu.Unlock()

	for _, c := range []struct {
		name, typ, help string
		v               int64
	}{
		{"rdv_registered_total", "counter", "IDs handed out.", m.registered.Load()},
		{"rdv_resumed_total", "counter", "IDs given back to their host after a signalling drop.", m.resumed.Load()},
		{"rdv_pake_failed_total", "counter", "Failed code attempts (PAKE not confirmed).", m.pakeFailed.Load()},
		{"rdv_id_locked_total", "counter", "IDs locked after repeated wrong codes.", m.idLocked.Load()},
		{"rdv_ip_banned_total", "counter", "IPs banned after repeated failures.", m.ipBanned.Load()},
		{"rdv_accepted_total", "counter", "Sessions accepted by the host.", m.accepted.Load()},
		{"rdv_rejected_total", "counter", "Sessions refused by the host.", m.rejected.Load()},
		{"rdv_relay_bytes_total", "counter", "Bytes relayed by TURN (both directions).", m.relayBytes.Load()},
		{"rdv_relay_dropped_bytes_total", "counter", "Relayed bytes dropped by the per-session bandwidth cap.", m.relayDropped.Load()},
		{"rdv_relay_active", "gauge", "Open TURN allocations.", m.relayAllocs.Load()},
		{"rdv_relay_sessions", "gauge", "Sessions holding relay allocations (gathered by ICE in every session; unused ones are released or reclaimed).", m.relaySessions.Load()},
		{"rdv_relay_reclaimed_total", "counter", "Idle TURN allocations closed by the server.", m.relayReclaimed.Load()},
		{"rdv_tls_mux_dropped_total", "counter", "Connections dropped on the shared TLS port (too many pending handshakes).", m.muxDropped.Load()},
		{"rdv_relay_refused_total", "counter", "TURN allocations refused by quota.", m.relayRefused.Load()},
		{"rdv_turn_auth_fail_total", "counter", "TURN requests with invalid or expired credentials.", m.turnAuthFail.Load()},
		{"rdv_turn_revoked_total", "counter", "TURN requests refused because their session ended.", m.turnRevoked.Load()},
	} {
		p(c.name, c.typ, c.help)
		fmt.Fprintf(w, "%s %d\n", c.name, c.v)
	}
}

func (s *Server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.m.write(w, s.hub.gauges())
}
