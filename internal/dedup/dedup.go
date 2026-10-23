// Package dedup remembers which pod problems were already sent.
package dedup

import (
	"sync"
	"time"
)

// Store is an in-memory cooldown. A second process does not see these keys.
type Store struct {
	mu       sync.Mutex
	last     map[string]time.Time
	cooldown time.Duration
}

func New(cooldown time.Duration) *Store {
	return &Store{last: map[string]time.Time{}, cooldown: cooldown}
}

// Allow reports whether key may be sent at now.
func (s *Store) Allow(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.last[key]
	if !ok {
		return true
	}
	return now.Sub(t) >= s.cooldown
}

// Mark records a successful send.
func (s *Store) Mark(key string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[key] = now
}
