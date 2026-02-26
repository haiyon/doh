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
- POST body size limit (64 KiB)
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

| Flag                | Default      | Description                                             |
| ------------------- | ------------ | ------------------------------------------------------- |
| `-addr`             | `:8053`      | HTTP listen address                                     |
| `-cache-ttl`        | `10m`        | Maximum cache TTL (capped by DNS record TTL)            |
| `-batch-size`       | `3`          | Upstreams queried concurrently per round                |
| `-upstream-timeout` | `4s`         | Per-upstream HTTP request timeout                       |
| `-region`           | `global`     | Upstream preset region: `global`, `us`, `kr`, `cn`      |
| `-upstreams`        | _(disabled)_ | Comma-separated upstream DoH URLs (overrides `-region`) |
| `-token`            | _(disabled)_ | Bearer token required in `Authorization` header         |
| `-rate-limit`       | `0`          | Max requests per second per IP (`0` = disabled)         |
| `-rate-burst`       | `0`          | Token bucket burst capacity (defaults to `-rate-limit`) |

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

```
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
