// Command healthcheck is a tiny, dependency-free liveness probe for the
// routing-server container's distroless image (which has no shell, wget, or
// curl). It GETs /healthz on the configured address and exits 0 on HTTP 200,
// non-zero otherwise — exactly the contract a docker-compose HEALTHCHECK wants.
//
// Address resolution mirrors routing-server: ROUTING_SERVER_ADDR (default
// ":8080"). A bare ":port" is dialed against localhost inside the container.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := os.Getenv("ROUTING_SERVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	// ":8080" -> "localhost:8080"; "0.0.0.0:8080" -> "localhost:8080".
	host, port, found := strings.Cut(addr, ":")
	if !found {
		// No colon: treat the whole value as a port.
		port = addr
		host = ""
	}
	if host == "" || host == "0.0.0.0" || host == "[::]" {
		host = "localhost"
	}
	url := fmt.Sprintf("http://%s:%s/healthz", host, port)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: %s -> %d\n", url, resp.StatusCode)
		os.Exit(1)
	}
}
