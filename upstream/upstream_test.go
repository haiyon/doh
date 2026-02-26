package upstream

import "testing"

func TestResolveSanitizeAndDedupe(t *testing.T) {
	in := []string{
		" https://dns.google/dns-query ",
		"https://dns.google/dns-query",
		"ftp://invalid.example.com",
		"://bad",
		"",
	}

	got := Resolve(in, RegionGlobal)
	if len(got) != 1 {
		t.Fatalf("expected one valid unique url, got=%v", got)
	}
	if got[0] != "https://dns.google/dns-query" {
		t.Fatalf("unexpected url: %q", got[0])
	}
}

func TestResolveFallsBackToGlobalWhenInvalidOverride(t *testing.T) {
	got := Resolve([]string{"", "ftp://invalid"}, RegionUS)
	if len(got) == 0 {
		t.Fatalf("expected global fallback list")
	}
}

func TestResolveRegionPreset(t *testing.T) {
	got := Resolve(nil, RegionCN)
	if len(got) != len(presets[RegionCN]) {
		t.Fatalf("expected cn preset length %d, got %d", len(presets[RegionCN]), len(got))
	}
}
