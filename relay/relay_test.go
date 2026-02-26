package relay

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildFromEnvProvidesHealthz(t *testing.T) {
	t.Setenv("DOH_REGION", "global")

	h, cleanup := BuildFromEnv("test")
	t.Cleanup(cleanup)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestBuildFromEnvTokenGuard(t *testing.T) {
	t.Setenv("DOH_TOKEN", "secret")

	h, cleanup := BuildFromEnv("test")
	t.Cleanup(cleanup)

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestBuildNormalizesInvalidConfig(t *testing.T) {
	h, cleanup := Build("test", Config{
		CacheTTL:        0,
		BatchSize:       0,
		UpstreamTimeout: 0,
		Region:          "",
		Token:           "secret",
		RateLimit:       0,
		RateBurst:       0,
	})
	t.Cleanup(cleanup)

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns=abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestBuildWithExplicitUpstreamList(t *testing.T) {
	respBody := []byte{0x00, 0x00, 0x00, 0x00}
	var gotQueryParam atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQueryParam.Store(r.URL.Query().Get("dns") != "")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(upstream.Close)

	h, cleanup := Build("test", Config{
		CacheTTL:        time.Minute,
		BatchSize:       1,
		UpstreamTimeout: time.Second,
		Upstreams:       []string{upstream.URL},
		Region:          "global",
	})
	t.Cleanup(cleanup)

	query := base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x00, 0x00})
	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !gotQueryParam.Load() {
		t.Fatalf("expected dns query param to be forwarded to upstream")
	}
	if !bytes.Equal(rec.Body.Bytes(), respBody) {
		t.Fatalf("unexpected body: %x", rec.Body.Bytes())
	}
}
