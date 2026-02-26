// Package handler implements the HTTP handler for the DoH relay endpoint.
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/haiyon/doh/cache"
	"github.com/haiyon/doh/ratelimit"
)

const (
	contentTypeDNS   = "application/dns-message"
	maxBodySize      = 65535 // DNS wire-format message maximum size (16-bit length field).
	dnsRcodeNoError  = 0
	dnsRcodeServFail = 2
)

// errorResponse is the JSON envelope returned for all HTTP-level errors.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Timestamp int64  `json:"timestamp"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
}

// upstreamResult carries the result of a single upstream query attempt.
type upstreamResult struct {
	body    []byte
	rcode   int
	err     error
	latency time.Duration
	url     string
}

// Config holds the dependencies and tuning parameters for Handler.
type Config struct {
	// Upstreams is the ordered list of DoH upstream URLs.
	Upstreams []string

	// Cache is the shared response cache.
	Cache *cache.Cache

	// BatchSize is the number of upstreams queried concurrently per round.
	BatchSize int

	// UpstreamTimeout is the per-upstream HTTP request deadline.
	UpstreamTimeout time.Duration

	// Token, when non-empty, requires clients to present a matching
	// Bearer token in the Authorization header.
	Token string

	// Limiter, when non-nil, enforces per-IP request rate limiting.
	Limiter *ratelimit.Limiter

	// Debug enables per-request query logging.
	Debug bool
}

// Handler is an http.Handler that proxies DoH queries to a pool of upstream resolvers.
type Handler struct {
	cfg    Config
	client *http.Client
}

// New constructs a Handler from cfg.
func New(cfg Config) *Handler {
	if cfg.UpstreamTimeout <= 0 {
		cfg.UpstreamTimeout = 4 * time.Second
	}

	maxIdleConns := max(len(cfg.Upstreams)*4, 32)

	transport := &http.Transport{
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: 16,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
	}
	return &Handler{
		cfg:    cfg,
		client: &http.Client{Transport: transport},
	}
}

