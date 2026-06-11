package graph

import (
	"math"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// earthRadiusM is the mean (volumetric) Earth radius in meters, the standard
// constant for great-circle distance on a spherical Earth. The road network
// spans a city, so the sphere approximation's sub-percent error versus a full
// WGS84 ellipsoid is irrelevant to nearest-node resolution.
const earthRadiusM = 6_371_000.0

// degToRad converts decimal degrees to radians.
func degToRad(deg float64) float64 { return deg * math.Pi / 180.0 }

// haversine returns the great-circle distance between two WGS84 coordinates in
// meters, using the haversine formula on a sphere of radius earthRadiusM.
//
// The haversine form is used (rather than the simpler spherical law of cosines)
// because it stays numerically well-conditioned for the small angular
// separations typical of intersections in one city, where law-of-cosines loses
// precision to catastrophic cancellation. Distance is symmetric and returns 0
// for identical points.
func haversine(a, b domain.LatLon) float64 {
	lat1 := degToRad(a.Lat)
	lat2 := degToRad(b.Lat)
	dLat := degToRad(b.Lat - a.Lat)
	dLon := degToRad(b.Lon - a.Lon)

	sinDLat := math.Sin(dLat / 2)
	sinDLon := math.Sin(dLon / 2)
	h := sinDLat*sinDLat + math.Cos(lat1)*math.Cos(lat2)*sinDLon*sinDLon
	// 2*asin(sqrt(h)) == 2*atan2(sqrt(h), sqrt(1-h)); atan2 is robust at h→1.
	c := 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
	return earthRadiusM * c
}
