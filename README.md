# DoH

A DNS-over-HTTPS (DoH) relay server written in Go, compliant with [RFC 8484](https://datatracker.ietf.org/doc/html/rfc8484).

Accepts standard DoH queries (GET and POST) and fans them out concurrently to a configurable pool of upstream resolvers. The first valid `NOERROR` response is returned to the client and cached in memory.

## Features

- RFC 8484 compliant — GET and POST, Content-Type validation, base64url validation
- Built-in regional upstream presets: `global`, `us`, `kr`, `cn`
- Configurable upstream list via flag (overrides region preset)
- Concurrent upstream fan-out with configurable batch size
- In-memory TTL cache — respects DNS record TTL, background eviction
- Per-IP token bucket rate limiting — honours `X-Forwarded-For` / `X-Real-IP`
- Bearer token authentication
- Per-upstream request timeout with graceful fallback
- Request payload size limit (65,535 bytes decoded wire bytes for GET/POST)
- HTTP/2 upstream connections
- `Cache-Control` response header derived from DNS TTL
- `/healthz` liveness endpoint
- Graceful shutdown on `SIGINT` / `SIGTERM`
- Single static binary — no runtime dependencies

## Requirements

- Go 1.22 or later

## Build

```bash
git clone https://github.com/haiyon/doh.git
cd doh
go build -trimpath -ldflags="-s -w -buildid= -X main.version=$(git describe --tags --always)" -o doh .
```

## Usage

```text
./doh [flags]
```

### Flags

| Flag                | Default             | Description                                                        |
| ------------------- | ------------------- | ------------------------------------------------------------------ |
| `-addr`             | `:$PORT` or `:8053` | HTTP listen address (`PORT` env takes precedence when set)         |
| `-cache-ttl`        | `10m`               | Maximum cache TTL (capped by DNS record TTL)                       |
| `-batch-size`       | `3`                 | Upstreams queried concurrently per round                           |
| `-upstream-timeout` | `4s`                | Per-upstream HTTP request timeout                                  |
| `-region`           | `global`            | Upstream preset region: `global`, `us`, `kr`, `cn`                 |
| `-upstreams`        | _(disabled)_        | Comma-separated upstream DoH URLs (overrides `-region`)            |
| `-token`            | _(disabled)_        | Bearer token required in `Authorization` header                    |
| `-rate-limit`       | `0`                 | Max requests per second per IP (`0` = disabled)                    |
| `-rate-burst`       | `0`                 | Token bucket burst capacity (defaults to `-rate-limit`)            |
| `-debug`            | `false`             | Enable per-request query logging (name, type, rcode, ttl, latency) |

### Examples

```bash
# Default — global preset
./doh

# CN regional preset
./doh -region cn

# Explicit upstream list
./doh -upstreams "https://cloudflare-dns.com/dns-query,https://dns.google/dns-query"

# Auth + rate limiting
./doh -region us -token "your-secret-token" -rate-limit 20 -rate-burst 40
```

### Query examples

```bash
# GET
curl -s "http://127.0.0.1:8053/dns-query?dns=AAABAAABAAAAAAAAB2V4YW1wbGUDY29tAAABAAE" \
  -H "Accept: application/dns-message" \
  --output - | xxd | head

# POST
curl -s -X POST "http://127.0.0.1:8053/dns-query" \
  -H "Content-Type: application/dns-message" \
  -H "Accept: application/dns-message" \
  --data-binary @query.bin --output - | xxd | head

# Health check
curl http://127.0.0.1:8053/healthz
```

## Deployment

### systemd (bare metal / VPS)

Copy the binary and install the service:

```bash
sudo cp doh /usr/local/bin/doh
sudo chmod 755 /usr/local/bin/doh
sudo cp doh.service /etc/systemd/system/doh.service
```

Edit `/etc/systemd/system/doh.service` to set your flags (token, region, etc.), then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now doh
sudo systemctl status doh
```

To verify the hardening score:

```bash
systemd-analyze security doh.service
```

Logs:

```bash
journalctl -u doh -f
```

### Docker

```bash
# Build
docker build --build-arg VERSION=$(git describe --tags --always) -t doh .

# Run
docker run -d --name doh -p 8053:8053 doh \
  -region global -rate-limit 20 -rate-burst 40
```

### Docker Compose

```bash
VERSION=$(git describe --tags --always) docker compose up -d
docker compose ps
docker compose logs -f
```

To customise flags, edit the `command:` section in `docker-compose.yml`.

### Serverless (multi-platform)

The repository includes native serverless entrypoints and templates:

- Vercel Functions (Go): `api/dns-query/index.go`, `api/healthz/index.go`, `vercel.json`
- Cloudflare Workers: `deploy/cloudflare/worker.mjs`, `deploy/cloudflare/wrangler.jsonc.example`
- Netlify Functions (Go): `deploy/netlify/functions/*`, `netlify.toml`
- Railway config file: `railway.json`
- Shared env-driven app bootstrap: `relay/relay.go`

Common environment variables:

| Variable                  | Default   | Description                                      |
| ------------------------- | --------- | ------------------------------------------------ |
| `DOH_REGION`              | `global`  | Upstream preset: `global`, `us`, `kr`, `cn`      |
| `DOH_UPSTREAMS`           | _(empty)_ | Comma-separated upstream list (overrides region) |
| `DOH_CACHE_TTL`           | `10m`     | Cache TTL upper bound                            |
| `DOH_BATCH_SIZE`          | `3`       | Concurrent upstreams per round                   |
| `DOH_UPSTREAM_TIMEOUT`    | `4s`      | Upstream timeout for Go runtime targets          |
| `DOH_UPSTREAM_TIMEOUT_MS` | `4000`    | Upstream timeout for Cloudflare Worker           |
| `DOH_TOKEN`               | _(empty)_ | Bearer token                                     |
| `DOH_RATE_LIMIT`          | `0`       | Per-IP RPS (Go runtime targets only)             |
| `DOH_RATE_BURST`          | `0`       | Token bucket burst (Go runtime targets only)     |
| `DOH_DEBUG`               | `false`   | Enable debug logs                                |
| `DOH_EDGE_CACHE`          | `false`   | Cloudflare Worker Cache API toggle (GET only)    |

#### Vercel Functions (Go)

```bash
# First deployment
vercel

# Production deployment
vercel --prod
```

Best practices:

- Keep serverless handlers stateless. In-memory cache/rate-limit are per instance, not global.
- Vercel filesystem is read-only except `/tmp` (ephemeral, size-limited), so do not persist state to local disk.
- Keep upstream timeout conservative (for example 2s-4s) to reduce function duration and retries.

#### Cloudflare Workers

```bash
cd deploy/cloudflare
cp wrangler.jsonc.example wrangler.jsonc
wrangler secret put DOH_TOKEN
wrangler deploy
```

Best practices:

- Cloudflare Workers do not provide native `net/http` Go runtime; this repo provides a Worker template for edge/serverless use.
- Worker memory is ephemeral per isolate; do not rely on process-local token buckets for global throttling.
- Cache API is data-center local. Enable `DOH_EDGE_CACHE=true` only when local edge caching behavior is acceptable.
- For upstream-heavy workloads, consider `placement.mode = "smart"` in Wrangler config.

#### Netlify Functions (Go)

```bash
# First deployment
netlify deploy

# Production deployment
netlify deploy --prod
```

Best practices:

- Keep function code stateless; memory cache/rate-limit are per instance.
- For custom Go builds in Netlify, target Linux amd64 (`GOOS=linux`, `GOARCH=amd64`) per Netlify runtime requirements.
- Use `netlify.toml` redirects so clients can call `/dns-query` and `/healthz` directly.

#### Container serverless targets

The existing image works directly with Railway / Cloud Run / AWS Lambda container image mode.

Railway:

```bash
# Use repository default railway.json
cat railway.json
```

Best practices:

- Railway injects `PORT`; this project now auto-binds to `:$PORT` when `-addr` is not explicitly set.
- Keep `healthcheckPath` as `/healthz` for reliable rolling deploy checks.

Cloud Run example:

```bash
gcloud run deploy doh \
  --image docker.io/haiyon/doh:v0.1.1 \
  --region us-central1 \
  --allow-unauthenticated \
  --set-env-vars DOH_REGION=global
```

AWS Lambda (container image) example:

```bash
aws lambda create-function \
  --function-name doh-relay \
  --package-type Image \
  --code ImageUri=docker.io/haiyon/doh:v0.1.1 \
  --role arn:aws:iam::<account-id>:role/<lambda-exec-role>
```

Official references:

- Vercel Go runtime: <https://vercel.com/docs/functions/runtimes/go>
- Vercel runtime environment (`/tmp`): <https://vercel.com/docs/functions/runtimes>
- Cloudflare supported languages/runtimes: <https://developers.cloudflare.com/workers/languages/>
- Cloudflare Wrangler configuration: <https://developers.cloudflare.com/workers/wrangler/configuration/>
- Cloudflare Cache API behavior: <https://developers.cloudflare.com/workers/runtime-apis/cache/>
- Cloudflare Smart Placement: <https://developers.cloudflare.com/workers/configuration/smart-placement/>
- Netlify Go Functions: <https://docs.netlify.com/build/functions/languages/go/>
- Netlify custom Go build requirements: <https://docs.netlify.com/build/functions/languages/go/#custom-builds>
- Railway Dockerfile deploy: <https://docs.railway.com/guides/dockerfiles>
- Railway config as code: <https://docs.railway.com/reference/config-as-code>
- Go module layout: <https://go.dev/doc/modules/layout>
- Cloud Run request concurrency: <https://cloud.google.com/run/docs/about-concurrency>
- AWS Lambda best practices: <https://docs.aws.amazon.com/lambda/latest/dg/best-practices.html>
- AWS Lambda container images: <https://docs.aws.amazon.com/lambda/latest/dg/images-create.html>

### TLS / reverse proxy

The server speaks plain HTTP. Put it behind nginx or Caddy for TLS termination — DoH clients require HTTPS.

**nginx example:**

```nginx
server {
    listen 443 ssl http2;
    server_name doh.example.com;

    ssl_certificate     /etc/letsencrypt/live/doh.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/doh.example.com/privkey.pem;

    location /dns-query {
        proxy_pass http://127.0.0.1:8053;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

**Caddy example:**

```text
doh.example.com {
    reverse_proxy /dns-query localhost:8053
}
```

## Regional Upstream Presets

| Region        | Flag     | Providers                                                                                                 |
| ------------- | -------- | --------------------------------------------------------------------------------------------------------- |
| Global        | `global` | Cloudflare, Google, OpenDNS, NextDNS, AdGuard, ControlD, IIJ, DNS.SB, Wikimedia, FFMUC, RethinkDNS, Quad9 |
| United States | `us`     | Cloudflare, Google, OpenDNS, NextDNS, ControlD, RethinkDNS, Quad9                                         |
| Korea         | `kr`     | Cloudflare, Google, Tiar.app, IIJ, DNS.SB, NextDNS, AdGuard, RethinkDNS                                   |
| China         | `cn`     | DNSPod (doh.pub), Alibaba DNS, 360 DNS, DNSPod SM2                                                        |

The `-upstreams` flag always takes precedence over `-region`.

## Architecture

```text
Client
  │
  ▼
HTTP Server
  ├─ /healthz  ──────────────────────────────────► 200 OK (liveness)
  │
  └─ /dns-query
       ├─ Rate limit check (per real IP)
       ├─ Bearer token check
       │
       ├─ Cache HIT  ─────────────────────────────► Response
       │
       └─ Cache MISS
            ├─ Batch 1: upstream[0..N]  (concurrent)
            │     └─ first NOERROR ──► Cache (DNS TTL) ──► Response
            ├─ Batch 2: upstream[N..2N]  (concurrent)
            │     └─ first NOERROR ──► Cache (DNS TTL) ──► Response
            └─ ... all batches exhausted
                  └─ first non-error response  OR  502
```

## Project Structure

```text
doh/
├── main.go
├── api/
│   ├── dns-query/index.go     # Vercel function entrypoint (/dns-query)
│   ├── healthz/index.go       # Vercel function entrypoint (/healthz)
│   └── shared/runtime.go      # Vercel shared lazy init runtime
├── relay/
│   └── relay.go               # Shared env-based app bootstrap
├── deploy/
│   ├── cloudflare/            # Worker + Wrangler template
│   ├── netlify/functions/     # Netlify Go function entrypoints
│   └── README.md              # Deployment layout notes
├── netlify.toml               # Netlify redirects and function directory
├── railway.json               # Railway deployment config (config-as-code)
├── vercel.json
├── handler/
├── upstream/
├── cache/
├── ratelimit/
└── health/
```

## License

MIT
