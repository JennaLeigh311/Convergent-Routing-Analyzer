// Package graph holds the road-network domain: the Graph port (read-only,
// adapter-loaded from the pgRouting/GeoJSON export), the adjacency-list
// implementation, and the geo/spatial primitives (NearestNode for OD
// endpoints, NearestEdge for map-matching). The Graph port is defined in
// graph.go; concrete loaders/adapters arrive in a later phase.
package graph
