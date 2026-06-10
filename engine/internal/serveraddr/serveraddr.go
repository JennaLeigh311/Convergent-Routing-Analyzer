// Package serveraddr centralizes the routing-server's listen-address contract:
// the env-var name, the default, and how a bind address is turned into a
// localhost-dialable URL for the in-container healthcheck. Both
// cmd/routing-server (which binds) and cmd/healthcheck (which probes) depend on
// this single leaf so the two can never drift on the var name, default port, or
// dial convention.
package serveraddr

import (
	"net"
	"os"
)

// EnvVar is the environment variable that overrides the listen address.
const EnvVar = "ROUTING_SERVER_ADDR"

// Default is the listen address used when EnvVar is unset. It matches the
// .env.example default so the dev (core) profile needs no configuration.
const Default = ":8080"

// Resolve returns the configured listen address, falling back to Default when
// EnvVar is unset or empty.
func Resolve() string {
	if addr := os.Getenv(EnvVar); addr != "" {
		return addr
	}
	return Default
}

// HealthURL turns a listen address into the /healthz URL the in-container
// healthcheck should dial. A wildcard or empty host (":8080", "0.0.0.0:8080",
// "[::]:8080") is rewritten to localhost; a concrete host (incl. IPv6 literals
// like "[::1]:8080") is preserved. A bare port ("8080") is treated as ":8080".
//
// net.SplitHostPort/JoinHostPort handle IPv6 bracketing correctly, unlike a
// naive split on the first colon.
func HealthURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port separator: treat the whole value as a bare port.
		host, port = "", addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}
