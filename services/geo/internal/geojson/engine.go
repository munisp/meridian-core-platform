// Package geojson is the embedded fallback spatial engine for the geo
// service: point-in-polygon (ray casting) with haversine prefilter over an
// embedded simplified dataset of Nigerian state polygons ([seed] — coarse,
// not survey-grade) and a seed set of LGA centroids. The primary engine is
// geo-rs; this mirrors its semantics for dev-standalone operation.
package geojson

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

//go:embed data/states.geojson
var statesGeoJSON []byte

//go:embed data/lgas.geojson
var lgasGeoJSON []byte

// Feature is one GeoJSON polygon feature.
type Feature struct {
	Name     string       `json:"name"`
	Code     string       `json:"code"`
	Level    string       `json:"level"` // state|lga
	State    string       `json:"state,omitempty"`
	Ring     [][2]float64 `json:"ring"` // exterior ring, [lon,lat] pairs
	Centroid [2]float64   `json:"centroid"`
}

// Dataset is the loaded boundary set.
type Dataset struct {
	States []Feature `json:"states"`
	LGAs   []Feature `json:"lgas"`
	Seed   bool      `json:"seed"`
}

type rawFeature struct {
	Properties struct {
		Name  string `json:"name"`
		Code  string `json:"code"`
		State string `json:"state"`
	} `json:"properties"`
	Geometry struct {
		Type        string         `json:"type"`
		Coordinates [][][2]float64 `json:"coordinates"`
	} `json:"geometry"`
}

type rawCollection struct {
	Features []rawFeature `json:"features"`
}

func centroid(ring [][2]float64) [2]float64 {
	var sx, sy float64
	for _, p := range ring {
		sx += p[0]
		sy += p[1]
	}
	n := float64(len(ring))
	return [2]float64{sx / n, sy / n}
}

func parseCollection(b []byte, level string) ([]Feature, error) {
	var fc rawCollection
	if err := json.Unmarshal(b, &fc); err != nil {
		return nil, err
	}
	var out []Feature
	for _, f := range fc.Features {
		if f.Geometry.Type != "Polygon" || len(f.Geometry.Coordinates) == 0 {
			continue
		}
		ring := f.Geometry.Coordinates[0]
		out = append(out, Feature{
			Name:     f.Properties.Name,
			Code:     f.Properties.Code,
			Level:    level,
			State:    f.Properties.State,
			Ring:     ring,
			Centroid: centroid(ring),
		})
	}
	return out, nil
}

// LoadEmbedded loads the [seed] embedded dataset.
func LoadEmbedded() (*Dataset, error) {
	states, err := parseCollection(statesGeoJSON, "state")
	if err != nil {
		return nil, fmt.Errorf("states: %w", err)
	}
	lgas, err := parseCollection(lgasGeoJSON, "lga")
	if err != nil {
		return nil, fmt.Errorf("lgas: %w", err)
	}
	return &Dataset{States: states, LGAs: lgas, Seed: true}, nil
}

// HaversineKm is the great-circle distance between two lon/lat points.
func HaversineKm(lon1, lat1, lon2, lat2 float64) float64 {
	const r = 6371.0088
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// PointInPolygon ray-casts lon/lat against a ring (lon=x, lat=y).
func PointInPolygon(lon, lat float64, ring [][2]float64) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > lat) != (yj > lat) {
			xInt := (xj-xi)*(lat-yi)/(yj-yi) + xi
			if lon < xInt {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

func bboxContains(lon, lat float64, ring [][2]float64, padKm float64) bool {
	minLon, maxLon := ring[0][0], ring[0][0]
	minLat, maxLat := ring[0][1], ring[0][1]
	for _, p := range ring[1:] {
		minLon = math.Min(minLon, p[0])
		maxLon = math.Max(maxLon, p[0])
		minLat = math.Min(minLat, p[1])
		maxLat = math.Max(maxLat, p[1])
	}
	pad := padKm / 111.0
	return lon >= minLon-pad && lon <= maxLon+pad && lat >= minLat-pad && lat <= maxLat+pad
}

// Attribution is the result of point attribution.
type Attribution struct {
	State     string `json:"state"`
	StateCode string `json:"state_code"`
	LGA       string `json:"lga,omitempty"`
	Ward      string `json:"ward,omitempty"`
	Source    string `json:"source"` // embedded-seed | geo-rs
}

// AttributePoint finds the state (and seed LGA) containing lon/lat.
// Strategy: haversine prefilter by bbox, then exact point-in-polygon;
// LGA = nearest seed centroid in the state within 60km.
func (d *Dataset) AttributePoint(lat, lon float64) *Attribution {
	for _, st := range d.States {
		if !bboxContains(lon, lat, st.Ring, 0) {
			continue // haversine prefilter (bbox form)
		}
		if !PointInPolygon(lon, lat, st.Ring) {
			continue
		}
		att := &Attribution{State: st.Name, StateCode: st.Code, Source: "embedded-seed [seed]"}
		best := -1.0
		bestName := ""
		for _, lga := range d.LGAs {
			if lga.State != st.Name {
				continue
			}
			dist := HaversineKm(lon, lat, lga.Centroid[0], lga.Centroid[1])
			if best < 0 || dist < best {
				best = dist
				bestName = lga.Name
			}
		}
		if bestName != "" && best <= 60 {
			att.LGA = bestName
		}
		return att
	}
	return nil
}

// NearestStates returns states ordered by centroid distance (diagnostics).
func (d *Dataset) NearestStates(lat, lon float64, n int) []Feature {
	type scored struct {
		f Feature
		d float64
	}
	var ss []scored
	for _, st := range d.States {
		ss = append(ss, scored{st, HaversineKm(lon, lat, st.Centroid[0], st.Centroid[1])})
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].d < ss[j].d })
	out := make([]Feature, 0, n)
	for i := 0; i < len(ss) && i < n; i++ {
		out = append(out, ss[i].f)
	}
	return out
}

// Boundaries returns the features at a level (state|lga).
func (d *Dataset) Boundaries(level string) []Feature {
	switch level {
	case "state":
		return d.States
	case "lga":
		return d.LGAs
	default:
		return nil
	}
}
