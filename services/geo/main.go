// geo — geo service (SPEC 2). Point/batch attribution via geo-rs when
// GEO_RS_URL is set, with the embedded [seed] polygon engine as fallback.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/services/geo/internal/geojson"
)

const (
	service = "geo"
	version = "0.1.0"
)

type server struct {
	ds    *geojson.Dataset
	geoRS string // GEO_RS_URL, "" = embedded only
	hc    *http.Client
}

type pointReq struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type batchReq struct {
	Points []pointReq `json:"points"`
}

func main() {
	ds, err := geojson.LoadEmbedded()
	if err != nil {
		log.Fatalf("load embedded boundaries: %v", err)
	}
	s := &server{
		ds:    ds,
		geoRS: httpx.Env("GEO_RS_URL", ""),
		hc:    &http.Client{Timeout: 3 * time.Second},
	}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/attribution/point", s.point)
	mux.HandleFunc("POST /v1/attribution/batch", s.batch)
	mux.HandleFunc("GET /v1/boundaries/{level}", s.boundaries)

	addr := ":" + httpx.Port("8005")
	log.Printf("%s %s (states=%d lgas=%d geo_rs=%q)", service, version,
		len(ds.States), len(ds.LGAs), s.geoRS)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

// viaGeoRS delegates to the Rust engine when configured.
func (s *server) viaGeoRS(ctx context.Context, lat, lon float64) *geojson.Attribution {
	if s.geoRS == "" {
		return nil
	}
	body, _ := json.Marshal(pointReq{Lat: lat, Lon: lon})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.geoRS+"/v1/attribution/point", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.hc.Do(req)
	if err != nil {
		log.Printf("geo-rs unavailable, embedded fallback: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var att geojson.Attribution
	if err := json.NewDecoder(resp.Body).Decode(&att); err != nil || att.State == "" {
		return nil
	}
	att.Source = "geo-rs"
	return &att
}

func (s *server) attribute(ctx context.Context, lat, lon float64) *geojson.Attribution {
	if att := s.viaGeoRS(ctx, lat, lon); att != nil {
		return att
	}
	return s.ds.AttributePoint(lat, lon)
}

func (s *server) point(w http.ResponseWriter, r *http.Request) {
	var req pointReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.Lat < -90 || req.Lat > 90 || req.Lon < -180 || req.Lon > 180 {
		httpx.BadRequest(w, "lat/lon out of range")
		return
	}
	att := s.attribute(r.Context(), req.Lat, req.Lon)
	if att == nil {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"found": false, "state": nil, "lga": nil, "ward": nil,
			"note": "point outside all [seed] state polygons"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"found": true, "state": att.State, "state_code": att.StateCode,
		"lga": att.LGA, "ward": att.Ward, "source": att.Source,
	})
}

func (s *server) batch(w http.ResponseWriter, r *http.Request) {
	var req batchReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if len(req.Points) == 0 || len(req.Points) > 10_000 {
		httpx.BadRequest(w, "points must contain 1..10000 entries")
		return
	}
	type result struct {
		Lat   float64 `json:"lat"`
		Lon   float64 `json:"lon"`
		Found bool    `json:"found"`
		State string  `json:"state,omitempty"`
		LGA   string  `json:"lga,omitempty"`
	}
	out := make([]result, 0, len(req.Points))
	for _, p := range req.Points {
		res := result{Lat: p.Lat, Lon: p.Lon}
		if att := s.attribute(r.Context(), p.Lat, p.Lon); att != nil {
			res.Found = true
			res.State = att.State
			res.LGA = att.LGA
		}
		out = append(out, res)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"results": out, "count": len(out)})
}

func (s *server) boundaries(w http.ResponseWriter, r *http.Request) {
	level := r.PathValue("level")
	feats := s.ds.Boundaries(level)
	if feats == nil {
		httpx.BadRequest(w, "level must be state or lga")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"level": level, "seed": s.ds.Seed, "count": len(feats), "features": feats,
	})
}
