// Package congestion holds the CongestionProvider port and its adapters
// (in-memory store, static/simulator sources, and the compacted-Kafka-topic
// consumer). The engine never learns the source of congestion through this
// boundary — see project-spec.md §2.
//
// Interfaces are defined in issue #2; this package is currently a stub.
package congestion
