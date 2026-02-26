// Package main is the entry point for the DoH relay server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/haiyon/doh/relay"
)

// version is injected at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	// -healthz mode: probe the local server and exit with the result.
	// Used by Docker HEALTHCHECK since the scratch image has no curl/wget.
	if len(os.Args) == 2 && os.Args[1] == "-healthz" {
		if err := probe(); err != nil {
			log.Fatal(err)
		}
		return
	}

	addr := flag.String("addr", defaultListenAddr(), "HTTP listen address")
	cacheTTL := flag.Duration("cache-ttl", 600*time.Second, "Cache TTL for successful DNS responses")
	batchSize := flag.Int("batch-size", 3, "Number of upstreams queried concurrently per round")
	upstreamTimeout := flag.Duration("upstream-timeout", 4*time.Second, "Per-upstream HTTP request timeout")
	upstreamList := flag.String("upstreams", "", "Comma-separated upstream DoH URLs (overrides -region)")
	region := flag.String("region", "global", "Upstream preset region: global, us, kr, cn")
	token := flag.String("token", "", "Bearer token required in Authorization header (disabled when empty)")
	rateLimit := flag.Float64("rate-limit", 0, "Max requests per second per IP (disabled when 0)")
	rateBurst := flag.Float64("rate-burst", 0, "Token bucket burst capacity (defaults to -rate-limit when 0)")
	debug := flag.Bool("debug", false, "Enable per-request query logging")
	flag.Parse()

	var overrides []string
	if *upstreamList != "" {
		for _, u := range strings.Split(*upstreamList, ",") {
			if u = strings.TrimSpace(u); u != "" {
				overrides = append(overrides, u)
			}
		}
	}
	mux, cleanup := relay.Build(version, relay.Config{
		CacheTTL:        *cacheTTL,
		BatchSize:       *batchSize,
		UpstreamTimeout: *upstreamTimeout,
		Region:          *region,
		Upstreams:       overrides,
		Token:           *token,
		RateLimit:       *rateLimit,
		RateBurst:       *rateBurst,
		Debug:           *debug,
	})
	defer cleanup()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("doh %s listening on %s", version, *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("server exited")
}

// probe performs a single GET /healthz against the default listen address and
// returns an error if the response is not 200 OK. Used by Docker HEALTHCHECK.
func probe() error {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://localhost:8053/healthz")
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

func defaultListenAddr() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return ":8053"
	}
	if strings.HasPrefix(port, ":") {
		return port
	}
	return ":" + port
}
