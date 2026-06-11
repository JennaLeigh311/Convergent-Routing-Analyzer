// Package graph holds the road-network domain: the Graph port (read-only,
// adapter-loaded from the pgRouting/GeoJSON export), the adjacency-list
// implementation, and the geo/spatial primitives. NearestNode (OD endpoints) is
// implemented, backed by a k-d tree over node positions built once at
// construction. NearestEdge (map-matching) is the only spatial query still
// pending; it arrives in Phase 7. The Graph port is defined in graph.go.
package graph
