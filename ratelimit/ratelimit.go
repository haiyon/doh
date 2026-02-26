// Package ratelimit implements a per-IP token bucket rate limiter.
// Each unique remote address is allocated an independent bucket.
// Buckets that have not been accessed for longer than the cleanup interval
// are removed by a background goroutine to bound memory usage.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bucket is a single token bucket for one remote address.
type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// Limiter enforces a maximum request rate per remote IP address.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64 // tokens replenished per second
	capacity float64 // maximum token count
	closeCh  chan struct{}
	once     sync.Once
}

// New creates a Limiter that allows up to capacity requests per second per IP.
// A background goroutine removes idle buckets every 5 minutes.
func New(ratePerSec float64, capacity float64) *Limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if capacity <= 0 {
		capacity = ratePerSec
	}

	l := &Limiter{
		buckets:  make(map[string]*bucket),
		rate:     ratePerSec,
		capacity: capacity,
		closeCh:  make(chan struct{}),
	}
	go l.janitor(5 * time.Minute)
	return l
}

// Allow reports whether the request r should be permitted.
// It resolves the client IP (honouring X-Forwarded-For and X-Real-IP when
// present), refills the bucket proportionally to elapsed time, and consumes
// one token.
func (l *Limiter) Allow(r *http.Request) bool {
	ip := clientIP(r)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.capacity, lastSeen: now}
		l.buckets[ip] = b
	}

	elapsed := now.Sub(b.lastSeen).Seconds()
	b.lastSeen = now
	b.tokens = min(l.capacity, b.tokens+elapsed*l.rate)

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Close stops the background janitor goroutine.
func (l *Limiter) Close() {
	l.once.Do(func() {
		close(l.closeCh)
	})
}

// janitor removes buckets that have been idle for longer than interval.
func (l *Limiter) janitor(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.evictIdle(interval)
		case <-l.closeCh:
			return
		}
	}
}

func (l *Limiter) evictIdle(idleFor time.Duration) {
	cutoff := time.Now().Add(-idleFor)
	l.mu.Lock()
	for ip, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, ip)
		}
	}
	l.mu.Unlock()
}

// clientIP resolves the real client IP from the request.
// It checks X-Forwarded-For (leftmost entry) and X-Real-IP before falling
// back to RemoteAddr. Only the first address in X-Forwarded-For is used
// because that is the one set by the original client; subsequent entries
// may be added by intermediate proxies and are not trustworthy.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may be a comma-separated list; take the first entry.
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			xff = xff[:idx]
		}
		if ip := strings.TrimSpace(xff); ip != "" && net.ParseIP(ip) != nil {
			return ip
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
