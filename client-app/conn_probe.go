package main

// conn_probe.go — the Status column of the local base's connections (ui.go
// localDevProbe): is each one's host:port answering, from this computer? A
// plain TCP connection, closed at once (nothing is sent: no handshake, no
// credential), a few seconds at most, a bounded number at a time. The page
// names the connections by their vault id; their addresses never come from
// the page.

import (
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	connProbeTimeout  = 4 * time.Second
	connProbeParallel = 16
	connProbeMax      = 500
)

// probeConns: id -> reachable, for the ids lookup knows (the others are left
// out). dial is net.DialTimeout's shape (a fake in tests).
func probeConns(ids []string, lookup func(id string) (host string, port int, ok bool), dial func(addr string, d time.Duration) (net.Conn, error)) map[string]bool {
	out := map[string]bool{}
	if len(ids) > connProbeMax {
		ids = ids[:connProbeMax]
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, connProbeParallel)
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		host, port, ok := lookup(id)
		if !ok || host == "" || port < 1 || port > 65535 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id, addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			c, err := dial(addr, connProbeTimeout)
			if err == nil {
				_ = c.Close()
			}
			mu.Lock()
			out[id] = err == nil
			mu.Unlock()
		}(id, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	wg.Wait()
	return out
}

// vaultConnAddr: the host and port of an ssh/rdp connection of the vault.
func vaultConnAddr(v *Vault, id string) (string, int, bool) {
	m, err := v.Get(id)
	if err != nil || (m.Kind != vaultKindSSH && m.Kind != vaultKindRDP) {
		return "", 0, false
	}
	return m.Host, int(m.Port), true
}
