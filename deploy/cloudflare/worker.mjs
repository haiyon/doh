const CONTENT_TYPE_DNS = "application/dns-message";
const MAX_BODY_SIZE = 65535; // DNS wire-format message maximum size.
const DNS_RCODE_NOERROR = 0;

const PRESETS = {
  global: [
    "https://cloudflare-dns.com/dns-query",
    "https://dns.google/dns-query",
    "https://doh.opendns.com/dns-query",
    "https://dns.nextdns.io/dns-query",
    "https://unfiltered.adguard-dns.com/dns-query",
    "https://freedns.controld.com/p0",
    "https://public.dns.iij.jp/dns-query",
    "https://doh.dns.sb/dns-query",
    "https://wikimedia-dns.org/dns-query",
    "https://doh.ffmuc.net/dns-query",
    "https://sky.rethinkdns.com/dns-query",
    "https://dns.quad9.net/dns-query"
  ],
  us: [
    "https://cloudflare-dns.com/dns-query",
    "https://dns.google/dns-query",
    "https://doh.opendns.com/dns-query",
    "https://dns.nextdns.io/dns-query",
    "https://freedns.controld.com/p0",
    "https://sky.rethinkdns.com/dns-query",
    "https://dns.quad9.net/dns-query"
  ],
  kr: [
    "https://cloudflare-dns.com/dns-query",
    "https://dns.google/dns-query",
    "https://jp.tiar.app/dns-query",
    "https://public.dns.iij.jp/dns-query",
    "https://doh.dns.sb/dns-query",
    "https://dns.nextdns.io/dns-query",
    "https://unfiltered.adguard-dns.com/dns-query",
    "https://sky.rethinkdns.com/dns-query"
  ],
  cn: [
    "https://doh.pub/dns-query",
    "https://dns.alidns.com/dns-query",
    "https://doh.360.cn/dns-query",
    "https://sm2.doh.pub/dns-query"
  ]
};

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);

    if (url.pathname === "/healthz") {
      return json(200, { status: "ok" });
    }

    if (url.pathname !== "/dns-query") {
      return json(404, { error: { code: 404, message: "Not Found" } });
    }

    const token = (env.DOH_TOKEN || "").trim();
    if (token) {
      const auth = request.headers.get("Authorization") || "";
      if (!auth.startsWith("Bearer ") || auth.slice(7) !== token) {
        return json(401, { error: { code: 401, message: "Unauthorized" } }, {
          "WWW-Authenticate": 'Bearer realm="doh"'
        });
      }
    }

    const method = request.method.toUpperCase();
    let dnsParam = "";
    let bodyBytes = null;

    if (method === "GET") {
      dnsParam = url.searchParams.get("dns") || "";
      if (!dnsParam) {
        return json(400, { error: { code: 400, message: "Bad Request: missing dns query parameter" } });
      }
      try {
        const raw = decodeBase64URL(dnsParam);
        if (raw.byteLength > MAX_BODY_SIZE) {
          return json(413, {
            error: { code: 413, message: `Request Entity Too Large: dns parameter exceeds ${MAX_BODY_SIZE} bytes` }
          });
        }
      } catch {
        return json(400, { error: { code: 400, message: "Bad Request: dns parameter is not valid base64url" } });
      }
    } else if (method === "POST") {
      const ct = request.headers.get("Content-Type") || "";
      const mediaType = ct.split(";")[0].trim().toLowerCase();
      if (mediaType !== CONTENT_TYPE_DNS) {
        return json(415, {
          error: {
            code: 415,
            message: "Unsupported Media Type: Content-Type must be application/dns-message"
          }
        });
      }
      const buf = await request.arrayBuffer();
      if (buf.byteLength === 0) {
        return json(400, { error: { code: 400, message: "Bad Request: empty POST body" } });
      }
      if (buf.byteLength > MAX_BODY_SIZE) {
        return json(413, {
          error: { code: 413, message: `Request Entity Too Large: body exceeds ${MAX_BODY_SIZE} bytes` }
        });
      }
      bodyBytes = new Uint8Array(buf);
      dnsParam = encodeBase64URL(bodyBytes);
    } else {
      return json(405, { error: { code: 405, message: "Method Not Allowed" } }, { Allow: "GET, POST" });
    }

    const debug = parseBool(env.DOH_DEBUG, false);
    const upstreams = resolveUpstreams(env);
    const batchSize = Math.max(1, parseIntSafe(env.DOH_BATCH_SIZE, 3));
    const timeoutMs = Math.max(100, parseIntSafe(env.DOH_UPSTREAM_TIMEOUT_MS, 4000));

    const edgeCacheEnabled = method === "GET" && parseBool(env.DOH_EDGE_CACHE, false);
    const cacheKey = new Request(`${url.origin}/dns-query?dns=${dnsParam}`, {
      method: "GET",
      headers: { Accept: CONTENT_TYPE_DNS }
    });

    if (edgeCacheEnabled) {
      const cached = await caches.default.match(cacheKey);
      if (cached) {
        const headers = new Headers(cached.headers);
        headers.set("X-Cache", "HIT");
        headers.set("X-Edge-Cache", "HIT");
        return new Response(cached.body, { status: cached.status, headers });
      }
    }

    let firstFailure = null;
    for (let i = 0; i < upstreams.length; i += batchSize) {
      const batch = upstreams.slice(i, i + batchSize);
      const batchResult = await queryBatch({
        batch,
        method,
        dnsParam,
        bodyBytes,
        timeoutMs,
        debug
      });

      if (!firstFailure && batchResult.firstFailure) {
        firstFailure = batchResult.firstFailure;
      }
      if (batchResult.noerror) {
        const ttl = dnsTTL(batchResult.noerror);
        const headers = new Headers({
          "Content-Type": CONTENT_TYPE_DNS,
          "X-Cache": "MISS",
          "X-Edge-Cache": "MISS",
          "Cache-Control": ttl > 0 ? `max-age=${ttl}` : "no-cache"
        });
        const out = new Response(batchResult.noerror, { status: 200, headers });
        if (edgeCacheEnabled && ttl > 0) {
          ctx.waitUntil(caches.default.put(cacheKey, out.clone()));
        }
        return out;
      }
    }

    if (firstFailure) {
      const ttl = dnsTTL(firstFailure);
      return new Response(firstFailure, {
        status: 200,
        headers: {
          "Content-Type": CONTENT_TYPE_DNS,
          "X-Cache": "MISS",
          "X-Edge-Cache": "MISS",
          "Cache-Control": ttl > 0 ? `max-age=${ttl}` : "no-cache"
        }
      });
    }

    return json(502, { error: { code: 502, message: "All upstream DoH resolvers failed" } });
  }
};

