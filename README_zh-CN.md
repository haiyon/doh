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
- POST 请求体大小限制（64 KiB）
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

| 参数                | 默认值     | 说明                                          |
| ------------------- | ---------- | --------------------------------------------- |
| `-addr`             | `:8053`    | HTTP 监听地址                                 |
| `-cache-ttl`        | `10m`      | 缓存最大有效期（以 DNS 记录 TTL 为上限）      |
| `-batch-size`       | `3`        | 每轮并发查询的上游数量                        |
| `-upstream-timeout` | `4s`       | 单个上游 HTTP 请求超时时间                    |
| `-region`           | `global`   | 上游预设区域：`global`、`us`、`kr`、`cn`      |
| `-upstreams`        | _（禁用）_ | 逗号分隔的上游 DoH URL 列表（覆盖 `-region`） |
| `-token`            | _（禁用）_ | `Authorization` 头中要求的 Bearer Token       |
| `-rate-limit`       | `0`        | 每 IP 每秒最大请求数（`0` 表示禁用）          |
| `-rate-burst`       | `0`        | 令牌桶突发容量（默认等于 `-rate-limit`）      |
| `-debug`            | `false`    | 启用每请求查询日志（域名、类型、rcode、ttl、延迟） |

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

```
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
