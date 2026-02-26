package handler

import (
	"net/http"

	serverless "github.com/haiyon/doh/platform"
)

// Handler is the Vercel entrypoint for /dns-query.
func Handler(w http.ResponseWriter, r *http.Request) {
	serverless.ServePath("/dns-query", w, r)
}