async function queryBatch({ batch, method, dnsParam, bodyBytes, timeoutMs, debug }) {
  let firstFailure = null;
  const controllers = batch.map(() => new AbortController());
  let inflight = batch.map((upstreamURL, idx) => ({
    idx,
    task: queryOne({
      upstreamURL,
      method,
      dnsParam,
      bodyBytes,
      timeoutMs,
      debug,
      externalSignal: controllers[idx].signal
    }).then((res) => ({ idx, res }))
  }));

  while (inflight.length > 0) {
    const { idx, res } = await Promise.race(inflight.map((item) => item.task));
    inflight = inflight.filter((item) => item.idx !== idx);
    if (res.error) {
      continue;
    }
    if (res.rcode === DNS_RCODE_NOERROR) {
      controllers.forEach((controller, i) => {
        if (i !== idx) {
          controller.abort();
        }
      });
      return { noerror: res.body, firstFailure };
    }
    if (!firstFailure) {
      firstFailure = res.body;
    }
  }

  return { noerror: null, firstFailure };
}

async function queryOne({ upstreamURL, method, dnsParam, bodyBytes, timeoutMs, debug, externalSignal }) {
  let url = upstreamURL;
  const headers = { Accept: CONTENT_TYPE_DNS };
  const init = { method, headers };

  const controller = new AbortController();
  let timedOut = false;
  let onExternalAbort = null;
  if (externalSignal) {
    onExternalAbort = () => controller.abort();
    if (externalSignal.aborted) {
      controller.abort();
    } else {
      externalSignal.addEventListener("abort", onExternalAbort, { once: true });
    }
  }
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  init.signal = controller.signal;

  try {
    if (method === "GET") {
      const target = new URL(upstreamURL);
      target.searchParams.set("dns", dnsParam);
      url = target.toString();
    } else {
      headers["Content-Type"] = CONTENT_TYPE_DNS;
      init.body = bodyBytes;
    }

    const resp = await fetch(url, init);
    if (resp.status !== 200) {
      if (debug) {
        console.log(`[debug] upstream ${upstreamURL} err=HTTP_${resp.status}`);
      }
      return { error: new Error(`HTTP ${resp.status}`) };
    }
    const body = new Uint8Array(await resp.arrayBuffer());
    if (body.byteLength > MAX_BODY_SIZE) {
      if (debug) {
        console.log(`[debug] upstream ${upstreamURL} err=response_too_large`);
      }
      return { error: new Error("response too large") };
    }
    const rcode = dnsRcode(body);
    if (debug) {
      console.log(`[debug] upstream ${upstreamURL} rcode=${rcode}`);
    }
    return { body, rcode };
  } catch (err) {
    if (debug) {
      if (timedOut) {
        console.log(`[debug] upstream ${upstreamURL} err=timeout`);
      } else if (controller.signal.aborted) {
        console.log(`[debug] upstream ${upstreamURL} err=aborted`);
      } else {
        console.log(`[debug] upstream ${upstreamURL} err=${String(err)}`);
      }
    }
    return { error: err };
  } finally {
    clearTimeout(timer);
    if (externalSignal && onExternalAbort) {
      externalSignal.removeEventListener("abort", onExternalAbort);
    }
  }
}

