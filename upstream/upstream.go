// Package upstream provides DoH upstream resolver lists by region
// and resolves the active list based on configuration.
package upstream

import (
	"math/rand/v2"
	"net/url"
	"strings"
)

// Region identifies a geographic upstream preset.
type Region string

const (
	RegionGlobal Region = "global"
	RegionUS     Region = "us"
	RegionKR     Region = "kr"
	RegionCN     Region = "cn"
)

// presets maps each region to its curated list of DoH upstream URLs.
var presets = map[Region][]string{
	RegionGlobal: {
		"https://cloudflare-dns.com/dns-query",
		"https://dns.google/dns-query",
		"https://doh.opendns.com/dns-query",
		"https://dns.nextdns.io/dns-query",
		"https://unfiltered.adguard-dns.com/dns-query",
		"https://freedns.controld.com/p0",
		"https://public.dns.iij.jp/dns-query",
		"https://doh.dns.sb/dns-query",
		"https://wikimedia-dns.org/dns-query",
		"https://doh.ffmuc.net/dns-query",
		"https://sky.rethinkdns.com/dns-query",
		"https://dns.quad9.net/dns-query",
	},
	RegionUS: {
		"https://cloudflare-dns.com/dns-query",
		"https://dns.google/dns-query",
		"https://doh.opendns.com/dns-query",
		"https://dns.nextdns.io/dns-query",
		"https://freedns.controld.com/p0",
		"https://sky.rethinkdns.com/dns-query",
		"https://dns.quad9.net/dns-query",
	},
	RegionKR: {
		"https://cloudflare-dns.com/dns-query",
		"https://dns.google/dns-query",
		"https://jp.tiar.app/dns-query",
		"https://public.dns.iij.jp/dns-query",
		"https://doh.dns.sb/dns-query",
		"https://dns.nextdns.io/dns-query",
		"https://unfiltered.adguard-dns.com/dns-query",
		"https://sky.rethinkdns.com/dns-query",
	},
	RegionCN: {
		"https://doh.pub/dns-query",
		"https://dns.alidns.com/dns-query",
		"https://doh.360.cn/dns-query",
		"https://sm2.doh.pub/dns-query",
	},
}

// Resolve returns the upstream list to use, in priority order:
//  1. explicit override (non-empty urls) — used as-is after shuffling
//  2. named region preset
//  3. global preset (fallback)
//
// The returned slice is always a shuffled copy.
func Resolve(urls []string, region Region) []string {
	var list []string
	switch {
	case len(urls) > 0:
		list = urls
	case region != "" && region != RegionGlobal:
		if p, ok := presets[region]; ok {
			list = p
		}
	}
	if len(list) == 0 {
		list = presets[RegionGlobal]
	}

	out := sanitize(list)
	if len(out) == 0 {
		out = sanitize(presets[RegionGlobal])
	}
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Regions returns all available preset region names.
func Regions() []Region {
	return []Region{RegionGlobal, RegionUS, RegionKR, RegionCN}
}

func sanitize(urls []string) []string {
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))

	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			continue
		}
		normalized := parsed.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	return out
}
