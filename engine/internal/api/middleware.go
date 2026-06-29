package api

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Security-middleware environment variables and their defaults. The middleware
// is OFF-by-default-open for auth (so the demo/dev profile needs no config) but
// always rate-limited with a reasonable default.
const (
	// EnvAPIToken is the static bearer token. Unset/empty disables auth entirely
	// (the open demo/dev default); when set, every routing request must present
	// `Authorization: Bearer <token>`.
	EnvAPIToken = "ROUTING_API_TOKEN"
	// EnvRateLimitRPS overrides the per-client steady-state request rate. A value
	// <= 0 disables rate limiting; an unparseable value falls back to the default.
	EnvRateLimitRPS = "ROUTING_RATE_LIMIT_RPS"
	// EnvRateLimitBurst overrides the per-client burst (token-bucket capacity).
	EnvRateLimitBurst = "ROUTING_RATE_LIMIT_BURST"

	defaultRateLimitRPS   = 20.0
	defaultRateLimitBurst = 40
)

// SecurityConfig is the resolved auth + rate-limit policy for the routing
// surface. Token == "" disables auth (open demo/dev default); RPS <= 0 disables
// rate limiting. It is built once at startup (LoadSecurityConfig) and handed to
// NewSecurityMiddleware.
type SecurityConfig struct {
	// Token is the expected bearer token. Empty ⇒ auth disabled (open).
	Token string
	// RPS is the per-client steady-state refill rate (tokens/second). <= 0 ⇒ rate
	// limiting disabled.
	RPS float64
	// Burst is the per-client token-bucket capacity (max instantaneous burst).
	Burst int
}

// LoadSecurityConfig reads the security policy from the environment, applying the
// documented defaults. It is called by the binary so main stays thin; it is a
// pure env read so it is also trivially testable. An unparseable RPS/BURST value
// falls back to its default rather than failing startup — a malformed knob should
// not take the whole surface down.
func LoadSecurityConfig() SecurityConfig {
	cfg := SecurityConfig{
		// Trim the configured token: the presented token is TrimSpace'd in
		// bearerToken, so a token configured with stray surrounding whitespace would
		// otherwise never match (a silent, permanent 401). Trim both sides so they agree.
		Token: strings.TrimSpace(os.Getenv(EnvAPIToken)),
		RPS:   defaultRateLimitRPS,
		Burst: defaultRateLimitBurst,
	}
	if v := strings.TrimSpace(os.Getenv(EnvRateLimitRPS)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.RPS = f
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvRateLimitBurst)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Burst = n
		}
	}
	return cfg
}

