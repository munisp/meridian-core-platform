"""I3 (REAL): refund fast-track lane.

Credit score + compliance history decide the refund lane:
  - auto_approve: credit_score >= AUTO_MIN_SCORE, compliance_ratio >= 0.9,
    amount <= AUTO_MAX_KOBO (₦5m default)
  - manual_review: amount <= REVIEW_MAX_KOBO and score >= REVIEW_MIN_SCORE
    (emits an nrs.refund.manual_review.v1 fallback event)
  - standard: everything else (full officer queue)

All caps are env-tunable rule constants; amounts are integer kobo.
"""
from __future__ import annotations

import os
import time

AUTO_MAX_KOBO = int(os.environ.get("REFUND_FASTTRACK_AUTO_MAX_KOBO", 500_000_000))    # ₦5m
REVIEW_MAX_KOBO = int(os.environ.get("REFUND_FASTTRACK_REVIEW_MAX_KOBO", 2_000_000_000))  # ₦20m
AUTO_MIN_SCORE = int(os.environ.get("REFUND_FASTTRACK_AUTO_MIN_SCORE", 700))
REVIEW_MIN_SCORE = int(os.environ.get("REFUND_FASTTRACK_REVIEW_MIN_SCORE", 550))
AUTO_MIN_COMPLIANCE = float(os.environ.get("REFUND_FASTTRACK_AUTO_MIN_COMPLIANCE", 0.9))


def compliance_ratio(filings_on_time: int, filings_total: int) -> float:
    if filings_total <= 0:
        return 0.0
    return min(max(filings_on_time / filings_total, 0.0), 1.0)


def decide_refund_lane(tin_hash: str, amount_kobo: int, credit_score: int,
                       filings_on_time: int, filings_total: int,
                       prior_breaks: int = 0, tax_type: str | None = None) -> dict:
    """Pure decision function — returns the lane decision document."""
    if amount_kobo <= 0:
        raise ValueError("amount_kobo must be positive")
    ratio = compliance_ratio(filings_on_time, filings_total)
    reasons: list[str] = []

    checks = {
        "amount_within_auto_cap": amount_kobo <= AUTO_MAX_KOBO,
        "amount_within_review_cap": amount_kobo <= REVIEW_MAX_KOBO,
        "credit_score_auto": credit_score >= AUTO_MIN_SCORE,
        "credit_score_review": credit_score >= REVIEW_MIN_SCORE,
        "compliance_auto": ratio >= AUTO_MIN_COMPLIANCE,
        "no_open_breaks": prior_breaks == 0,
    }
    if (checks["amount_within_auto_cap"] and checks["credit_score_auto"]
            and checks["compliance_auto"] and checks["no_open_breaks"]):
        lane = "auto_approve"
        reasons.append(f"score {credit_score} >= {AUTO_MIN_SCORE}, compliance {ratio:.2f} >= "
                       f"{AUTO_MIN_COMPLIANCE}, amount within ₦{AUTO_MAX_KOBO // 100:,}")
    elif checks["amount_within_review_cap"] and checks["credit_score_review"]:
        lane = "manual_review"
        if not checks["no_open_breaks"]:
            reasons.append(f"{prior_breaks} open reconciliation breaks")
        if not checks["compliance_auto"]:
            reasons.append(f"compliance {ratio:.2f} below {AUTO_MIN_COMPLIANCE}")
        if not checks["amount_within_auto_cap"]:
            reasons.append(f"amount above ₦{AUTO_MAX_KOBO // 100:,} auto cap")
    else:
        lane = "standard"
        if not checks["amount_within_review_cap"]:
            reasons.append(f"amount above ₦{REVIEW_MAX_KOBO // 100:,} review cap")
        if not checks["credit_score_review"]:
            reasons.append(f"credit score {credit_score} below {REVIEW_MIN_SCORE}")

    return {
        "type": "nrs.refund.fasttrack.v1",
        "tin_hash": tin_hash,
        "amount_kobo": amount_kobo,
        "tax_type": tax_type,
        "credit_score": credit_score,
        "compliance_ratio": round(ratio, 4),
        "prior_breaks": prior_breaks,
        "lane": lane,
        "reasons": reasons,
        "rule_caps": {
            "auto_max_kobo": AUTO_MAX_KOBO,
            "review_max_kobo": REVIEW_MAX_KOBO,
            "auto_min_score": AUTO_MIN_SCORE,
            "review_min_score": REVIEW_MIN_SCORE,
            "auto_min_compliance": AUTO_MIN_COMPLIANCE,
        },
        "decided_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
