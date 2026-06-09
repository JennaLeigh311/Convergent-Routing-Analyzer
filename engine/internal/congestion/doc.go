// Package congestion holds the CongestionProvider port and its adapters
// (in-memory store, static/simulator sources, and the compacted-Kafka-topic
// consumer). The engine never learns the source of congestion through this
// boundary — see project-spec.md §2. The CongestionProvider port is defined in
// congestion.go; concrete adapters arrive in later phases as subpackages.
package congestion
