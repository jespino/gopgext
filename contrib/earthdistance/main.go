// earthdistance is a Go port of PostgreSQL's contrib/earthdistance
// (version 1.2): great-circle distances on the surface of the Earth, on
// points of the cube extension (the earth domain, in SQL: sql/) or on the
// built-in point type (geo_distance and the <@> operator, here).
//
// Ported from PostgreSQL 18's contrib/earthdistance (earthdistance.c).
package main

import (
	"math"

	"gopgext/pg"
)

func main() {}

// earthRadius is the Earth's radius in statute miles.
const earthRadius = 3958.747716

const twoPi = 2.0 * math.Pi

func degToRad(degrees float64) float64 { return (degrees / 360.0) * twoPi }

// GeoDistance is geo_distance(point, point) (the <@> operator): the
// distance in statute miles between two points given as (longitude in
// degrees west of Greenwich, latitude in degrees above the equator).
//
//gopgext:function symbol=geo_distance manual
func GeoDistance(pt1, pt2 pg.Point) float64 {
	long1, lat1 := degToRad(pt1.X), degToRad(pt1.Y)
	long2, lat2 := degToRad(pt2.X), degToRad(pt2.Y)

	// The difference in longitudes: under 180 degrees.
	longdiff := math.Abs(long1 - long2)
	if longdiff > math.Pi {
		longdiff = twoPi - longdiff
	}
	sino := math.Sqrt(math.Sin(math.Abs(lat1-lat2)/2.)*math.Sin(math.Abs(lat1-lat2)/2.) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(longdiff/2.)*math.Sin(longdiff/2.))
	if sino > 1. {
		sino = 1.
	}
	return 2. * earthRadius * math.Asin(sino)
}