// NewSecurityMiddleware builds the auth + per-client rate-limit wrapper for the
// routing mux. It is applied around the API mux ONLY — the binary keeps
// /healthz, /readyz and /metrics outside it so liveness, readiness, and scrapes
// are never gated. Behaviour is fully driven by cfg:
//
//   - cfg.Token == "" ⇒ auth disabled: requests pass through unauthenticated and
//     the rate limiter (if enabled) keys on the client IP.
//   - cfg.Token != "" ⇒ require `Authorization: Bearer <token>`; a missing or
//     wrong token is a 401 (constant-time compared, no timing leak). The rate
//     limiter keys on the bearer token.
//   - cfg.RPS <= 0 ⇒ rate limiting disabled.
//
// The returned wrapper holds one shared, bounded token-bucket limiter across all
// requests, so it must be built ONCE and reused for every handler it fronts.
func NewSecurityMiddleware(cfg SecurityConfig, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	authEnabled := cfg.Token != ""

	var limiter *rateLimiter
	// effectiveBurst is the burst actually in force after the clamp, so the startup
	// log reflects what the limiter will really do rather than the raw config value.
	effectiveBurst := cfg.Burst
	if cfg.RPS > 0 {
		burst := float64(cfg.Burst)
		if burst < 1 {
			// A zero/negative burst would wedge the bucket shut and 429 everything;
			// clamp to at least one token so the limiter throttles rather than blocks.
			burst = 1
		}
		effectiveBurst = int(burst)
		limiter = newRateLimiter(cfg.RPS, burst)
	}

	if authEnabled {
		logger.Info("routing surface auth enabled", "rate_limit_rps", cfg.RPS, "rate_limit_burst", effectiveBurst)
	} else {
		logger.Warn("routing surface auth DISABLED (ROUTING_API_TOKEN unset)", "rate_limit_rps", cfg.RPS, "rate_limit_burst", effectiveBurst)
	}

	expected := []byte(cfg.Token)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var token string
			if authEnabled {
				token = bearerToken(r)
				// Constant-time compare so a wrong token cannot be distinguished by
				// response timing. ConstantTimeCompare is length-safe (returns 0 on a
				// length mismatch) and is reached even for an empty token.
				if subtle.ConstantTimeCompare([]byte(token), expected) != 1 {
					writeSecurityError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
			}

			if limiter != nil {
				// Per-client key, computed only when the limiter is live: the bearer
				// token when auth is on (policy is per credential — with a single
				// static token this is effectively one shared bucket), else the client
				// IP (best-effort; see clientIP).
				key := "token:" + token
				if !authEnabled {
					key = clientIP(r)
				}
				if ok, retry := limiter.allow(key); !ok {
					secs := int(math.Ceil(retry.Seconds()))
					if secs < 1 {
						secs = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(secs))
					writeSecurityError(w, http.StatusTooManyRequests, "rate limit exceeded")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// writeSecurityError emits the uniform JSON error envelope the rest of the
// surface uses. The middleware runs BEFORE the api handlers, so it has no metrics
// handle; a rejected request is not counted as a routing request (it never
// reached a routing endpoint). On the off chance the envelope fails to marshal
// (it cannot — errorResponse is a plain string), fall back to a hardcoded body.
func writeSecurityError(w http.ResponseWriter, status int, message string) {
	body, err := json.Marshal(errorResponse{Error: message})
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// bearerToken extracts the token from an `Authorization: Bearer <token>` header,
// returning "" when the header is absent or not a bearer credential. The scheme
// match is case-insensitive (RFC 7235); the token itself is taken verbatim.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// clientIP returns the host portion of r.RemoteAddr, falling back to the raw
// value when it has no port. This is the rate-limit key when auth is off.
//
// NOTE: this trusts the transport's peer address. A TLS-terminating reverse
// proxy is ASSUMED in front of the service; X-Forwarded-For is deliberately NOT
// consulted, because it is client-spoofable when not behind such a proxy. Behind
// a proxy that rewrites RemoteAddr (or with the proxy itself rate-limited), this
// keys correctly; see docs/api.md.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter is a per-client token-bucket limiter. It is hand-rolled (no
// golang.org/x/time/rate) to keep the module deps minimal. The bucket map is
// bounded by LAZY eviction: idle buckets are swept on access (no background
// goroutine, so nothing to stop and no leak). A bucket that has been idle for
// idleTTL has refilled to full, so dropping it is state-preserving — a later
// request just recreates a full bucket.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rps     float64
	burst   float64

	lastSweep time.Time
	// sweepInterval bounds how often the (O(n)) sweep runs so a hot path is not
	// taxed on every request.
	sweepInterval time.Duration
	// idleTTL is how long a bucket may go untouched before it is evicted. It is
	// kept comfortably above the full-refill time so an actively-throttled client's
	// bucket is never dropped mid-penalty.
	idleTTL time.Duration

	// now is the clock seam; production uses time.Now, tests inject a fake.
	now func() time.Time
}

// tokenBucket is one client's bucket: a fractional token count refilled lazily
// from the elapsed time since the last touch.
type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	lastAccess time.Time
}

func newRateLimiter(rps, burst float64) *rateLimiter {
	return &rateLimiter{
		buckets:       make(map[string]*tokenBucket),
		rps:           rps,
		burst:         burst,
		sweepInterval: time.Minute,
		idleTTL:       10 * time.Minute,
		now:           time.Now,
	}
}

// allow consumes one token for key, returning (true, 0) when permitted or
// (false, retryAfter) when the bucket is empty. retryAfter is the time until one
// token has refilled. It is safe for concurrent use.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, lastRefill: now}
		l.buckets[key] = b
	}

	// Refill by the elapsed wall time, capped at the bucket's capacity.
	if elapsed := now.Sub(b.lastRefill).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rps)
		b.lastRefill = now
	}
	b.lastAccess = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// Empty: time until the next whole token. rps > 0 is guaranteed by the
	// constructor (the middleware only builds a limiter when RPS > 0).
	needed := 1 - b.tokens
	retry := time.Duration(needed / l.rps * float64(time.Second))
	return false, retry
}

// sweepLocked evicts buckets idle past idleTTL, at most once per sweepInterval.
// The caller must hold l.mu. Eviction is state-preserving (an idle bucket has
// refilled to full), so this bounds memory without affecting throttling.
func (l *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.sweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.lastAccess) >= l.idleTTL {
			delete(l.buckets, k)
		}
	}
}
