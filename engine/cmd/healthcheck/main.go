// Command healthcheck is a tiny, dependency-free liveness probe for the
// routing-server container's distroless image (which has no shell, wget, or
// curl). It GETs /healthz on the configured address and exits 0 on HTTP 200,
// non-zero otherwise — exactly the contract a docker-compose HEALTHCHECK wants.
//
// Address resolution mirrors routing-server via the shared internal/serveraddr
// leaf: ROUTING_SERVER_ADDR (default ":8080"), with wildcard/empty hosts dialed
// against localhost inside the container.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/serveraddr"
)

func main() {
	url := serveraddr.HealthURL(serveraddr.Resolve())

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
