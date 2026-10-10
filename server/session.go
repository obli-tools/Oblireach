package main

import (
	"sync"
	"time"
)

// PeerRole distinguishes a browser viewer from an agent.
type PeerRole string

const (
	RoleViewer PeerRole = "viewer"
	RoleAgent  PeerRole = "agent"
)

// peer represents one side of a tunnel connection.
type peer struct {
	role   PeerRole
	sendCh chan []byte  // outbound frames (binary or text)
	textCh chan []byte  // outbound text-only control messages
	done   chan struct{} // closed when this peer disconnects
}

// sessionEntry holds the state for one relay session.
type sessionEntry struct {
	token     string
	createdAt time.Time

	mu     sync.Mutex
	viewer *peer
	agent  *peer

	// pairCh is written exactly once when the second peer arrives.
	// The first peer waits on it to learn its partner's send channel.
	pairCh chan *peer
}

// sessionStore manages in-flight sessions indexed by session token.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionEntry
	max      int
}

func newSessionStore(max int) *sessionStore {
	return &sessionStore{
		sessions: make(map[string]*sessionEntry),
		max:      max,
	}
}

// count returns the number of sessions currently tracked.
func (s *sessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// getOrCreate returns (entry, isNew).
// Returns (nil, false) when the store is full.
func (s *sessionStore) getOrCreate(token string) (*sessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.sessions[token]; ok {
		return e, false
	}
	if s.max > 0 && len(s.sessions) >= s.max {
		return nil, false
	}
	e := &sessionEntry{
		token:     token,
		createdAt: time.Now(),
		pairCh:    make(chan *peer, 1),
	}
	s.sessions[token] = e
	return e, true
}

// remove deletes the session from the store.
func (s *sessionStore) remove(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// register attempts to register a peer in the session entry.
// Returns (partnerPairCh, alreadyTaken, ok).
//   - partnerPairCh: the channel the caller should wait on; when a value
//     arrives it is the partner peer whose sendCh the caller should forward to.
//   - alreadyTaken: a peer with the same role is already registered.
//   - ok: registration succeeded.
func (e *sessionEntry) register(p *peer) (partnerCh <-chan *peer, alreadyTaken bool, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch p.role {
	case RoleViewer:
		if e.viewer != nil {
			return nil, true, false
		}
		e.viewer = p
		// If agent already registered, notify it through pairCh and return a
		// channel that will be immediately readable.
		if e.agent != nil {
			ch := make(chan *peer, 1)
			ch <- e.agent
			e.pairCh <- p // notify the agent's goroutine
			return ch, false, true
		}
	case RoleAgent:
		if e.agent != nil {
			return nil, true, false
		}
		e.agent = p
		if e.viewer != nil {
			ch := make(chan *peer, 1)
			ch <- e.viewer
			e.pairCh <- p // notify the viewer's goroutine
			return ch, false, true
		}
	}

	// First peer — wait on the shared pairCh.
	return e.pairCh, false, true
}
