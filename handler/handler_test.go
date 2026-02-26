package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haiyon/doh/cache"
)

func TestServeHTTPBearerAuthStrict(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dnsMsg(0))
	}))
	t.Cleanup(upstream.Close)

	c := cache.New(time.Minute)
	t.Cleanup(c.Close)

	h := New(Config{
		Upstreams:       []string{upstream.URL},
		Cache:           c,
		BatchSize:       1,
		UpstreamTimeout: time.Second,
		Token:           "secret-token",
	})

	badReq := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	badReq.Header.Set("Authorization", "Basic secret-token")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-bearer auth, got %d", badRec.Code)
	}

	goodReq := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	goodReq.Header.Set("Authorization", "Bearer secret-token")
	goodRec := httptest.NewRecorder()
	h.ServeHTTP(goodRec, goodReq)
	if goodRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid bearer token, got %d", goodRec.Code)
	}
}

func TestParseRequestRequiresDNSParam(t *testing.T) {
	h := New(Config{Upstreams: []string{"https://example.com/dns-query"}, UpstreamTimeout: time.Second})

	req := httptest.NewRequest(http.MethodGet, "/dns-query?foo=bar", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing dns param, got %d", rec.Code)
	}
}

func TestParseRequestRejectsInvalidBase64(t *testing.T) {
	h := New(Config{Upstreams: []string{"https://example.com/dns-query"}, UpstreamTimeout: time.Second})

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns=!!!invalid!!!", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid base64url, got %d", rec.Code)
	}
}

func TestParseRequestPostRequiresContentType(t *testing.T) {
	h := New(Config{Upstreams: []string{"https://example.com/dns-query"}, UpstreamTimeout: time.Second})

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(dnsMsg(0)))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for wrong Content-Type, got %d", rec.Code)
	}
}

func TestMethodNotAllowedIncludesAllowHeader(t *testing.T) {
	h := New(Config{Upstreams: []string{"https://example.com/dns-query"}, UpstreamTimeout: time.Second})

	req := httptest.NewRequest(http.MethodPut, "/dns-query", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, POST" {
		t.Fatalf("expected Allow: GET, POST, got %q", allow)
	}
}

func TestQueryOneRejectsOversizedResponse(t *testing.T) {
	oversized := make([]byte, maxBodySize+1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(oversized)
	}))
	t.Cleanup(upstream.Close)

	h := New(Config{UpstreamTimeout: time.Second})
	res := h.queryOne(context.Background(), upstream.URL, http.MethodGet, "abc", nil)
	if res.err == nil {
		t.Fatalf("expected oversized response error")
	}
	if !strings.Contains(res.err.Error(), "too large") {
		t.Fatalf("unexpected error: %v", res.err)
	}
}

func TestQueryUpstreamsReturnsFirstNoError(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dnsMsg(2))
	}))
	t.Cleanup(failSrv.Close)

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dnsMsg(0))
	}))
	t.Cleanup(okSrv.Close)

	h := New(Config{
		Upstreams:       []string{failSrv.URL, okSrv.URL},
		BatchSize:       2,
		UpstreamTimeout: time.Second,
	})

	resp, err := h.queryUpstreams(context.Background(), http.MethodGet, "abc", nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if dnsRcode(resp) != 0 {
		t.Fatalf("expected NOERROR response, got rcode=%d", dnsRcode(resp))
	}
}

func TestBuildCacheKeyIncludesSegmentBoundaries(t *testing.T) {
	a := buildCacheKey("AB", []byte("C"))
	b := buildCacheKey("A", []byte("BC"))
	if a == b {
		t.Fatalf("unexpected cache key collision")
	}
}

func TestHandleUsesCache(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dnsMsg(0))
	}))
	t.Cleanup(upstream.Close)

	c := cache.New(time.Minute)
	t.Cleanup(c.Close)

	h := New(Config{
		Upstreams:       []string{upstream.URL},
		Cache:           c,
		BatchSize:       1,
		UpstreamTimeout: time.Second,
	})

	req1 := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected first request success, got %d", rec1.Code)
	}
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected MISS on first request")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected second request success, got %d", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected HIT on second request")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly one upstream call, got %d", calls.Load())
	}
}

func TestHandleSetsCacheControlHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dnsMsg(0))
	}))
	t.Cleanup(upstream.Close)

	h := New(Config{
		Upstreams:       []string{upstream.URL},
		BatchSize:       1,
		UpstreamTimeout: time.Second,
	})

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Fatalf("expected Cache-Control header to be set")
	}
}

func TestDNSTTLExtraction(t *testing.T) {
	// Minimal DNS response: header(12) + question(5) + answer(15) = 32 bytes.
	// Question: root-label(1) + QTYPE(2) + QCLASS(2) = 5 bytes.
	// Answer:   root-label(1) + TYPE(2) + CLASS(2) + TTL(4) + RDLENGTH(2) + RDATA(4) = 15 bytes.
	msg := make([]byte, 32)
	msg[4] = 0; msg[5] = 1  // QDCOUNT = 1
	msg[6] = 0; msg[7] = 1  // ANCOUNT = 1
	// Question at offset 12.
	msg[12] = 0             // root label
	msg[13] = 0; msg[14] = 1 // QTYPE A
	msg[15] = 0; msg[16] = 1 // QCLASS IN
	// Answer at offset 17.
	msg[17] = 0              // root label
	msg[18] = 0; msg[19] = 1 // TYPE A
	msg[20] = 0; msg[21] = 1 // CLASS IN
	// TTL = 300 = 0x0000012C
	msg[22] = 0; msg[23] = 0; msg[24] = 1; msg[25] = 0x2c
	// RDLENGTH = 4
	msg[26] = 0; msg[27] = 4
	// RDATA: 4 zero bytes (already zero-initialized)

	ttl := dnsTTL(msg)
	if ttl != 300 {
		t.Fatalf("expected TTL=300, got %d", ttl)
	}
}

func dnsMsg(rcode int) []byte {
	msg := make([]byte, 12)
	msg[3] = byte(rcode & 0x0f)
	return msg
}
