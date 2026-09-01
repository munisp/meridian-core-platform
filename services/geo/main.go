// geo — geo service (SPEC 2). Point/batch attribution via geo-rs when
// GEO_RS_URL is set, with the embedded [seed] polygon engine as fallback.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/services/geo/internal/geojson"
	"github.com/munisp/meridian-core-platform/services/geo/internal/postgis"
)

const (
	service = "geo"
	version = "0.1.0"
)

type server struct {
	ds    *geojson.Dataset
	geoRS string          // GEO_RS_URL, "" = not configured
	pg    *postgis.Engine // non-nil when DATABASE_URL set (PostGIS engine)
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
	// OTel bootstrap (DESIGN-CONTRACT): fail-soft — no OTLP endpoint means
	// no-op providers; PROFILE=prod without one logs a loud warning.
	otelProv := otelx.InitProvidersFor(context.Background(), service, version)
	defer otelProv.Shutdown(context.Background())

	ds, err := geojson.LoadEmbedded()
	if err != nil {
		log.Fatalf("load embedded boundaries: %v", err)
	}
	s := &server{
		ds:    ds,
		geoRS: httpx.Env("GEO_RS_URL", ""),
		hc:    &http.Client{Timeout: 3 * time.Second},
	}

	// DATABASE_URL selects the PostGIS engine (real ST_* queries); the
	// embedded pure-Go engine remains the fallback when it is unset. A
	// configured-but-broken DATABASE_URL fails closed in prod, and falls
	// back with a loud log line in dev.
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		eng, err := postgis.Open(ctx, dbURL, ds)
		cancel()
		if err != nil {
			if os.Getenv("PROFILE") == "prod" {
				log.Fatalf("component=geo FATAL: DATABASE_URL set but PostGIS unavailable (%v); failing closed", err)
			}
			log.Printf("component=geo postgis unavailable (%v); embedded engine fallback", err)
		} else {
			s.pg = eng
			defer eng.Close()
			log.Printf("component=geo engine=postgis")
		}
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

// viaPostGIS answers attribution with the real ST_Covers query when the
// PostGIS engine is configured; a query error or miss falls back to the
// embedded engine.
func (s *server) viaPostGIS(ctx context.Context, lat, lon float64) *geojson.Attribution {
	if s.pg == nil {
		return nil
	}
	att, err := s.pg.AttributePoint(ctx, lat, lon)
	if err != nil {
		log.Printf("postgis query failed, embedded fallback: %v", err)
		return nil
	}
	if att == nil {
		return nil // outside all state polygons; embedded agrees or also misses
	}
	return &geojson.Attribution{State: att.State, StateCode: att.StateCode, Source: "postgis"}
}

func (s *server) attribute(ctx context.Context, lat, lon float64) *geojson.Attribution {
	if att := s.viaGeoRS(ctx, lat, lon); att != nil {
		return att
	}
	if att := s.viaPostGIS(ctx, lat, lon); att != nil {
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