function resolveUpstreams(env) {
  const override = (env.DOH_UPSTREAMS || "").trim();
  if (override) {
    const list = override
      .split(",")
      .map((v) => v.trim())
      .filter(Boolean);
    if (list.length > 0) {
      return shuffle(list);
    }
  }

  const region = (env.DOH_REGION || "global").trim().toLowerCase();
  return shuffle([...(PRESETS[region] || PRESETS.global)]);
}

function shuffle(list) {
  for (let i = list.length - 1; i > 0; i -= 1) {
    const j = Math.floor(Math.random() * (i + 1));
    [list[i], list[j]] = [list[j], list[i]];
  }
  return list;
}

function dnsRcode(msg) {
  if (!msg || msg.byteLength < 4) {
    return 2;
  }
  return msg[3] & 0x0f;
}

function dnsTTL(msg) {
  if (!msg || msg.byteLength < 12) {
    return 0;
  }
  const anCount = (msg[6] << 8) | msg[7];
  const nsCount = (msg[8] << 8) | msg[9];
  const arCount = (msg[10] << 8) | msg[11];
  const total = anCount + nsCount + arCount;
  if (total === 0) {
    return 0;
  }

  let offset = 12;
  const qdCount = (msg[4] << 8) | msg[5];
  for (let i = 0; i < qdCount; i += 1) {
    offset = skipName(msg, offset);
    if (offset < 0 || offset + 4 > msg.byteLength) {
      return 0;
    }
    offset += 4;
  }

  let minTTL = 0xffffffff;
  let found = false;
  for (let i = 0; i < total; i += 1) {
    offset = skipName(msg, offset);
    if (offset < 0 || offset + 10 > msg.byteLength) {
      return 0;
    }
    const ttl =
      (msg[offset + 4] << 24) |
      (msg[offset + 5] << 16) |
      (msg[offset + 6] << 8) |
      msg[offset + 7];
    const rdLen = (msg[offset + 8] << 8) | msg[offset + 9];
    offset += 10 + rdLen;
    if (offset > msg.byteLength) {
      return 0;
    }
    if (ttl < minTTL) {
      minTTL = ttl >>> 0;
      found = true;
    }
  }

  return found ? minTTL : 0;
}

function skipName(msg, offset) {
  while (true) {
    if (offset >= msg.byteLength) {
      return -1;
    }
    const length = msg[offset];
    if (length === 0) {
      return offset + 1;
    }
    if ((length & 0xc0) === 0xc0) {
      return offset + 2;
    }
    if ((length & 0xc0) !== 0) {
      return -1;
    }
    offset += 1 + length;
  }
}

function decodeBase64URL(input) {
  const base64 = input.replace(/-/g, "+").replace(/_/g, "/");
  const padding = "=".repeat((4 - (base64.length % 4 || 4)) % 4);
  const bin = atob(base64 + padding);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i += 1) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

function encodeBase64URL(bytes) {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 1) {
    bin += String.fromCharCode(bytes[i]);
  }
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

function parseIntSafe(v, fallback) {
  const n = Number.parseInt(String(v ?? ""), 10);
  return Number.isFinite(n) ? n : fallback;
}

function parseBool(v, fallback) {
  if (v === undefined || v === null || String(v).trim() === "") {
    return fallback;
  }
  const s = String(v).trim().toLowerCase();
  return s === "1" || s === "true" || s === "yes" || s === "on";
}

function json(code, payload, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status: code,
    headers: {
      "Content-Type": "application/json; charset=utf-8",
      ...headers
    }
  });
}
