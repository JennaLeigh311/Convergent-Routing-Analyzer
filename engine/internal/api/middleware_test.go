package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// okHandler is a trivial downstream the middleware fronts: it records that it was
// reached and returns 200. The auth/rate-limit tests assert whether a request
// reaches it (passthrough) or is short-circuited (401/429) before it.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// decodeErrorEnvelope asserts the body is the uniform {"error":"..."} envelope
// and returns the message.
func decodeErrorEnvelope(t *testing.T, body []byte) string {
	t.Helper()
	var env errorResponse
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("error body is not the uniform envelope: %v (body: %s)", err, body)
	}
	return env.Error
}

// TestSecurityMiddlewareAuthDisabled asserts the open default: with no token
// configured every request passes through unauthenticated.
func TestSecurityMiddlewareAuthDisabled(t *testing.T) {
	var reached bool
	mw := NewSecurityMiddleware(SecurityConfig{}, nil) // no token, no rate limit
	h := mw(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))

	if !reached {
		t.Fatalf("auth-disabled request did not reach the handler")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestSecurityMiddlewareAuth covers the token-required paths: a missing token, a
// wrong token, and a malformed scheme are all 401 with the uniform envelope and
// never reach the handler; the correct token passes through.
func TestSecurityMiddlewareAuth(t *testing.T) {
	const token = "s3cr3t-token"
	cfg := SecurityConfig{Token: token} // auth on, rate limiting off (RPS 0)

	cases := []struct {
		name       string
		header     string
		setHeader  bool
		wantStatus int
		wantReach  bool
	}{
		{name: "no header", setHeader: false, wantStatus: http.StatusUnauthorized},
		{name: "empty bearer", header: "Bearer ", setHeader: true, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer nope", setHeader: true, wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic " + token, setHeader: true, wantStatus: http.StatusUnauthorized},
		{name: "correct token", header: "Bearer " + token, setHeader: true, wantStatus: http.StatusOK, wantReach: true},
		{name: "scheme case-insensitive", header: "bearer " + token, setHeader: true, wantStatus: http.StatusOK, wantReach: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			h := NewSecurityMiddleware(cfg, nil)(okHandler(&reached))
			req := httptest.NewRequest(http.MethodGet, "/route", nil)
			if tc.setHeader {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if reached != tc.wantReach {
				t.Errorf("handler reached = %v, want %v", reached, tc.wantReach)
			}
			if tc.wantStatus == http.StatusUnauthorized {
				if msg := decodeErrorEnvelope(t, rec.Body.Bytes()); msg == "" {
					t.Errorf("401 body has empty error message")
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
					t.Errorf("Content-Type = %q, want JSON", ct)
				}
			}
		})
	}
}

// TestSecurityMiddlewareRateLimit asserts the burst is honored and the next
// request over it is a 429 with a Retry-After header and the uniform envelope.
// A tiny burst with a modest RPS makes the limit deterministic within the test's
// wall-clock (the refill over microseconds is negligible).
func TestSecurityMiddlewareRateLimit(t *testing.T) {
	cfg := SecurityConfig{RPS: 5, Burst: 3} // auth off, 3-token burst
	var reached bool
	h := NewSecurityMiddleware(cfg, nil)(okHandler(&reached))

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/route", nil)
		req.RemoteAddr = "203.0.113.7:54321"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// The first `burst` requests are allowed.
	for i := 0; i < cfg.Burst; i++ {
		if rec := send(); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}

	// The next one exhausts the bucket: 429 + Retry-After + uniform envelope.
	rec := send()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-burst status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Errorf("429 missing Retry-After header")
	} else if secs, err := strconv.Atoi(ra); err != nil || secs < 1 {
		t.Errorf("Retry-After = %q, want a positive integer (seconds until refill)", ra)
	}
	if msg := decodeErrorEnvelope(t, rec.Body.Bytes()); msg == "" {
		t.Errorf("429 body has empty error message")
	}
}

