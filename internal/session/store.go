package session

import (
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"sync"
	"time"
)

// MediaType is the play protocol for a session.
type MediaType string

const (
	MediaHLS         MediaType = "hls"
	MediaProgressive MediaType = "progressive"
)

// Session holds one playable quality stream and proxy binding.
type Session struct {
	Token        string
	UpstreamURL  string
	UpstreamHost string
	Platform     string
	Channel      string
	Quality      string
	Label        string
	MediaType    MediaType
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// Store is a bounded in-memory session map with sliding TTL.
type Store struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]*Session
}

func NewStore(ttl time.Duration, max int) *Store {
	if max <= 0 {
		max = 64
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Store{
		ttl:     ttl,
		max:     max,
		entries: make(map[string]*Session),
	}
}

func (s *Store) Create(upstream, platform, channel, quality, label string, media MediaType) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now())

	// Evict oldest if at capacity
	for len(s.entries) >= s.max {
		s.evictOldestLocked()
	}

	host := ""
	if u, err := url.Parse(upstream); err == nil {
		host = u.Hostname()
	}
	now := time.Now()
	sess := &Session{
		Token:        newToken(),
		UpstreamURL:  upstream,
		UpstreamHost: host,
		Platform:     platform,
		Channel:      channel,
		Quality:      quality,
		Label:        label,
		MediaType:    media,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.ttl),
	}
	s.entries[sess.Token] = sess
	return sess
}

func (s *Store) Get(token string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	sess, ok := s.entries[token]
	if !ok {
		return nil
	}
	if now.After(sess.ExpiresAt) {
		delete(s.entries, token)
		return nil
	}
	// Sliding TTL
	sess.ExpiresAt = now.Add(s.ttl)
	// Return a shallow copy so callers don't mutate under lock
	cp := *sess
	return &cp
}

func (s *Store) cleanupLocked(now time.Time) {
	for k, v := range s.entries {
		if now.After(v.ExpiresAt) {
			delete(s.entries, k)
		}
	}
}

func (s *Store) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, v := range s.entries {
		if first || v.CreatedAt.Before(oldestTime) {
			first = false
			oldestKey = k
			oldestTime = v.CreatedAt
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

func newToken() string {
	var b [18]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
