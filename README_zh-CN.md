# DoH

基于 Go 语言实现的 DNS-over-HTTPS（DoH）中继服务器，符合 [RFC 8484](https://datatracker.ietf.org/doc/html/rfc8484) 规范。

接收客户端发送的标准 DoH 查询（GET 与 POST），并发地将请求转发至可配置的上游解析器池，将首个有效的 `NOERROR` 响应返回给客户端并写入内存缓存。

## 功能特性

- 符合 RFC 8484 规范，支持 GET 与 POST，含 Content-Type 与 base64url 校验
- 内置区域上游预设：`global`、`us`、`kr`、`cn`
- 通过参数覆盖区域预设，指定自定义上游列表
- 可配置批次大小的并发上游转发
- 基于 DNS 记录 TTL 的内存响应缓存，含后台过期清理
- 基于令牌桶的每 IP 速率限制，支持 `X-Forwarded-For` / `X-Real-IP`
- Bearer Token 身份认证
- 每个上游独立超时控制，支持优雅降级
- 请求载荷大小限制（GET/POST 解码后 DNS 线格式均为 65,535 字节）
- 上游连接支持 HTTP/2
- 响应携带基于 DNS TTL 的 `Cache-Control` 头
- `/healthz` 存活探针端点
- 支持 `SIGINT` / `SIGTERM` 信号的优雅关闭
- 单一静态二进制，无运行时依赖

## 环境要求

- Go 1.22 或更高版本

## 构建

```bash
git clone https://github.com/haiyon/doh.git
cd doh
go build -trimpath -ldflags="-s -w -buildid= -X main.version=$(git describe --tags --always)" -o doh .
```

## 使用方法

```text
./doh [参数]
```

### 命令行参数

| 参数                | 默认值              | 说明                                               |
| ------------------- | ------------------- | -------------------------------------------------- |
| `-addr`             | `:$PORT` 或 `:8053` | HTTP 监听地址（设置 `PORT` 环境变量时优先使用）    |
| `-cache-ttl`        | `10m`               | 缓存最大有效期（以 DNS 记录 TTL 为上限）           |
| `-batch-size`       | `3`                 | 每轮并发查询的上游数量                             |
| `-upstream-timeout` | `4s`                | 单个上游 HTTP 请求超时时间                         |
| `-region`           | `global`            | 上游预设区域：`global`、`us`、`kr`、`cn`           |
| `-upstreams`        | _（禁用）_          | 逗号分隔的上游 DoH URL 列表（覆盖 `-region`）      |
| `-token`            | _（禁用）_          | `Authorization` 头中要求的 Bearer Token            |
| `-rate-limit`       | `0`                 | 每 IP 每秒最大请求数（`0` 表示禁用）               |
| `-rate-burst`       | `0`                 | 令牌桶突发容量（默认等于 `-rate-limit`）           |
| `-debug`            | `false`             | 启用每请求查询日志（域名、类型、rcode、ttl、延迟） |

### 使用示例

```bash
# 默认 — 全球预设
./doh

# CN 区域预设
./doh -region cn

# 显式指定上游列表
./doh -upstreams "https://cloudflare-dns.com/dns-query,https://dns.google/dns-query"

# 启用认证与速率限制
./doh -region us -token "your-secret-token" -rate-limit 20 -rate-burst 40
```

### 查询示例

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

# 健康检查
curl http://127.0.0.1:8053/healthz
```

## 部署

### systemd（裸机 / VPS）

复制二进制文件并安装服务：

```bash
sudo cp doh /usr/local/bin/doh
sudo chmod 755 /usr/local/bin/doh
sudo cp doh.service /etc/systemd/system/doh.service
```

编辑 `/etc/systemd/system/doh.service` 中的启动参数（token、region 等），然后：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now doh
sudo systemctl status doh
```

验证安全加固评分：

```bash
systemd-analyze security doh.service
```

查看日志：

```bash
journalctl -u doh -f
```

### Docker

```bash
# 构建
docker build --build-arg VERSION=$(git describe --tags --always) -t doh .

# 运行
docker run -d --name doh -p 8053:8053 doh \
  -region global -rate-limit 20 -rate-burst 40
```

### Docker Compose

```bash
VERSION=$(git describe --tags --always) docker compose up -d
docker compose ps
docker compose logs -f
```

如需修改启动参数，编辑 `docker-compose.yml` 中的 `command:` 部分。

### Serverless（多平台）

仓库内已提供可直接使用的 serverless 入口与模板：

- Vercel Functions（Go）：`api/dns-query/index.go`、`api/healthz/index.go`、`vercel.json`
- Cloudflare Workers：`deploy/cloudflare/worker.mjs`、`deploy/cloudflare/wrangler.jsonc.example`
- Netlify Functions（Go）：`deploy/netlify/functions/*`、`netlify.toml`
- Railway 配置文件：`railway.json`
- 统一环境变量启动层：`relay/relay.go`

通用环境变量：

| 变量                      | 默认值   | 说明                                       |
| ------------------------- | -------- | ------------------------------------------ |
| `DOH_REGION`              | `global` | 上游预设：`global`、`us`、`kr`、`cn`       |
| `DOH_UPSTREAMS`           | _空_     | 逗号分隔上游列表（覆盖区域预设）           |
| `DOH_CACHE_TTL`           | `10m`    | 缓存 TTL 上限                              |
| `DOH_BATCH_SIZE`          | `3`      | 每轮并发上游数量                           |
| `DOH_UPSTREAM_TIMEOUT`    | `4s`     | Go 运行时目标的上游超时                    |
| `DOH_UPSTREAM_TIMEOUT_MS` | `4000`   | Cloudflare Worker 的上游超时               |
| `DOH_TOKEN`               | _空_     | Bearer Token                               |
| `DOH_RATE_LIMIT`          | `0`      | 每 IP 每秒请求数（仅 Go 运行时目标）       |
| `DOH_RATE_BURST`          | `0`      | 令牌桶突发（仅 Go 运行时目标）             |
| `DOH_DEBUG`               | `false`  | 开启调试日志                               |
| `DOH_EDGE_CACHE`          | `false`  | Cloudflare Worker Cache API 开关（仅 GET） |

#### Vercel Functions（Go）

```bash
# 首次部署
vercel

# 生产部署
vercel --prod
```

最佳实践：

- 函数应保持无状态；内存缓存和限流都只是“单实例级别”，不是全局一致。
- Vercel 文件系统默认只读，仅 `/tmp` 可写且为临时空间，不要依赖本地持久化。
- 上游超时建议保守设置（例如 2s-4s），降低函数时长与重试概率。

#### Cloudflare Workers

```bash
cd deploy/cloudflare
cp wrangler.jsonc.example wrangler.jsonc
wrangler secret put DOH_TOKEN
wrangler deploy
```

最佳实践：

- Cloudflare Workers 不提供原生 `net/http` 的 Go 运行时；本仓库提供 Worker 模板用于边缘/函数场景。
- Worker 内存为隔离实例临时态，不应依赖进程内令牌桶做全局限流。
- Cache API 是数据中心本地缓存；仅在可接受本地边缘缓存特性时开启 `DOH_EDGE_CACHE=true`。
- 对上游请求密集场景，可考虑在 Wrangler 中启用 `placement.mode = "smart"`。

#### Netlify Functions（Go）

```bash
# 首次部署
netlify deploy

# 生产部署
netlify deploy --prod
```

最佳实践：

- 函数保持无状态；内存缓存和限流仅在单实例内生效。
- 若使用 Netlify 自定义 Go 构建，按官方要求设置 Linux amd64（`GOOS=linux`、`GOARCH=amd64`）。
- 通过 `netlify.toml` 的 redirect，客户端可直接使用 `/dns-query` 与 `/healthz`。

#### 容器型 Serverless 平台

现有镜像可直接用于 Railway / Cloud Run / AWS Lambda 容器镜像模式。

Railway：

```bash
# 直接使用仓库内默认 railway.json
cat railway.json
```

最佳实践：

- Railway 会注入 `PORT`；本项目在未显式设置 `-addr` 时会自动监听 `:$PORT`。
- 部署健康检查建议固定为 `/healthz`，便于滚动发布判定。

Cloud Run 示例：

```bash
gcloud run deploy doh \
  --image docker.io/haiyon/doh:v0.1.1 \
  --region us-central1 \
  --allow-unauthenticated \
  --set-env-vars DOH_REGION=global
```

AWS Lambda（容器镜像）示例：

```bash
aws lambda create-function \
  --function-name doh-relay \
  --package-type Image \
  --code ImageUri=docker.io/haiyon/doh:v0.1.1 \
  --role arn:aws:iam::<account-id>:role/<lambda-exec-role>
```

官方参考文档：

- Vercel Go runtime：<https://vercel.com/docs/functions/runtimes/go>
- Vercel 运行时环境（`/tmp`）：<https://vercel.com/docs/functions/runtimes>
- Cloudflare 支持语言与运行时：<https://developers.cloudflare.com/workers/languages/>
- Cloudflare Wrangler 配置：<https://developers.cloudflare.com/workers/wrangler/configuration/>
- Cloudflare Cache API 行为：<https://developers.cloudflare.com/workers/runtime-apis/cache/>
- Cloudflare Smart Placement：<https://developers.cloudflare.com/workers/configuration/smart-placement/>
- Netlify Go Functions：<https://docs.netlify.com/build/functions/languages/go/>
- Netlify 自定义 Go 构建要求：<https://docs.netlify.com/build/functions/languages/go/#custom-builds>
- Railway Dockerfile 部署：<https://docs.railway.com/guides/dockerfiles>
- Railway 配置即代码：<https://docs.railway.com/reference/config-as-code>
- Go module layout：<https://go.dev/doc/modules/layout>
- Cloud Run 并发说明：<https://cloud.google.com/run/docs/about-concurrency>
- AWS Lambda 最佳实践：<https://docs.aws.amazon.com/lambda/latest/dg/best-practices.html>
- AWS Lambda 容器镜像：<https://docs.aws.amazon.com/lambda/latest/dg/images-create.html>

### TLS / 反向代理

服务本身仅提供 HTTP。DoH 客户端要求 HTTPS，需在前端配置 nginx 或 Caddy 进行 TLS 终止。

**nginx 示例：**

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

**Caddy 示例：**

```text
doh.example.com {
    reverse_proxy /dns-query localhost:8053
}
```

## 区域上游预设

| 区域 | 参数值   | 服务商                                                                                                    |
| ---- | -------- | --------------------------------------------------------------------------------------------------------- |
| 全球 | `global` | Cloudflare、Google、OpenDNS、NextDNS、AdGuard、ControlD、IIJ、DNS.SB、Wikimedia、FFMUC、RethinkDNS、Quad9 |
| 美国 | `us`     | Cloudflare、Google、OpenDNS、NextDNS、ControlD、RethinkDNS、Quad9                                         |
| 韩国 | `kr`     | Cloudflare、Google、Tiar.app、IIJ、DNS.SB、NextDNS、AdGuard、RethinkDNS                                   |
| 中国 | `cn`     | DNSPod（doh.pub）、阿里 DNS、360 DNS、DNSPod SM2                                                          |

`-upstreams` 参数始终优先于 `-region`。

## 架构说明

```text
客户端
  │
  ▼
HTTP 服务器
  ├─ /healthz  ──────────────────────────────────► 200 OK（存活探针）
  │
  └─ /dns-query
       ├─ 速率限制检查（基于真实 IP）
       ├─ Bearer Token 校验
       │
       ├─ 缓存命中  ─────────────────────────────► 返回响应
       │
       └─ 缓存未命中
            ├─ 第 1 批：upstream[0..N]  （并发）
            │     └─ 首个 NOERROR ──► 缓存（DNS TTL）──► 返回响应
            ├─ 第 2 批：upstream[N..2N]  （并发）
            │     └─ 首个 NOERROR ──► 缓存（DNS TTL）──► 返回响应
            └─ ... 所有批次均已尝试
                  └─ 返回首个非错误响应  或  502
```

## 项目结构

```text
doh/
├── main.go              # 程序入口，参数解析，服务生命周期，健康探针
├── api/
│   ├── dns-query/index.go     # Vercel 函数入口（/dns-query）
│   ├── healthz/index.go       # Vercel 函数入口（/healthz）
│   └── shared/runtime.go      # Vercel 共享懒加载运行时
├── relay/
│   └── relay.go               # 统一环境变量启动层
├── deploy/
│   ├── cloudflare/            # Worker 与 Wrangler 模板
│   ├── netlify/functions/     # Netlify Go 函数入口
│   └── README.md              # 部署目录说明
├── netlify.toml               # Netlify 路由重写与函数目录
├── railway.json               # Railway 配置即代码文件
├── vercel.json
├── go.mod
├── go.sum
├── Dockerfile           # 多阶段构建 → scratch 镜像
├── docker-compose.yml
├── doh.service          # systemd 单元文件，含安全加固配置
├── cache/
│   └── cache.go         # 线程安全 TTL 缓存，支持按条目设置 TTL
├── handler/
│   └── handler.go       # HTTP 处理器，认证，上游并发转发，DNS TTL 解析
├── health/
│   └── health.go        # /healthz 存活探针端点
├── ratelimit/
│   └── ratelimit.go     # 每 IP 令牌桶速率限制，支持 X-Forwarded-For
└── upstream/
    └── upstream.go      # 区域上游预设与解析
```

## 许可证

MIT
