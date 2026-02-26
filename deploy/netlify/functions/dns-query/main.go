package main

import (
	"context"
	"net/http"
	"os"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/haiyon/doh/relay"
)

var (
	once    sync.Once
	adapter *httpadapter.HandlerAdapter
)

func getAdapter() *httpadapter.HandlerAdapter {
	once.Do(func() {
		mux, _ := relay.BuildFromEnv(resolveVersion())
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/dns-query"
			mux.ServeHTTP(w, r2)
		})
		adapter = httpadapter.New(h)
	})
	return adapter
}

func resolveVersion() string {
	if v := os.Getenv("DOH_VERSION"); v != "" {
		return v
	}
	if v := os.Getenv("COMMIT_REF"); v != "" {
		return v
	}
	return "netlify"
}

func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	return getAdapter().ProxyWithContext(ctx, req)
}

func main() {
	lambda.Start(handler)
}