// ServeHTTP implements http.Handler (RFC 8484 §4).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Limiter != nil && !h.cfg.Limiter.Allow(r) {
		writeError(w, http.StatusTooManyRequests, "Too Many Requests")
		return
	}

	if h.cfg.Token != "" {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token != h.cfg.Token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="doh"`)
			writeError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
	}

	switch r.Method {
	case http.MethodGet, http.MethodPost:
		h.handle(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "Method Not Allowed")
	}
}

// handle processes a validated GET or POST DoH request.
func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	dnsPayload, body, rawMsg, ok := h.parseRequest(w, r)
	if !ok {
		return
	}

	qname, qtype := ".", "UNKNOWN"
	if h.cfg.Debug {
		qname, qtype = dnsQuestion(rawMsg)
	}

	cacheKey := buildCacheKey(dnsPayload, body)
	if h.cfg.Cache != nil {
		if cached, found := h.cfg.Cache.Get(cacheKey); found {
			w.Header().Set("Content-Type", contentTypeDNS)
			w.Header().Set("X-Cache", "HIT")
			_, _ = w.Write(cached)
			if h.cfg.Debug {
				log.Printf("[debug] %s %s %s %s cache=HIT latency=%s",
					clientIP(r), r.Method, qname, qtype, time.Since(start).Round(time.Microsecond))
			}
			return
		}
	}

	resp, err := h.queryUpstreams(r.Context(), r.Method, dnsPayload, body)
	if err != nil {
		log.Printf("all upstreams failed: %v", err)
		if h.cfg.Debug {
			log.Printf("[debug] %s %s %s %s cache=MISS error=bad_gateway latency=%s",
				clientIP(r), r.Method, qname, qtype, time.Since(start).Round(time.Microsecond))
		}
		writeError(w, http.StatusBadGateway, "All upstream DoH resolvers failed")
		return
	}

	ttl := dnsTTL(resp)
	if h.cfg.Cache != nil {
		if ttl > 0 {
			h.cfg.Cache.SetWithTTL(cacheKey, resp, time.Duration(ttl)*time.Second)
		} else {
			h.cfg.Cache.Set(cacheKey, resp)
		}
	}

	w.Header().Set("Content-Type", contentTypeDNS)
	if ttl > 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", ttl))
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Cache", "MISS")
	_, _ = w.Write(resp)

	if h.cfg.Debug {
		rcode := dnsRcode(resp)
		log.Printf("[debug] %s %s %s %s cache=MISS rcode=%d ttl=%ds latency=%s",
			clientIP(r), r.Method, qname, qtype, rcode, ttl, time.Since(start).Round(time.Microsecond))
	}
}

// parseRequest extracts the DNS payload identifier and raw body from r.
// For GET, dnsPayload is the base64url-encoded dns parameter value.
// For POST, body contains the raw wire bytes directly.
func (h *Handler) parseRequest(w http.ResponseWriter, r *http.Request) (dnsPayload string, body, rawMsg []byte, ok bool) {
	if r.Method == http.MethodGet {
		dns := r.URL.Query().Get("dns")
		if dns == "" {
			writeError(w, http.StatusBadRequest, "Bad Request: missing dns query parameter")
			return "", nil, nil, false
		}
		// Validate the dns parameter is valid base64url (RFC 8484 §4.1).
		raw, err := base64.RawURLEncoding.DecodeString(dns)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Bad Request: dns parameter is not valid base64url")
			return "", nil, nil, false
		}
		if len(raw) > maxBodySize {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("Request Entity Too Large: dns parameter exceeds %d bytes", maxBodySize))
			return "", nil, nil, false
		}
		return dns, nil, raw, true
	}

	// RFC 8484 §4.1: POST Content-Type must be application/dns-message.
	ct := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || !strings.EqualFold(mediaType, contentTypeDNS) {
		writeError(w, http.StatusUnsupportedMediaType, "Unsupported Media Type: Content-Type must be application/dns-message")
		return "", nil, nil, false
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Bad Request: failed to read request body")
		return "", nil, nil, false
	}
	if len(data) > maxBodySize {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("Request Entity Too Large: body exceeds %d bytes", maxBodySize))
		return "", nil, nil, false
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "Bad Request: empty POST body")
		return "", nil, nil, false
	}
	return "", data, data, true
}

// queryUpstreams fans out the DNS query to upstreams in sequential batches.
// Within each batch all upstreams are queried concurrently. The first NOERROR
// response wins. If no batch yields NOERROR, the first non-error response is
// returned. Returns an error only when every upstream fails.
func (h *Handler) queryUpstreams(ctx context.Context, method, dnsPayload string, body []byte) ([]byte, error) {
	batchSize := h.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 3
	}

	var firstFailure []byte

	for i := 0; i < len(h.cfg.Upstreams); i += batchSize {
		end := min(i+batchSize, len(h.cfg.Upstreams))
		resp, failure, err := h.queryBatch(ctx, h.cfg.Upstreams[i:end], method, dnsPayload, body)
		if err == nil {
			return resp, nil
		}
		if failure != nil && firstFailure == nil {
			firstFailure = failure
		}
	}

	if firstFailure != nil {
		return firstFailure, nil
	}
	return nil, fmt.Errorf("all %d upstreams returned errors or timed out", len(h.cfg.Upstreams))
}

// queryBatch queries all upstreams in batch concurrently.
func (h *Handler) queryBatch(
	ctx context.Context,
	batch []string,
	method, dnsPayload string,
	body []byte,
) (noerrorResp []byte, firstFailure []byte, err error) {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan upstreamResult, len(batch))

	for _, up := range batch {
		go func(url string) {
			results <- h.queryOne(batchCtx, url, method, dnsPayload, body)
		}(up)
	}

	transportErrors := 0
	for range batch {
		res := <-results
		if res.err != nil {
			log.Printf("upstream error [%s]: %v", res.url, res.err)
			transportErrors++
			if h.cfg.Debug {
				log.Printf("[debug] upstream %s err=%v latency=%s", res.url, res.err, res.latency.Round(time.Microsecond))
			}
			continue
		}
		if h.cfg.Debug {
			log.Printf("[debug] upstream %s rcode=%d latency=%s", res.url, res.rcode, res.latency.Round(time.Microsecond))
		}
		if res.rcode == dnsRcodeNoError {
			cancel()
			return res.body, nil, nil
		}
		if firstFailure == nil {
			firstFailure = res.body
		}
	}

	if transportErrors == len(batch) {
		return nil, firstFailure, fmt.Errorf("all %d upstreams in batch failed", len(batch))
	}
	return nil, firstFailure, fmt.Errorf("no NOERROR response in batch")
}

// queryOne performs a single DoH request to url.
func (h *Handler) queryOne(ctx context.Context, url, method, dnsPayload string, body []byte) upstreamResult {
	reqCtx, cancel := context.WithTimeout(ctx, h.cfg.UpstreamTimeout)
	defer cancel()

	var req *http.Request
	var err error

	if method == http.MethodGet {
		targetURL, buildErr := buildUpstreamGETURL(url, dnsPayload)
		if buildErr != nil {
			return upstreamResult{url: url, err: fmt.Errorf("build GET URL: %w", buildErr)}
		}
		req, err = http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
	} else {
		req, err = http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", contentTypeDNS)
		}
	}
	if err != nil {
		return upstreamResult{url: url, err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Accept", contentTypeDNS)

	start := time.Now()
	resp, err := h.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return upstreamResult{url: url, err: fmt.Errorf("do request: %w", err), latency: latency}
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		if closeErr := resp.Body.Close(); closeErr != nil {
			return upstreamResult{url: url, err: fmt.Errorf("upstream HTTP %d: close body: %w", resp.StatusCode, closeErr), latency: latency}
		}
		return upstreamResult{url: url, err: fmt.Errorf("upstream HTTP %d", resp.StatusCode), latency: latency}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return upstreamResult{url: url, err: fmt.Errorf("read body: %w", err), latency: latency}
	}
	if closeErr != nil {
		return upstreamResult{url: url, err: fmt.Errorf("close body: %w", closeErr), latency: latency}
	}
	if len(data) > maxBodySize {
		return upstreamResult{url: url, err: fmt.Errorf("upstream response too large: >%d bytes", maxBodySize), latency: latency}
	}
	return upstreamResult{url: url, body: data, rcode: dnsRcode(data), latency: latency}
}

// dnsRcode extracts the 4-bit RCODE from a DNS wire-format message.
// Returns dnsRcodeServFail for messages shorter than 4 bytes.
func dnsRcode(msg []byte) int {
	if len(msg) < 4 {
		return dnsRcodeServFail
	}
	return int(msg[3] & 0x0f)
}

// dnsTTL extracts the minimum TTL from all resource records in a DNS wire-format
// message. Returns 0 if the message is malformed or contains no records.
// The caller should treat 0 as "do not cache" or apply a minimum floor.
func dnsTTL(msg []byte) uint32 {
	// DNS header is 12 bytes; need at least that plus counts.
	if len(msg) < 12 {
		return 0
	}

	anCount := int(msg[6])<<8 | int(msg[7])
	nsCount := int(msg[8])<<8 | int(msg[9])
	arCount := int(msg[10])<<8 | int(msg[11])
	total := anCount + nsCount + arCount
	if total == 0 {
		return 0
	}

	// Skip question section: qdcount questions.
	qdCount := int(msg[4])<<8 | int(msg[5])
	offset := 12
	for range qdCount {
		offset = skipName(msg, offset)
		if offset < 0 || offset+4 > len(msg) {
			return 0
		}
		offset += 4 // QTYPE + QCLASS
	}

	var minTTL uint32 = ^uint32(0)
	found := false

	for range total {
		offset = skipName(msg, offset)
		if offset < 0 || offset+10 > len(msg) {
			return 0
		}
		ttl := uint32(msg[offset+4])<<24 | uint32(msg[offset+5])<<16 |
			uint32(msg[offset+6])<<8 | uint32(msg[offset+7])
		rdLen := int(msg[offset+8])<<8 | int(msg[offset+9])
		offset += 10 + rdLen
		if offset > len(msg) {
			return 0
		}
		if ttl < minTTL {
			minTTL = ttl
			found = true
		}
	}

	if !found {
		return 0
	}
	return minTTL
}

// skipName advances offset past a DNS name (label sequence or pointer).
// Returns -1 on malformed input.
func skipName(msg []byte, offset int) int {
	for {
		if offset >= len(msg) {
			return -1
		}
		length := int(msg[offset])
		if length == 0 {
			return offset + 1
		}
		if length&0xc0 == 0xc0 {
			// Pointer: 2 bytes total.
			return offset + 2
		}
		if length&0xc0 != 0 {
			return -1
		}
		offset += 1 + length
	}
}

// buildCacheKey returns a hex string key derived from the DNS payload via xxHash.
// For GET requests, dnsPayload is the base64url dns parameter value.
// For POST requests, body contains the raw wire bytes.
func buildCacheKey(dnsPayload string, body []byte) string {
	h := xxhash.New()
	writeSegment(h, []byte(dnsPayload))
	writeSegment(h, body)
	return fmt.Sprintf("%016x", h.Sum64())
}

func writeSegment(h io.Writer, data []byte) {
	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(data)))
	_, _ = h.Write(lenBuf[:])
	if len(data) > 0 {
		_, _ = h.Write(data)
	}
}

func buildUpstreamGETURL(rawURL, dnsPayload string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("dns", dnsPayload)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// dnsQuestion extracts the query name and query type from a DNS wire-format message.
// Returns (".", "UNKNOWN") for malformed messages.
func dnsQuestion(msg []byte) (qname, qtype string) {
	if len(msg) < 12 {
		return ".", "UNKNOWN"
	}
	qdCount := int(msg[4])<<8 | int(msg[5])
	if qdCount == 0 {
		return ".", "UNKNOWN"
	}
	offset := 12
	// Decode the first QNAME label sequence.
	var name []byte
	for {
		if offset >= len(msg) {
			return ".", "UNKNOWN"
		}
		length := int(msg[offset])
		if length == 0 {
			offset++
			break
		}
		if length&0xc0 != 0 {
			// Pointer or reserved — skip gracefully.
			offset += 2
			break
		}
		if offset+1+length > len(msg) {
			return ".", "UNKNOWN"
		}
		if len(name) > 0 {
			name = append(name, '.')
		}
		name = append(name, msg[offset+1:offset+1+length]...)
		offset += 1 + length
	}
	if len(name) == 0 {
		qname = "."
	} else {
		qname = string(name)
	}
	// QTYPE is the first 2 bytes after QNAME.
	if offset+2 > len(msg) {
		return qname, "UNKNOWN"
	}
	qt := uint16(msg[offset])<<8 | uint16(msg[offset+1])
	return qname, qtypeName(qt)
}

// qtypeName maps common DNS QTYPE values to their string representation.
func qtypeName(qt uint16) string {
	switch qt {
	case 1:
		return "A"
	case 2:
		return "NS"
	case 5:
		return "CNAME"
	case 6:
		return "SOA"
	case 12:
		return "PTR"
	case 15:
		return "MX"
	case 16:
		return "TXT"
	case 28:
		return "AAAA"
	case 33:
		return "SRV"
	case 43:
		return "DS"
	case 46:
		return "RRSIG"
	case 48:
		return "DNSKEY"
	case 65:
		return "HTTPS"
	case 255:
		return "ANY"
	default:
		return fmt.Sprintf("TYPE%d", qt)
	}
}

// clientIP returns the real client IP from X-Forwarded-For, X-Real-IP, or RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// writeError writes a JSON error response with the given HTTP status code.
func writeError(w http.ResponseWriter, code int, message string) {
	data, err := json.Marshal(errorResponse{
		Error: errorDetail{
			Timestamp: time.Now().UnixMilli(),
			Code:      code,
			Message:   message,
		},
	})
	if err != nil {
		data = []byte(`{"error":{"timestamp":0,"code":500,"message":"Internal Server Error"}}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}
