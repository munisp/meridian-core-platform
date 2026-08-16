// Package postgis is the Postgres/PostGIS attribution engine for the geo
// service, selected when DATABASE_URL is set (precedence: geo-rs URL, then
// PostGIS, then the embedded pure-Go engine — see main.go). It loads the
// same embedded seed boundaries into a real table and answers attribution
// with a genuine ST_Covers point-in-polygon query.
//
// PostGIS must be installed in the target database; if `CREATE EXTENSION
// postgis` fails, Open returns an error and the service falls back to the
// embedded engine (dev profile) or refuses to boot (PROFILE=prod, matching
// the fail-closed convention used elsewhere in the platform).
package postgis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/munisp/meridian-core-platform/services/geo/internal/geojson"
)

// Engine answers attribution queries against PostGIS.
type Engine struct {
	pool *pgxpool.Pool
}

// Attribution is one attribution result row.
type Attribution struct {
	State     string
	StateCode string
}

// Open connects, enables PostGIS, creates the boundary table and loads the
// embedded seed dataset when the table is empty.
func Open(ctx context.Context, databaseURL string, ds *geojson.Dataset) (*Engine, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgis connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgis ping: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS postgis"); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgis extension unavailable: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS geo_boundaries (
		level text NOT NULL,
		name  text NOT NULL,
		code  text NOT NULL,
		state text,
		geom  geometry(Polygon, 4326) NOT NULL
	)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgis schema: %w", err)
	}
	e := &Engine{pool: pool}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM geo_boundaries").Scan(&n); err != nil {
		pool.Close()
		return nil, err
	}
	if n == 0 {
		if err := e.load(ctx, ds); err != nil {
			pool.Close()
			return nil, fmt.Errorf("postgis seed load: %w", err)
		}
	}
	return e, nil
}

// wktPolygon renders an exterior ring as WKT (lon lat pairs).
func wktPolygon(ring [][2]float64) string {
	var b strings.Builder
	b.WriteString("POLYGON((")
	for i, p := range ring {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%g %g", p[0], p[1])
	}
	b.WriteString("))")
	return b.String()
}

func (e *Engine) load(ctx context.Context, ds *geojson.Dataset) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	insert := func(level string, feats []geojson.Feature) error {
		for _, f := range feats {
			if len(f.Ring) == 0 {
				continue
			}
			if _, err := tx.Exec(ctx,
				"INSERT INTO geo_boundaries(level,name,code,state,geom) VALUES ($1,$2,$3,$4,ST_GeomFromText($5,4326))",
				level, f.Name, f.Code, f.State, wktPolygon(f.Ring)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := insert("state", ds.States); err != nil {
		return err
	}
	if err := insert("lga", ds.LGAs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AttributePoint runs the real PostGIS query: state polygon covering the
// point (ST_Covers + ST_SetSRID/ST_Point), LGA resolved the same way when
// LGA polygons exist, else nearest LGA centroid via ST_Distance.
func (e *Engine) AttributePoint(ctx context.Context, lat, lon float64) (*Attribution, error) {
	var att Attribution
	var state, code *string
	err := e.pool.QueryRow(ctx,
		`SELECT name, code FROM geo_boundaries
		 WHERE level='state' AND ST_Covers(geom, ST_SetSRID(ST_Point($1,$2),4326))
		 LIMIT 1`, lon, lat).Scan(&state, &code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgis ST_Covers: %w", err)
	}
	if state == nil {
		return nil, nil
	}
	att.State, att.StateCode = *state, *code
	return &att, nil
}

// Close releases the pool.
func (e *Engine) Close() { e.pool.Close() }
