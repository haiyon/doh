// Package cache provides a thread-safe in-memory key-value store with TTL-based expiration.
// It is designed for caching DNS wire-format responses with minimal GC pressure.
package cache

import (
	"sync"
	"time"
)

// entry holds a cached value along with its expiration deadline.
type entry struct {
	value     []byte
	expiresAt time.Time
}

// Cache is a thread-safe in-memory store with per-entry TTL expiration.
// Expired entries are lazily evicted on access and periodically purged
// by a background goroutine.
type Cache struct {
	mu      sync.RWMutex
	items   map[string]entry
	ttl     time.Duration
	closeCh chan struct{}
	once    sync.Once
}

// New creates a new Cache with the given default TTL and starts a background
// goroutine that purges expired entries every ttl/2 (minimum 30 s).
func New(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = time.Minute
	}

	interval := max(ttl/2, 30*time.Second)

	c := &Cache{
		items:   make(map[string]entry),
		ttl:     ttl,
		closeCh: make(chan struct{}),
	}

	go c.janitor(interval)

	return c
}

// Set stores value under key with the default TTL.
func (c *Cache) Set(key string, value []byte) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL stores value under key with an explicit TTL.
// If ttl is zero or negative the default TTL is used.
func (c *Cache) SetWithTTL(key string, value []byte, ttl time.Duration) {
	if ttl <= 0 {
		ttl = c.ttl
	}
	copied := append([]byte(nil), value...)

	c.mu.Lock()
	c.items[key] = entry{
		value:     copied,
		expiresAt: time.Now().Add(ttl),
	}
	c.mu.Unlock()
}

// Get retrieves the value for key. The second return value is false when the
// key does not exist or has expired.
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	expired := ok && time.Now().After(e.expiresAt)
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}
	if expired {
		c.mu.Lock()
		if current, exists := c.items[key]; exists && time.Now().After(current.expiresAt) {
			delete(c.items, key)
		}
		c.mu.Unlock()
		return nil, false
	}

	return append([]byte(nil), e.value...), true
}

// Close stops the background janitor goroutine.
func (c *Cache) Close() {
	c.once.Do(func() {
		close(c.closeCh)
	})
}

// janitor periodically removes expired entries from the cache.
func (c *Cache) janitor(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.deleteExpired()
		case <-c.closeCh:
			return
		}
	}
}

// deleteExpired removes all entries whose TTL has elapsed.
func (c *Cache) deleteExpired() {
	now := time.Now()

	c.mu.Lock()
	for k, e := range c.items {
		if now.After(e.expiresAt) {
			delete(c.items, k)
		}
	}
	c.mu.Unlock()
}
