// Command routing-server is the REST + WebSocket API in front of the routing
// engine. Scaffolding stub — wired up in a later phase.
package main

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"

func main() {
	logger := logging.Setup()
	logger.Info("starting", "component", "routing-server", "status", "scaffold stub — not yet implemented")
}
