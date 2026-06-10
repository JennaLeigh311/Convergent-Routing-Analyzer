package serveraddr

import "testing"

func TestHealthURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"bare wildcard port", ":8080", "http://localhost:8080/healthz"},
		{"ipv4 wildcard", "0.0.0.0:8080", "http://localhost:8080/healthz"},
		{"ipv6 wildcard", "[::]:8080", "http://localhost:8080/healthz"},
		{"bare port no colon", "8080", "http://localhost:8080/healthz"},
		{"explicit localhost", "localhost:8080", "http://localhost:8080/healthz"},
		{"concrete ipv4 host", "127.0.0.1:9090", "http://127.0.0.1:9090/healthz"},
		{"concrete ipv6 host re-bracketed", "[::1]:8080", "http://[::1]:8080/healthz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HealthURL(tt.addr); got != tt.want {
				t.Errorf("HealthURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Run("falls back to Default when unset", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		if got := Resolve(); got != Default {
			t.Errorf("Resolve() = %q, want Default %q", got, Default)
		}
	})
	t.Run("uses env override when set", func(t *testing.T) {
		t.Setenv(EnvVar, "0.0.0.0:9999")
		if got := Resolve(); got != "0.0.0.0:9999" {
			t.Errorf("Resolve() = %q, want %q", got, "0.0.0.0:9999")
		}
	})
}
