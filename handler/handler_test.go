package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"log"
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

func TestParseRequestRejectsOversizedGETDNSPayload(t *testing.T) {
	h := New(Config{Upstreams: []string{"https://example.com/dns-query"}, UpstreamTimeout: time.Second})

	oversized := make([]byte, maxBodySize+1)
	dns := base64.RawURLEncoding.EncodeToString(oversized)

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+dns, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized dns payload, got %d", rec.Code)
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

func TestParseRequestPostAllowsContentTypeParameters(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(dnsMsg(0)))
	req.Header.Set("Content-Type", "Application/DNS-Message; charset=utf-8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid Content-Type with parameters, got %d", rec.Code)
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

func TestQueryOnePreservesExistingUpstreamQuery(t *testing.T) {
	var sawCustom atomic.Bool
	var sawDNS atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCustom.Store(r.URL.Query().Get("provider") == "custom")
		sawDNS.Store(r.URL.Query().Get("dns") != "")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dnsMsg(0))
	}))
	t.Cleanup(upstream.Close)

	h := New(Config{UpstreamTimeout: time.Second})
	res := h.queryOne(context.Background(), upstream.URL+"?provider=custom", http.MethodGet, "abc", nil)
	if res.err != nil {
		t.Fatalf("expected successful query, got error: %v", res.err)
	}
	if !sawCustom.Load() {
		t.Fatalf("expected existing upstream query parameters to be preserved")
	}
	if !sawDNS.Load() {
		t.Fatalf("expected dns query parameter to be appended")
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
	msg[4] = 0
	msg[5] = 1 // QDCOUNT = 1
	msg[6] = 0
	msg[7] = 1 // ANCOUNT = 1
	// Question at offset 12.
	msg[12] = 0 // root label
	msg[13] = 0
	msg[14] = 1 // QTYPE A
	msg[15] = 0
	msg[16] = 1 // QCLASS IN
	// Answer at offset 17.
	msg[17] = 0 // root label
	msg[18] = 0
	msg[19] = 1 // TYPE A
	msg[20] = 0
	msg[21] = 1 // CLASS IN
	// TTL = 300 = 0x0000012C
	msg[22] = 0
	msg[23] = 0
	msg[24] = 1
	msg[25] = 0x2c
	// RDLENGTH = 4
	msg[26] = 0
	msg[27] = 4
	// RDATA: 4 zero bytes (already zero-initialized)

	ttl := dnsTTL(msg)
	if ttl != 300 {
		t.Fatalf("expected TTL=300, got %d", ttl)
	}
}

func TestDebugLoggingCacheMissAndHit(t *testing.T) {
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
		Debug:           true,
	})
	dns := base64.RawURLEncoding.EncodeToString(dnsMsg(0))

	logs := captureLogs(t, func() {
		req1 := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+dns, nil)
		rec1 := httptest.NewRecorder()
		h.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusOK {
			t.Fatalf("expected first request success, got %d", rec1.Code)
		}

		req2 := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+dns, nil)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("expected second request success, got %d", rec2.Code)
		}
	})

	if !strings.Contains(logs, "[debug]") {
		t.Fatalf("expected debug logs, got %q", logs)
	}
	if !strings.Contains(logs, "cache=MISS") {
		t.Fatalf("expected cache MISS debug log, got %q", logs)
	}
	if !strings.Contains(logs, "cache=HIT") {
		t.Fatalf("expected cache HIT debug log, got %q", logs)
	}
}

func TestDebugLoggingDisabledByDefault(t *testing.T) {
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
	dns := base64.RawURLEncoding.EncodeToString(dnsMsg(0))

	logs := captureLogs(t, func() {
		req := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+dns, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected request success, got %d", rec.Code)
		}
	})

	if strings.Contains(logs, "[debug]") {
		t.Fatalf("did not expect debug logs when disabled, got %q", logs)
	}
}

func TestDebugLoggingOnBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	h := New(Config{
		Upstreams:       []string{upstream.URL},
		BatchSize:       1,
		UpstreamTimeout: time.Second,
		Debug:           true,
	})
	dns := base64.RawURLEncoding.EncodeToString(dnsMsg(0))

	var rec *httptest.ResponseRecorder
	logs := captureLogs(t, func() {
		req := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+dns, nil)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	})

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(logs, "error=bad_gateway") {
		t.Fatalf("expected debug bad_gateway log, got %q", logs)
	}
}

func TestClientIPHandlesIPv6RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/dns-query", nil)
	req.RemoteAddr = "[2001:db8::1]:5353"
	if got := clientIP(req); got != "2001:db8::1" {
		t.Fatalf("expected IPv6 host, got %q", got)
	}
}

func TestClientIPPrioritizesForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/dns-query", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.8, 203.0.113.9")
	req.Header.Set("X-Real-IP", "192.0.2.3")
	if got := clientIP(req); got != "198.51.100.8" {
		t.Fatalf("expected first X-Forwarded-For IP, got %q", got)
	}
}

func captureLogs(t *testing.T, fn func()) string {
	t.Helper()

	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	}()

	fn()
	return buf.String()
}

func dnsMsg(rcode int) []byte {
	msg := make([]byte, 12)
	msg[3] = byte(rcode & 0x0f)
	return msg
}
