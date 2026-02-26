// Package health implements the /healthz liveness endpoint.
package health

import (
	"encoding/json"
	"net/http"
)

// var startTime = time.Now()

// response is the JSON body returned by the health endpoint.
type response struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime,omitempty"`
	Version string `json:"version,omitempty"`
}

// Handler returns an http.Handler that responds to liveness probes.
// It always returns 200 OK as long as the process is running.
func Handler(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := json.Marshal(response{
			Status: "ok",
			// Uptime and Version are optional fields that can be included for monitoring purposes.
			// Uptime:  time.Since(startTime).Truncate(time.Second).String(),
			// Version: version,
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}
