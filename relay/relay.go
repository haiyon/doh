// Package relay builds HTTP handlers for different deployment targets.
package relay

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/haiyon/doh/cache"
	"github.com/haiyon/doh/handler"
	"github.com/haiyon/doh/health"
	"github.com/haiyon/doh/ratelimit"
	"github.com/haiyon/doh/upstream"
)

// Config controls how the relay handler stack is assembled.
type Config struct {
	CacheTTL        time.Duration
	BatchSize       int
	UpstreamTimeout time.Duration
	Region          string
	Upstreams       []string
	Token           string
	RateLimit       float64
	RateBurst       float64
	Debug           bool
}

// BuildFromEnv creates the DoH HTTP mux from environment variables.
//
// Supported environment variables:
//   - DOH_CACHE_TTL (default: 10m)
//   - DOH_BATCH_SIZE (default: 3)
//   - DOH_UPSTREAM_TIMEOUT (default: 4s)
//   - DOH_REGION (default: global)
//   - DOH_UPSTREAMS (comma-separated DoH URLs, overrides DOH_REGION)
//   - DOH_TOKEN
//   - DOH_RATE_LIMIT
//   - DOH_RATE_BURST
//   - DOH_DEBUG (true/false)
func BuildFromEnv(version string) (http.Handler, func()) {
	cfg := Config{
		CacheTTL:        parseDurationEnv("DOH_CACHE_TTL", 10*time.Minute),
		BatchSize:       parseIntEnv("DOH_BATCH_SIZE", 3),
		UpstreamTimeout: parseDurationEnv("DOH_UPSTREAM_TIMEOUT", 4*time.Second),
		Region:          getEnv("DOH_REGION", "global"),
		Upstreams:       parseCSVEnv("DOH_UPSTREAMS"),
		Token:           strings.TrimSpace(os.Getenv("DOH_TOKEN")),
		RateLimit:       parseFloatEnv("DOH_RATE_LIMIT", 0),
		RateBurst:       parseFloatEnv("DOH_RATE_BURST", 0),
		Debug:           parseBoolEnv("DOH_DEBUG", false),
	}
	return Build(version, cfg)
}

// Build creates the DoH HTTP mux from explicit configuration.
func Build(version string, cfg Config) (http.Handler, func()) {
	cfg = normalizeConfig(cfg)
	upstreams := upstream.Resolve(cfg.Upstreams, upstream.Region(cfg.Region))

	var limiter *ratelimit.Limiter
	if cfg.RateLimit > 0 {
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = cfg.RateLimit
		}
		limiter = ratelimit.New(cfg.RateLimit, burst)
	}

	c := cache.New(cfg.CacheTTL)
	h := handler.New(handler.Config{
		Upstreams:       upstreams,
		Cache:           c,
		BatchSize:       cfg.BatchSize,
		UpstreamTimeout: cfg.UpstreamTimeout,
		Token:           cfg.Token,
		Limiter:         limiter,
		Debug:           cfg.Debug,
	})

	mux := http.NewServeMux()
	mux.Handle("/dns-query", h)
	mux.Handle("/healthz", health.Handler(version))

	cleanup := func() {
		if limiter != nil {
			limiter.Close()
		}
		c.Close()
	}
	return mux, cleanup
}

func normalizeConfig(cfg Config) Config {
	if cfg.CacheTTL <= 0 {
		log.Printf("invalid cache ttl=%s; fallback to 10m", cfg.CacheTTL)
		cfg.CacheTTL = 10 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		log.Printf("invalid batch size=%d; fallback to 3", cfg.BatchSize)
		cfg.BatchSize = 3
	}
	if cfg.UpstreamTimeout <= 0 {
		log.Printf("invalid upstream timeout=%s; fallback to 4s", cfg.UpstreamTimeout)
		cfg.UpstreamTimeout = 4 * time.Second
	}
	cfg.Region = strings.ToLower(strings.TrimSpace(cfg.Region))
	if cfg.Region == "" {
		cfg.Region = "global"
	}
	cfg.Token = strings.TrimSpace(cfg.Token)
	return cfg
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseCSVEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q; fallback to %s", key, raw, fallback)
		return fallback
	}
	return v
}

func parseIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("invalid %s=%q; fallback to %d", key, raw, fallback)
		return fallback
	}
	return v
}

func parseFloatEnv(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("invalid %s=%q; fallback to %.2f", key, raw, fallback)
		return fallback
	}
	return v
}

func parseBoolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("invalid %s=%q; fallback to %t", key, raw, fallback)
		return fallback
	}
	return v
}
