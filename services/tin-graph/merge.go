// merge.go — TIN dedup/merge lifecycle HTTP API (M2).
//
// Officer-role gated (nrs:officer or admin, same scope as provision —
// audit M-3). Illegal lifecycle transitions answer 409 (RFC7807).
// Dedup detection only fills a review queue; merges are ALWAYS explicit
// officer decisions with reason + evidence_ref.
package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
)

// unmergeWindow is configurable via TIN_UNMERGE_WINDOW_HOURS (default 72h).
func unmergeWindow() time.Duration {
	if v := httpx.Env("TIN_UNMERGE_WINDOW_HOURS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Hour
		}
	}
	return 72 * time.Hour
}

func actorOf(r *http.Request) string {
	if c, ok := auth.FromContext(r.Context()); ok {
		return c.Sub
	}
	return ""
}

// lifecycleErr maps domain errors to RFC7807 statuses (409 on illegal).
func lifecycleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, graph.ErrNotFound):
		httpx.NotFound(w, "%v", err)
	case errors.Is(err, graph.ErrIllegalTransition),
		errors.Is(err, graph.ErrWindowExpired),
		errors.Is(err, graph.ErrTerminalMerge):
		httpx.Conflict(w, "%v", err)
	default:
		httpx.Internal(w, "%v", err)
	}
}

// mergeTINs handles POST /v1/tins/merge — moves relationships and
// registrations onto the surviving TIN, marks the merged record
// status=merged_into with an audit trail. Reversible via unmerge.
func (s *server) mergeTINs(w http.ResponseWriter, r *http.Request) {
	if !canAdministerTIN(r) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "role nrs:officer or admin required to merge TINs")
		return
	}
	var req struct {
		SurvivingTIN string `json:"surviving_tin"`
		MergedTIN    string `json:"merged_tin"`
		Reason       string `json:"reason"`
		EvidenceRef  string `json:"evidence_ref"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.SurvivingTIN == "" || req.MergedTIN == "" {
		httpx.BadRequest(w, "surviving_tin and merged_tin required")
		return
	}
	mr, err := graph.MergeTINs(s.st, graph.HashTIN(req.SurvivingTIN), graph.HashTIN(req.MergedTIN),
		req.Reason, req.EvidenceRef, actorOf(r), graph.NowRFC3339())
	if err != nil {
		lifecycleErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"merge": mr,
		"note":  "merged TIN is now status=merged_into and blocks new filings; reversible via POST /v1/tins/unmerge within " + unmergeWindow().String(),
	})
}

// unmergeTINs handles POST /v1/tins/unmerge — restores the pre-merge
// state inside the configurable window.
func (s *server) unmergeTINs(w http.ResponseWriter, r *http.Request) {
	if !canAdministerTIN(r) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "role nrs:officer or admin required to unmerge TINs")
		return
	}
	var req struct {
		MergedTIN string `json:"merged_tin"`
		Reason    string `json:"reason,omitempty"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.MergedTIN == "" {
		httpx.BadRequest(w, "merged_tin required")
		return
	}
	mr, err := graph.UnmergeTINs(s.st, graph.HashTIN(req.MergedTIN), actorOf(r), graph.NowRFC3339(), unmergeWindow())
	if err != nil {
		lifecycleErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"merge": mr, "note": "merge reversed; entities, registrations and lifecycle restored"})
}

// setLifecycle handles POST /v1/tins/{tin}/status — lifecycle transitions
// with effective dates; illegal transitions answer 409.
func (s *server) setLifecycle(w http.ResponseWriter, r *http.Request) {
	if !canAdministerTIN(r) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "role nrs:officer or admin required to change lifecycle status")
		return
	}
	var req struct {
		Status        string `json:"status"`
		EffectiveDate string `json:"effective_date,omitempty"` // YYYY-MM-DD; defaults to today
		Reason        string `json:"reason,omitempty"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	to, err := graph.ParseStatus(req.Status)
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	tinHash := graph.HashTIN(r.PathValue("tin"))
	rec, err := graph.Transition(s.st, tinHash, to, req.EffectiveDate, req.Reason, actorOf(r), graph.NowRFC3339())
	if err != nil {
		lifecycleErr(w, err)
		return
	}
	allowed, why := graph.FilingAllowed(rec.Status)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"lifecycle": rec, "filing_allowed": allowed, "filing_block_reason": why,
	})
}

// getLifecycle handles GET /v1/tins/{tin}/status.
func (s *server) getLifecycle(w http.ResponseWriter, r *http.Request) {
	rec, err := graph.LoadLifecycle(s.st, graph.HashTIN(r.PathValue("tin")))
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	allowed, why := graph.FilingAllowed(rec.Status)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"lifecycle": rec, "filing_allowed": allowed, "filing_block_reason": why,
	})
}

// filingEligibility handles GET /v1/tins/{tin}/filing-eligibility — the
// documented rule hook filings/assessment services call before accepting
// a new filing for a TIN.
func (s *server) filingEligibility(w http.ResponseWriter, r *http.Request) {
	rec, err := graph.LoadLifecycle(s.st, graph.HashTIN(r.PathValue("tin")))
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	allowed, why := graph.FilingAllowed(rec.Status)
	status := http.StatusOK
	if !allowed {
		status = http.StatusConflict
	}
	httpx.JSON(w, status, map[string]any{
		"tin_hash": rec.TINHash, "status": rec.Status, "filing_allowed": allowed, "reason": why,
	})
}

// dedupScan handles POST /v1/dedup/scan — fuzzy name + DOB/contact
// matching over all entities; results go to the review queue
// (collection dedup_queue, status pending_review). NEVER auto-merges.
func (s *server) dedupScan(w http.ResponseWriter, r *http.Request) {
	if !canAdministerTIN(r) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "role nrs:officer or admin required to run dedup scans")
		return
	}
	threshold := graph.DefaultDedupThreshold
	if v := httpx.Env("TIN_DEDUP_THRESHOLD", ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			threshold = f
		}
	}
	cands := graph.FindDedupCandidates(s.allEntities(), graph.MergedIntoSet(s.st), threshold)
	newQueued := 0
	for _, c := range cands {
		var existing graph.DedupCandidate
		if err := s.st.Get("dedup_queue", c.ID, &existing); err == nil {
			continue // already queued/dismissed — idempotent re-scan
		}
		if err := s.st.Put("dedup_queue", c.ID, c); err != nil {
			httpx.Internal(w, "%v", err)
			return
		}
		newQueued++
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"candidates": len(cands), "newly_queued": newQueued, "threshold": threshold,
		"note": "review queue only — dedup detection never auto-merges; officers merge via POST /v1/tins/merge with evidence",
	})
}

// dedupCandidates handles GET /v1/dedup/candidates — the review queue.
func (s *server) dedupCandidates(w http.ResponseWriter, r *http.Request) {
	if !canAdministerTIN(r) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "role nrs:officer or admin required to read the dedup queue")
		return
	}
	var queue []graph.DedupCandidate
	if err := s.st.ListInto("dedup_queue", &queue); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"candidates": queue, "count": len(queue)})
}
