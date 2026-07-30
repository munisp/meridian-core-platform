package geojson

import (
	"math"
	"testing"
)

func TestLoadEmbedded(t *testing.T) {
	ds, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.States) != 37 { // 36 states + FCT
		t.Fatalf("states = %d, want 37", len(ds.States))
	}
	if len(ds.LGAs) == 0 {
		t.Fatal("no lgas")
	}
	if !ds.Seed {
		t.Fatal("dataset must be marked seed")
	}
}

func TestPointInPolygon(t *testing.T) {
	sq := [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}
	if !PointInPolygon(5, 5, sq) {
		t.Fatal("center should be inside")
	}
	if PointInPolygon(15, 5, sq) {
		t.Fatal("outside point reported inside")
	}
	if PointInPolygon(-1, 5, sq) {
		t.Fatal("west point reported inside")
	}
}

func TestHaversine(t *testing.T) {
	// Lagos (3.3792, 6.5244) to Abuja (7.4951, 9.0579) ~ 540km
	d := HaversineKm(3.3792, 6.5244, 7.4951, 9.0579)
	if math.Abs(d-540) > 60 {
		t.Fatalf("lagos-abuja = %.0fkm", d)
	}
	if HaversineKm(3, 6, 3, 6) != 0 {
		t.Fatal("zero distance")
	}
}

func TestAttribution(t *testing.T) {
	ds, _ := LoadEmbedded()
	cases := []struct {
		name     string
		lat, lon float64
		state    string
		lga      string
	}{
		{"lagos island", 6.45, 3.40, "Lagos", "Lagos Island"},
		{"abuja amac", 9.06, 7.49, "FCT", "AMAC"},
		{"kano", 12.00, 8.52, "Kano", "Kano Municipal"},
		{"port harcourt", 4.82, 7.00, "Rivers", "Port Harcourt"},
		{"ibadan", 7.40, 3.90, "Oyo", "Ibadan North"},
		{"maiduguri", 11.83, 13.15, "Borno", "Maiduguri"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			att := ds.AttributePoint(c.lat, c.lon)
			if att == nil {
				t.Fatalf("no attribution for %s", c.name)
			}
			if att.State != c.state {
				t.Fatalf("state = %s, want %s", att.State, c.state)
			}
			if att.LGA != c.lga {
				t.Fatalf("lga = %s, want %s", att.LGA, c.lga)
			}
		})
	}
	// outside Nigeria (London)
	if att := ds.AttributePoint(51.5, -0.12); att != nil {
		t.Fatalf("london attributed to %s", att.State)
	}
}

func TestBoundaries(t *testing.T) {
	ds, _ := LoadEmbedded()
	if len(ds.Boundaries("state")) != 37 {
		t.Fatal("state boundaries")
	}
	if len(ds.Boundaries("lga")) == 0 {
		t.Fatal("lga boundaries")
	}
	if ds.Boundaries("ward") != nil {
		t.Fatal("ward should be unsupported in seed dataset")
	}
}
