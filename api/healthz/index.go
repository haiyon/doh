package handler

import (
	"net/http"

	serverless "github.com/haiyon/doh/platform"
)

// Handler is the Vercel entrypoint for /healthz.
func Handler(w http.ResponseWriter, r *http.Request) {
	serverless.ServePath("/healthz", w, r)
}
