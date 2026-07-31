"""Smart audit-case auto-bundling (REAL logic; evidence references are
call-site supplied).

GNN ring detections for the same taxpayer cluster produce one bundled audit
case instead of N flat alerts. Bundling is deterministic: detections are
grouped by connected component over shared members, and each bundle carries
the shared-evidence references (ring id, member tin_hashes, rule packs seen,
model versions) so the audit-evidence service can seal the assembly as one
TAT.
"""
from __future__ import annotations

import hashlib
import time
from typing import Any


def _components(detections: list[dict]) -> list[list[dict]]:
    """Union-find over detections linked by shared ring members."""
    parent = list(range(len(detections)))

    def find(i: int) -> int:
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    def union(i: int, j: int) -> None:
        parent[find(i)] = find(j)

    by_member: dict[str, int] = {}
    for i, d in enumerate(detections):
        for m in d.get("members", []):
            if m in by_member:
                union(i, by_member[m])
            else:
                by_member[m] = i
    groups: dict[int, list[dict]] = {}
    for i, d in enumerate(detections):
        groups.setdefault(find(i), []).append(d)
    return list(groups.values())


def bundle_audit_cases(detections: list[dict[str, Any]], case_prefix: str = "case",
                       model_versions: dict[str, str] | None = None,
                       rule_packs: list[str] | None = None) -> dict[str, Any]:
    """Bundle ring detections into audit cases.

    Each detection: {"ring_id": str, "members": [tin_hash...],
                     "ring_probability": float, "evidence_refs": [str...]}.
    Returns {"cases": [...], "detection_count": n, "bundle_count": m}.
    """
    if not detections:
        return {"cases": [], "detection_count": 0, "bundle_count": 0}
    cases = []
    for group in _components(detections):
        members = sorted({m for d in group for m in d.get("members", [])})
        rings = sorted({str(d.get("ring_id")) for d in group if d.get("ring_id")})
        evidence = sorted({e for d in group for e in d.get("evidence_refs", [])})
        max_p = max(float(d.get("ring_probability") or 0.0) for d in group)
        seed = "|".join(rings) + "|" + "|".join(members)
        case_id = f"{case_prefix}-{hashlib.sha256(seed.encode()).hexdigest()[:16]}"
        severity = "high" if max_p >= 0.8 else "medium" if max_p >= 0.5 else "low"
        cases.append({
            "case_id": case_id,
            "kind": "audit.ring_bundle",
            "severity": severity,
            "max_ring_probability": round(max_p, 6),
            "ring_ids": rings,
            "member_tin_hashes": members,
            "detections": group,
            "shared_evidence": {
                "evidence_refs": evidence,
                "model_versions": model_versions or {},
                "rule_packs": rule_packs or [],
            },
            "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        })
    cases.sort(key=lambda c: (-c["max_ring_probability"], c["case_id"]))
    return {"cases": cases, "detection_count": len(detections), "bundle_count": len(cases)}
