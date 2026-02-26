package platform

import (
	"net/http"
	"os"
	"sync"

	"github.com/haiyon/doh/relay"
)

var (
	once sync.Once
	mux  http.Handler
)

func handler() http.Handler {
	once.Do(func() {
		mux, _ = relay.BuildFromEnv(resolveVersion())
	})
	return mux
}

func resolveVersion() string {
	if v := os.Getenv("DOH_VERSION"); v != "" {
		return v
	}
	if v := os.Getenv("VERCEL_GIT_COMMIT_TAG"); v != "" {
		return v
	}
	if v := os.Getenv("VERCEL_GIT_COMMIT_SHA"); v != "" {
		return v
	}
	return "vercel"
}

// ServePath forwards the incoming function request to the canonical path
// handled by the shared DoH mux.
func ServePath(path string, w http.ResponseWriter, r *http.Request) {
	r2 := r.Clone(r.Context())
	r2.URL.Path = path
	handler().ServeHTTP(w, r2)
}
