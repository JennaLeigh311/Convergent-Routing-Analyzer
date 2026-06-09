// Package cost holds the CostFunction port and its volume-delay
// implementations (BPR, linear). BPR: t(v) = t_free * (1 + alpha*(v/c)^beta).
// The CostFunction port is defined in cost.go; implementations arrive in a
// later phase.
package cost
