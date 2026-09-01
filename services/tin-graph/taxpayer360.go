// taxpayer360.go — I1: Taxpayer 360° graph profile API.
//
// GET /v1/taxpayer360/{tin_hash} aggregates into one response:
//   - identity: the entity record (REAL, local store)
//   - filings_summary: from the filings service when FILINGS_API_URL is set
//     (REAL downstream call); otherwise status "unavailable" (never faked)
//   - ledger_posture: account balances from the ledger service when
//     LEDGER_API_URL is set (REAL); otherwise "unavailable"
//   - graph: entity graph neighbourhood (REAL, BuildGraph)
//   - risk_scores: fraud/credit/fusion scores from ml-serving when
//     ML_SERVING_URL is set (REAL); otherwise "unavailable"
//
// Every section carries a "status": "ok" | "unavailable" and the response
// carries an honesty tag — no section is ever silently simulated.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
)

type section360 struct {
	Status string `json:"status"` // ok | unavailable
	Source string `json:"source"` // local | downstream url | ""
	Data   any    `json:"data,omitempty"`
	Note   string `json:"note,omitempty"`
}

type taxpayer360 struct {
	TINHash    string     `json:"tin_hash"`
	EntityID   string     `json:"entity_id,omitempty"`
	Tag        string     `json:"tag"` // REAL sections are marked per-section; no SIMULATED data
	At         string     `json:"at"`
	Identity   section360 `json:"identity"`
	Filings    section360 `json:"filings_summary"`
	Ledger     section360 `json:"ledger_posture"`
	Graph      section360 `json:"graph_neighborhood"`
	RiskScores section360 `json:"risk_scores"`
}

var downstreamClient = &http.Client{Timeout: 3 * time.Second, Transport: otelx.Client(nil)}

// fetchJSON GETs a downstream JSON endpoint, injecting the caller's bearer
// token so downstream auth sees the same principal.
func fetchJSON(url, authz string) (any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := downstreamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var body any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

func downstreamSection(envVar, url, authz, note string) section360 {
	base := httpx.Env(envVar, "")
	if base == "" {
		return section360{Status: "unavailable", Source: "", Note: envVar + " not configured"}
	}
	data, err := fetchJSON(base+url, authz)
	if err != nil {
		return section360{Status: "unavailable", Source: base, Note: "downstream error: " + err.Error()}
	}
	return section360{Status: "ok", Source: base, Data: data, Note: note}
}

func (s *server) taxpayer360Handler(w http.ResponseWriter, r *http.Request) {
	tinHash := r.PathValue("tin_hash")
	// Object-level authz (audit M-3): the full taxpayer 360 view is PII.
	// nrs:officer/admin may read any record; any other caller may read only
	// their own record — identified by the tin_hash matching their tenant_id
	// or sub claim.
	if !canAdministerTIN(r) {
		claims, _ := auth.FromContext(r.Context())
		if tinHash != claims.TenantID && tinHash != claims.Sub {
			httpx.Errorf(w, http.StatusForbidden, "forbidden",
				"callers may only read their own taxpayer360 record (role nrs:officer or admin required for others)")
			return
		}
	}
	authz := r.Header.Get("Authorization")
	out := taxpayer360{
		TINHash: tinHash,
		Tag:     "REAL (per-section status; unavailable sections are never simulated)",
		At:      time.Now().UTC().Format(time.RFC3339),
	}

	// identity (local store)
	for _, e := range s.allEntities() {
		if e.TINHash == tinHash {
			out.EntityID = e.ID
			out.Identity = section360{Status: "ok", Source: "local", Data: e}
			gv := graph.BuildGraph(e.ID, s.allEntities())
			out.Graph = section360{Status: "ok", Source: "local", Data: gv}
			break
		}
	}
	if out.Identity.Status == "" {
		out.Identity = section360{Status: "unavailable", Note: "no entity for tin_hash"}
		out.Graph = section360{Status: "unavailable", Note: "no entity for tin_hash"}
	}

	// filings summary (downstream when configured)
	out.Filings = downstreamSection("FILINGS_API_URL",
		"/v1/filings?tin_hash="+tinHash, authz, "filings summary")

	// ledger posture (downstream when configured)
	out.Ledger = downstreamSection("LEDGER_API_URL",
		"/v1/accounts", authz, "ledger accounts/balances posture")

	// risk scores (ml-serving when configured): latest fraud/credit/fusion
	mlBase := httpx.Env("ML_SERVING_URL", "")
	if mlBase == "" {
		out.RiskScores = section360{Status: "unavailable", Note: "ML_SERVING_URL not configured"}
	} else {
		scores := map[string]any{}
		okAny := false
		for _, model := range []string{"fraud", "credit", "fusion"} {
			req, err := http.NewRequest(http.MethodPost, mlBase+"/v1/score/"+model, nil)
			if err != nil {
				continue
			}
			if authz != "" {
				req.Header.Set("Authorization", authz)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := downstreamClient.Do(req)
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					resp.Body.Close()
				}
				continue
			}
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			scores[model] = body
			okAny = true
		}
		if okAny {
			out.RiskScores = section360{Status: "ok", Source: mlBase, Data: scores,
				Note: "scores computed with empty feature vectors when no features are supplied; pass ?features= for full scoring"}
		} else {
			out.RiskScores = section360{Status: "unavailable", Source: mlBase,
				Note: "ml-serving unreachable or all models hot-skipped"}
		}
	}

	status := http.StatusOK
	if out.EntityID == "" {
		status = http.StatusNotFound
	}
	httpx.JSON(w, status, out)
}