// TestSecurityMiddlewareRateLimitPerClient asserts buckets are keyed per client:
// exhausting one IP's bucket does not throttle a different IP.
func TestSecurityMiddlewareRateLimitPerClient(t *testing.T) {
	cfg := SecurityConfig{RPS: 1, Burst: 1}
	var reached bool
	h := NewSecurityMiddleware(cfg, nil)(okHandler(&reached))

	send := func(addr string) int {
		req := httptest.NewRequest(http.MethodGet, "/route", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("198.51.100.1:1111"); code != http.StatusOK {
		t.Fatalf("client A first request: %d, want 200", code)
	}
	if code := send("198.51.100.1:1111"); code != http.StatusTooManyRequests {
		t.Fatalf("client A second request: %d, want 429", code)
	}
	// A different client still has a full bucket.
	if code := send("198.51.100.2:2222"); code != http.StatusOK {
		t.Fatalf("client B first request: %d, want 200 (B should not share A's bucket)", code)
	}
}

// TestSecurityMiddlewareRateLimitTokenKeyed asserts the auth-ON posture: the
// limiter keys on the bearer token, NOT the client IP. The same token is throttled
// regardless of source IP — this is the production path (auth on ⇒ token-keyed),
// which the IP-keyed tests above (auth off) never exercise.
func TestSecurityMiddlewareRateLimitTokenKeyed(t *testing.T) {
	const token = "tok"
	cfg := SecurityConfig{Token: token, RPS: 1, Burst: 1} // auth on + tiny bucket
	var reached bool
	h := NewSecurityMiddleware(cfg, nil)(okHandler(&reached))

	send := func(addr string) int {
		req := httptest.NewRequest(http.MethodGet, "/route", nil)
		req.RemoteAddr = addr
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("203.0.113.1:1111"); code != http.StatusOK {
		t.Fatalf("first request: %d, want 200", code)
	}
	// Same token, DIFFERENT IP: still throttled, proving the key is the token (had
	// it keyed on IP, this fresh IP would get its own full bucket and pass).
	if code := send("203.0.113.2:2222"); code != http.StatusTooManyRequests {
		t.Fatalf("second request (same token, different IP): %d, want 429 (token-keyed)", code)
	}
}

// TestSecurityMiddlewareBurstClamp asserts a zero/negative burst is clamped to one
// token rather than wedging the bucket shut (which would 429 the very first
// request). Without the clamp this regresses to a fully-closed limiter.
func TestSecurityMiddlewareBurstClamp(t *testing.T) {
	cfg := SecurityConfig{RPS: 5, Burst: 0} // burst 0 must clamp to 1
	var reached bool
	h := NewSecurityMiddleware(cfg, nil)(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/route", nil)
	req.RemoteAddr = "198.51.100.9:9999"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first request with burst 0 (clamped to 1): %d, want 200", rec.Code)
	}
	if !reached {
		t.Errorf("handler not reached — a 0 burst wedged the bucket shut instead of clamping to 1")
	}
}

// TestLoadSecurityConfig covers the env→config read: defaults when unset, and
// each override (including the unparseable-falls-back-to-default behavior).
func TestLoadSecurityConfig(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		t.Setenv(EnvAPIToken, "")
		t.Setenv(EnvRateLimitRPS, "")
		t.Setenv(EnvRateLimitBurst, "")
		cfg := LoadSecurityConfig()
		if cfg.Token != "" {
			t.Errorf("Token = %q, want empty", cfg.Token)
		}
		if cfg.RPS != defaultRateLimitRPS {
			t.Errorf("RPS = %v, want %v", cfg.RPS, defaultRateLimitRPS)
		}
		if cfg.Burst != defaultRateLimitBurst {
			t.Errorf("Burst = %v, want %v", cfg.Burst, defaultRateLimitBurst)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		t.Setenv(EnvAPIToken, "abc")
		t.Setenv(EnvRateLimitRPS, "100")
		t.Setenv(EnvRateLimitBurst, "250")
		cfg := LoadSecurityConfig()
		if cfg.Token != "abc" || cfg.RPS != 100 || cfg.Burst != 250 {
			t.Errorf("config = %+v, want {abc 100 250}", cfg)
		}
	})

	t.Run("unparseable falls back to default", func(t *testing.T) {
		t.Setenv(EnvRateLimitRPS, "not-a-number")
		t.Setenv(EnvRateLimitBurst, "also-bad")
		cfg := LoadSecurityConfig()
		if cfg.RPS != defaultRateLimitRPS || cfg.Burst != defaultRateLimitBurst {
			t.Errorf("config = %+v, want defaults on bad input", cfg)
		}
	})
}

// TestRateLimiterRefill asserts the bucket refills over (injected) time: an
// exhausted bucket admits again once enough time has elapsed for a token.
func TestRateLimiterRefill(t *testing.T) {
	now := time.Unix(0, 0)
	rl := newRateLimiter(2, 1) // 2 tokens/sec, capacity 1
	rl.now = func() time.Time { return now }

	if ok, _ := rl.allow("k"); !ok {
		t.Fatalf("first request should be allowed")
	}
	if ok, retry := rl.allow("k"); ok {
		t.Fatalf("second immediate request should be denied")
	} else if retry <= 0 {
		t.Errorf("denied request should report a positive Retry-After, got %v", retry)
	}

	// Advance half a second: at 2 tokens/sec that is one token.
	now = now.Add(500 * time.Millisecond)
	if ok, _ := rl.allow("k"); !ok {
		t.Errorf("request after refill should be allowed")
	}
}

// TestRateLimiterEviction asserts the bucket map is bounded: an idle bucket is
// swept once it passes idleTTL and the sweep interval has elapsed, so the map
// does not grow without bound as clients come and go.
func TestRateLimiterEviction(t *testing.T) {
	now := time.Unix(0, 0)
	rl := newRateLimiter(10, 10)
	rl.now = func() time.Time { return now }
	rl.sweepInterval = time.Minute
	rl.idleTTL = 5 * time.Minute

	rl.allow("idle")
	if len(rl.buckets) != 1 {
		t.Fatalf("expected 1 bucket after first request, got %d", len(rl.buckets))
	}

	// Advance well past both the idle TTL and the sweep interval, then touch a
	// DIFFERENT key so the sweep runs. The idle bucket must be evicted.
	now = now.Add(10 * time.Minute)
	rl.allow("fresh")
	if _, ok := rl.buckets["idle"]; ok {
		t.Errorf("idle bucket was not evicted; map = %v", rl.buckets)
	}
	if _, ok := rl.buckets["fresh"]; !ok {
		t.Errorf("fresh bucket missing after sweep")
	}
}
