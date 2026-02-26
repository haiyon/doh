package main

import "testing"

func TestDefaultListenAddr(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		t.Setenv("PORT", "")
		if got := defaultListenAddr(); got != ":8053" {
			t.Fatalf("expected :8053, got %q", got)
		}
	})

	t.Run("plain_port", func(t *testing.T) {
		t.Setenv("PORT", "8080")
		if got := defaultListenAddr(); got != ":8080" {
			t.Fatalf("expected :8080, got %q", got)
		}
	})

	t.Run("colon_port", func(t *testing.T) {
		t.Setenv("PORT", ":9090")
		if got := defaultListenAddr(); got != ":9090" {
			t.Fatalf("expected :9090, got %q", got)
		}
	})
}
