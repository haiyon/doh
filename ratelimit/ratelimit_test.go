package ratelimit

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllowRateLimit(t *testing.T) {
	l := New(1, 1)
	t.Cleanup(l.Close)

	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "203.0.113.10:12345"

	if !l.Allow(req) {
		t.Fatalf("first request should be allowed")
	}
	if l.Allow(req) {
		t.Fatalf("second immediate request should be rejected")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	l := New(10, 10)
	l.Close()
	l.Close()
}

func TestBucketRefills(t *testing.T) {
	l := New(20, 1)
	t.Cleanup(l.Close)

	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "203.0.113.20:12345"

	if !l.Allow(req) {
		t.Fatalf("first request should be allowed")
	}
	if l.Allow(req) {
		t.Fatalf("second immediate request should be rejected")
	}

	time.Sleep(70 * time.Millisecond)
	if !l.Allow(req) {
		t.Fatalf("request should be allowed after refill")
	}
}

func TestClientIPXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.99, 10.0.0.1")

	if ip := clientIP(req); ip != "203.0.113.99" {
		t.Fatalf("expected 203.0.113.99 from X-Forwarded-For, got %q", ip)
	}
}

func TestClientIPXRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Real-IP", "203.0.113.88")

	if ip := clientIP(req); ip != "203.0.113.88" {
		t.Fatalf("expected 203.0.113.88 from X-Real-IP, got %q", ip)
	}
}

func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "203.0.113.77:54321"

	if ip := clientIP(req); ip != "203.0.113.77" {
		t.Fatalf("expected 203.0.113.77 from RemoteAddr, got %q", ip)
	}
}

func TestClientIPIgnoresInvalidXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "203.0.113.55:12345"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	if ip := clientIP(req); ip != "203.0.113.55" {
		t.Fatalf("expected fallback to RemoteAddr, got %q", ip)
	}
}

func TestRateLimitUsesXForwardedFor(t *testing.T) {
	l := New(1, 1)
	t.Cleanup(l.Close)

	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.42")

	if !l.Allow(req) {
		t.Fatalf("first request should be allowed")
	}
	if l.Allow(req) {
		t.Fatalf("second immediate request should be rejected")
	}

	// Different real IP via proxy should have its own bucket.
	req2 := httptest.NewRequest("GET", "http://example.com", nil)
	req2.RemoteAddr = "10.0.0.1:12345"
	req2.Header.Set("X-Forwarded-For", "203.0.113.43")

	if !l.Allow(req2) {
		t.Fatalf("different client IP should have independent bucket")
	}
}
