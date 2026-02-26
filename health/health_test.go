package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerWithVersion(t *testing.T) {
	h := Handler("v0.1.1")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if got["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", got["status"])
	}
	if got["version"] != "v0.1.1" {
		t.Fatalf("expected version=v0.1.1, got %v", got["version"])
	}
}

func TestHandlerWithoutVersionOmitsField(t *testing.T) {
	h := Handler("   ")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if got["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", got["status"])
	}
	if _, exists := got["version"]; exists {
		t.Fatalf("did not expect version field when version is empty")
	}
}
