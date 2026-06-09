// Command replay reads GPS-ping CSVs (Porto/T-Drive) and replays them to the
// gps-pings Kafka topic at a controllable speed. Scaffolding stub — wired up
// in a later phase.
package main

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/logging"

func main() {
	logger := logging.Setup()
	logger.Info("scaffold stub — not yet implemented", "component", "replay")
}
