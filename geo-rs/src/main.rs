//! geo-rs — Meridian spatial engine (axum HTTP).
//! Endpoints: POST /v1/attribution/point, POST /v1/attribution/batch,
//! GET /v1/boundaries/{level}, GET /healthz.
//! Strategy: haversine prefilter by centroid distance, then exact
//! ray-casting point-in-polygon over embedded simplified NG state polygons.
//! NOTE: embedded boundaries are [seed] coarse polygons, not survey-grade.
//! Toolchain unavailable in build env — verified by review, not cargo build.

use axum::{extract::Path, http::StatusCode, response::Json, routing::{get, post}, Router};
use serde::{Deserialize, Serialize};
use std::f64::consts::PI;

// Embedded simplified GeoJSON (generated; mirrors services/geo seed data).
const STATES_GEOJSON: &str = include_str!("../data/states.geojson");
const LGAS_GEOJSON: &str = include_str!("../data/lgas.geojson");

#[derive(Clone, Debug)]
struct Feature {
    name: String,
    code: String,
    state: String,
    ring: Vec<(f64, f64)>, // (lon, lat)
    centroid: (f64, f64),
}

#[derive(Debug, Deserialize)]
struct PointReq {
    lat: f64,
    lon: f64,
}

#[derive(Debug, Deserialize)]
struct BatchReq {
    points: Vec<PointReq>,
}

#[derive(Debug, Serialize)]
struct Attribution {
    found: bool,
    state: Option<String>,
    state_code: Option<String>,
    lga: Option<String>,
    ward: Option<String>,
    source: String,
}

#[derive(Debug, Serialize)]
struct BatchItem {
    lat: f64,
    lon: f64,
    found: bool,
    state: Option<String>,
    lga: Option<String>,
}

/// Great-circle distance in km.
fn haversine_km(lon1: f64, lat1: f64, lon2: f64, lat2: f64) -> f64 {
    const R: f64 = 6371.0088;
    let d_lat = (lat2 - lat1) * PI / 180.0;
    let d_lon = (lon2 - lon1) * PI / 180.0;
    let la1 = lat1 * PI / 180.0;
    let la2 = lat2 * PI / 180.0;
    let a = (d_lat / 2.0).sin().powi(2) + la1.cos() * la2.cos() * (d_lon / 2.0).sin().powi(2);
    2.0 * R * a.sqrt().asin()
}

/// Ray-casting point-in-polygon; ring vertices are (lon, lat).
fn point_in_polygon(lon: f64, lat: f64, ring: &[(f64, f64)]) -> bool {
    let n = ring.len();
    if n < 3 {
        return false;
    }
    let mut inside = false;
    let mut j = n - 1;
    for i in 0..n {
        let (xi, yi) = ring[i];
        let (xj, yj) = ring[j];
        if (yi > lat) != (yj > lat) {
            let x_int = (xj - xi) * (lat - yi) / (yj - yi) + xi;
            if lon < x_int {
                inside = !inside;
            }
        }
        j = i;
    }
    inside
}

fn parse_features(raw: &str, level: &str) -> Vec<Feature> {
    let v: serde_json::Value = serde_json::from_str(raw).expect("embedded geojson valid");
    let mut out = Vec::new();
    if let Some(features) = v.get("features").and_then(|f| f.as_array()) {
        for f in features {
            let props = &f["properties"];
            let coords = &f["geometry"]["coordinates"][0];
            let mut ring: Vec<(f64, f64)> = Vec::new();
            if let Some(arr) = coords.as_array() {
                for p in arr {
                    if let (Some(x), Some(y)) = (p[0].as_f64(), p[1].as_f64()) {
                        ring.push((x, y));
                    }
                }
            }
            if ring.len() < 3 {
                continue;
            }
            let n = ring.len() as f64;
            let cx = ring.iter().map(|p| p.0).sum::<f64>() / n;
            let cy = ring.iter().map(|p| p.1).sum::<f64>() / n;
            let _ = level;
            out.push(Feature {
                name: props["name"].as_str().unwrap_or("").to_string(),
                code: props["code"].as_str().unwrap_or("").to_string(),
                state: props["state"].as_str().unwrap_or("").to_string(),
                ring,
                centroid: (cx, cy),
            });
        }
    }
    out
}

struct Engine {
    states: Vec<Feature>,
    lgas: Vec<Feature>,
}

