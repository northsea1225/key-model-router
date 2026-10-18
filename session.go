package main

import (
	"crypto/subtle"
	"sync"
	"time"
)

type session struct {
	Token     string
	CSRF      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type sessionStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]session
}

func newSessionStore(ttl time.Duration) *sessionStore {
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &sessionStore{ttl: ttl, entries: make(map[string]session)}
}

func (s *sessionStore) setTTL(ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	s.mu.Lock()
	s.ttl = ttl
	s.mu.Unlock()
}

func (s *sessionStore) Create() (session, error) {
	token, errToken := randomToken(32)
	if errToken != nil {
		return session{}, errToken
	}
	csrf, errCSRF := randomToken(32)
	if errCSRF != nil {
		return session{}, errCSRF
	}
	now := time.Now()
	entry := session{
		Token:     token,
		CSRF:      csrf,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	s.mu.Lock()
	s.cleanupLocked(now)
	s.entries[token] = entry
	s.mu.Unlock()
	return entry, nil
}

func (s *sessionStore) Get(token string) (session, bool) {
	if token == "" {
		return session{}, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, okEntry := s.entries[token]
	if !okEntry {
		return session{}, false
	}
	if now.After(entry.ExpiresAt) {
		delete(s.entries, token)
		return session{}, false
	}
	return entry, true
}

func (s *sessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.entries, token)
	s.mu.Unlock()
}

func (s *sessionStore) cleanupLocked(now time.Time) {
	if len(s.entries) < 64 {
		return
	}
	for token, entry := range s.entries {
		if now.After(entry.ExpiresAt) {
			delete(s.entries, token)
		}
	}
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
