package serveraddr

import "testing"

func TestHealthURL(test1 *testing.T) {
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
	for _, testCase := range tests {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := HealthURL(testCase.addr); got != testCase.want {
				test2.Errorf("HealthURL(%q) = %q, want %q", testCase.addr, got, testCase.want)
			}
		})
	}
}

func TestResolve(test1 *testing.T) {
	test1.Run("falls back to Default when unset", func(test2 *testing.T) {
		test2.Setenv(EnvVar, "")
		if got := Resolve(); got != Default {
			test2.Errorf("Resolve() = %q, want Default %q", got, Default)
		}
	})
	test1.Run("uses env override when set", func(test3 *testing.T) {
		test3.Setenv(EnvVar, "0.0.0.0:9999")
		if got := Resolve(); got != "0.0.0.0:9999" {
			test3.Errorf("Resolve() = %q, want %q", got, "0.0.0.0:9999")
		}
	})
}