impl Engine {
    fn attribute(&self, lat: f64, lon: f64) -> Option<Attribution> {
        // haversine prefilter: sort states by centroid distance, check
        // nearest-first; a state polygon can be skipped when its centroid is
        // farther than the state's own bounding radius plus point distance.
        let mut order: Vec<&Feature> = self.states.iter().collect();
        order.sort_by(|a, b| {
            haversine_km(lon, lat, a.centroid.0, a.centroid.1)
                .partial_cmp(&haversine_km(lon, lat, b.centroid.0, b.centroid.1))
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        for st in order {
            if !point_in_polygon(lon, lat, &st.ring) {
                continue;
            }
            // nearest seed LGA centroid within 60km
            let mut best: Option<(&Feature, f64)> = None;
            for lga in &self.lgas {
                if lga.state != st.name {
                    continue;
                }
                let d = haversine_km(lon, lat, lga.centroid.0, lga.centroid.1);
                if best.map(|(_, bd)| d < bd).unwrap_or(true) {
                    best = Some((lga, d));
                }
            }
            let lga_name = match best {
                Some((lga, d)) if d <= 60.0 => Some(lga.name.clone()),
                _ => None,
            };
            return Some(Attribution {
                found: true,
                state: Some(st.name.clone()),
                state_code: Some(st.code.clone()),
                lga: lga_name,
                ward: None, // wards not in the [seed] dataset
                source: "geo-rs embedded-seed [seed]".to_string(),
            });
        }
        None
    }
}

async fn healthz() -> Json<serde_json::Value> {
    Json(serde_json::json!({"status":"ok","service":"geo-rs","version":"0.1.0"}))
}

async fn point(
    axum::extract::State(engine): axum::extract::State<std::sync::Arc<Engine>>,
    Json(req): Json<PointReq>,
) -> (StatusCode, Json<serde_json::Value>) {
    if !(-90.0..=90.0).contains(&req.lat) || !(-180.0..=180.0).contains(&req.lon) {
        return (
            StatusCode::BAD_REQUEST,
            Json(serde_json::json!({"type":"about:blank","title":"bad_request","status":400,"detail":"lat/lon out of range"})),
        );
    }
    match engine.attribute(req.lat, req.lon) {
        Some(att) => (StatusCode::OK, Json(serde_json::to_value(att).unwrap())),
        None => (
            StatusCode::OK,
            Json(serde_json::json!({"found":false,"state":null,"lga":null,"ward":null,"source":"geo-rs embedded-seed [seed]"})),
        ),
    }
}

async fn batch(
    axum::extract::State(engine): axum::extract::State<std::sync::Arc<Engine>>,
    Json(req): Json<BatchReq>,
) -> Json<serde_json::Value> {
    let mut results = Vec::with_capacity(req.points.len());
    for p in &req.points {
        let att = engine.attribute(p.lat, p.lon);
        results.push(BatchItem {
            lat: p.lat,
            lon: p.lon,
            found: att.is_some(),
            state: att.as_ref().and_then(|a| a.state.clone()),
            lga: att.as_ref().and_then(|a| a.lga.clone()),
        });
    }
    let count = results.len();
    Json(serde_json::json!({"results": results, "count": count}))
}

async fn boundaries(
    axum::extract::State(engine): axum::extract::State<std::sync::Arc<Engine>>,
    Path(level): Path<String>,
) -> Json<serde_json::Value> {
    let feats: Vec<&Feature> = match level.as_str() {
        "state" => engine.states.iter().collect(),
        "lga" => engine.lgas.iter().collect(),
        _ => vec![],
    };
    let names: Vec<serde_json::Value> = feats
        .iter()
        .map(|f| serde_json::json!({"name": f.name, "code": f.code, "state": f.state, "centroid": f.centroid}))
        .collect();
    Json(serde_json::json!({"level": level, "seed": true, "count": names.len(), "features": names}))
}

#[tokio::main]
async fn main() {
    let engine = std::sync::Arc::new(Engine {
        states: parse_features(STATES_GEOJSON, "state"),
        lgas: parse_features(LGAS_GEOJSON, "lga"),
    });
    let app = Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(healthz))
        .route("/v1/attribution/point", post(point))
        .route("/v1/attribution/batch", post(batch))
        .route("/v1/boundaries/{level}", get(boundaries))
        .with_state(engine);
    let port = std::env::var("PORT").unwrap_or_else(|_| "8100".to_string());
    let listener = tokio::net::TcpListener::bind(format!("0.0.0.0:{port}"))
        .await
        .expect("bind");
    println!("geo-rs listening on :{port}");
    axum::serve(listener, app).await.expect("serve");
}

// ---------------------------------------------------------------------------
// Smoke tests (pure functions, no network). Run: cargo test
// ---------------------------------------------------------------------------
#[cfg(test)]
mod tests {
    use super::*;

    fn unit_square() -> Vec<(f64, f64)> {
        vec![(0.0, 0.0), (10.0, 0.0), (10.0, 10.0), (0.0, 10.0), (0.0, 0.0)]
    }

    #[test]
    fn pip_inside_outside_square() {
        let ring = unit_square();
        assert!(point_in_polygon(5.0, 5.0, &ring), "centre must be inside");
        assert!(point_in_polygon(0.5, 9.5, &ring), "near-corner inside");
        assert!(!point_in_polygon(15.0, 5.0, &ring), "east outside");
        assert!(!point_in_polygon(5.0, -1.0, &ring), "south outside");
        assert!(!point_in_polygon(-0.1, 5.0, &ring), "west outside");
    }

    #[test]
    fn pip_degenerate_rings() {
        assert!(!point_in_polygon(0.0, 0.0, &[]), "empty ring");
        assert!(!point_in_polygon(0.0, 0.0, &[(1.0, 1.0), (2.0, 2.0)]), "<3 vertices");
    }

    #[test]
    fn pip_concave_polygon() {
        // L-shaped concave ring: the notch at (7.5, 7.5) is OUTSIDE.
        let ring = vec![
            (0.0, 0.0), (10.0, 0.0), (10.0, 5.0), (5.0, 5.0),
            (5.0, 10.0), (0.0, 10.0), (0.0, 0.0),
        ];
        assert!(point_in_polygon(2.5, 7.5, &ring), "upper leg inside");
        assert!(!point_in_polygon(7.5, 7.5, &ring), "concave notch outside");
    }

    #[test]
    fn haversine_known_distance() {
        // Lagos (3.3792, 6.5244) -> Abuja (7.4951, 9.0579) ≈ 540 km.
        let d = haversine_km(3.3792, 6.5244, 7.4951, 9.0579);
        assert!((d - 540.0).abs() < 20.0, "distance {d} not ≈ 540km");
        assert_eq!(haversine_km(1.0, 1.0, 1.0, 1.0), 0.0, "same point = 0");
    }
}
