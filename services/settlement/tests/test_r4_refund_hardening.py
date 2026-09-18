"""R4 regression tests: refund destination binding, fasttrack tenant/role
binding, and the standard-lane approval queue.

Findings fixed:
  S1a#3  core refunds paid a tin-derived internal account, never bound to
         the original payment source (NIP lane binds; core lane did not).
  S1a#1  /v1/refunds/fasttrack accepted ANY authenticated caller with ANY
         caller-supplied tin_hash (no role gate, no tenant binding).
  S1a#4  standard-lane refunds could never execute (approve 409'd lanes
         != manual_review); no maker!=checker enforcement.
"""
from __future__ import annotations

import os

os.environ.setdefault("AUTH_MODE", "dev")
os.environ.setdefault("DATA_DIR", "/tmp/settlement-test-r4-hardening")

import shutil  # noqa: E402

from fastapi.testclient import TestClient  # noqa: E402

shutil.rmtree(os.environ["DATA_DIR"], ignore_errors=True)

from app.main import app, _store  # noqa: E402

c = TestClient(app)
OP = {"X-Dev-Role": "operator", "X-Tenant-ID": "tenant-a"}
OP_B = {"X-Dev-Role": "operator", "X-Tenant-ID": "tenant-b"}
ADMIN = {"X-Dev-Role": "admin", "X-Tenant-ID": "tenant-a"}
AUDITOR = {"X-Dev-Role": "auditor", "X-Tenant-ID": "tenant-a"}


def _seed_profile(tin_hash: str, score: int = 800) -> None:
    _store.put("taxpayer_credit_profiles", tin_hash, {
        "tin_hash": tin_hash, "credit_score": score,
        "filings_on_time": 10, "filings_total": 10})


def _bind_tenant(tin_hash: str, tenant: str) -> None:
    _store.put("tenant_tins", tin_hash, {"tin_hash": tin_hash, "tenant_id": tenant})


def _bind_source(tin_hash: str, period: str, tax_type, account: str) -> None:
    from app.refund_execution import payment_source_key
    _store.put("payment_sources", payment_source_key(tin_hash, period, tax_type), {
        "tin_hash": tin_hash, "period": period, "tax_type": tax_type,
        "account_id": account, "source": "test"})


def test_fasttrack_requires_operator_role():
    _seed_profile("tin-role-gate")
    r = c.post("/v1/refunds/fasttrack", headers=AUDITOR, json={
        "tin_hash": "tin-role-gate", "amount_kobo": 100_000, "period": "2026-01"})
    assert r.status_code == 403, r.text


def test_fasttrack_cross_tenant_rejected():
    _seed_profile("tin-xtenant")
    _bind_tenant("tin-xtenant", "tenant-a")
    r = c.post("/v1/refunds/fasttrack", headers=OP_B, json={
        "tin_hash": "tin-xtenant", "amount_kobo": 100_000, "period": "2026-01"})
    assert r.status_code == 403, r.text


def test_auto_refund_without_source_binding_demoted_to_manual_review():
    """No original payment source on file: auto_approve must NOT post money
    to an unbound destination — the lane is demoted to manual review."""
    _seed_profile("tin-nobind")
    _bind_tenant("tin-nobind", "tenant-a")
    r = c.post("/v1/refunds/fasttrack", headers=OP, json={
        "tin_hash": "tin-nobind", "amount_kobo": 100_000, "period": "2026-02"})
    assert r.status_code == 200, r.text
    doc = r.json()
    assert doc["lane"] == "manual_review", doc
    assert "execution" not in doc or doc.get("execution", {}).get("status") != "posted"
    assert any("payment source" in reason for reason in doc["reasons"])


def test_auto_refund_pays_bound_source_account():
    _seed_profile("tin-bound")
    _bind_tenant("tin-bound", "tenant-a")
    _bind_source("tin-bound", "2026-03", None, "0000000f0000000100000000000000aa")
    r = c.post("/v1/refunds/fasttrack", headers=OP, json={
        "tin_hash": "tin-bound", "amount_kobo": 100_000, "period": "2026-03"})
    assert r.status_code == 200, r.text
    doc = r.json()
    assert doc["lane"] == "auto_approve", doc
    exe = doc["execution"]
    assert exe["status"] == "posted", exe
    assert exe["taxpayer_account"] == "0000000f0000000100000000000000aa"
    assert exe["destination_bound"] is True


def test_refund_to_non_original_destination_rejected():
    """The executor must refuse an execution whose destination account is
    not the bound original-payment source account."""
    from app.refund_execution import (RefundDestinationUnbound, RefundExecutor,
                                      ledger_from_env)
    exe = RefundExecutor(_store, ledger_from_env())
    _bind_source("tin-exec", "2026-04", "vat", "0000000f0000000100000000000000bb")
    try:
        exe.execute(tin_hash="tin-exec", period="2026-04", tax_type="vat",
                    amount_kobo=50_000, decision={"lane": "auto_approve"},
                    approved_by="test",
                    destination_account="0000000f00000001000000000000dead")
    except RefundDestinationUnbound:
        pass
    else:  # pragma: no cover
        raise AssertionError("unbound destination was not rejected")
    # no destination arg at all resolves the bound account
    out = exe.execute(tin_hash="tin-exec", period="2026-04", tax_type="vat",
                      amount_kobo=50_000, decision={"lane": "auto_approve"},
                      approved_by="test")
    assert out["taxpayer_account"] == "0000000f0000000100000000000000bb"


def test_standard_lane_lifecycle_completes():
    """Standard lane: queued -> approved (by a DIFFERENT operator) -> posted."""
    _seed_profile("tin-standard", score=800)
    _bind_tenant("tin-standard", "tenant-a")
    _bind_source("tin-standard", "2026-05", None, "0000000f0000000100000000000000cc")
    r = c.post("/v1/refunds/fasttrack", headers=OP, json={
        "tin_hash": "tin-standard", "amount_kobo": 3_000_000_000, "period": "2026-05"})  # >₦20m
    assert r.status_code == 200, r.text
    doc = r.json()
    assert doc["lane"] == "standard", doc
    rid = doc["refund_id"]
    # queue lists it
    q = c.get("/v1/refunds/queue?lane=standard", headers=ADMIN)
    assert q.status_code == 200, q.text
    assert any(d["refund_id"] == rid for d in q.json()["refunds"])
    # maker cannot approve their own refund
    r = c.post(f"/v1/refunds/{rid}/approve", headers=OP)
    assert r.status_code == 403, r.text
    # a different operator approves -> execution posts
    r = c.post(f"/v1/refunds/{rid}/approve", headers=ADMIN2)
    assert r.status_code == 200, r.text
    assert r.json()["execution"]["status"] == "posted", r.text


ADMIN2 = {"X-Dev-Role": "operator", "X-Tenant-ID": "tenant-a", "X-Dev-Sub": "operator-2"}


def test_standard_lane_reject():
    _seed_profile("tin-std-reject", score=800)
    _bind_tenant("tin-std-reject", "tenant-a")
    r = c.post("/v1/refunds/fasttrack", headers=OP, json={
        "tin_hash": "tin-std-reject", "amount_kobo": 3_000_000_000, "period": "2026-06"})
    rid = r.json()["refund_id"]
    r = c.post(f"/v1/refunds/{rid}/reject", headers=ADMIN2, json={"reason": "insufficient docs"})
    assert r.status_code == 200, r.text
    assert r.json()["decision"]["status"] == "rejected"
    # rejected refunds can no longer be approved
    r = c.post(f"/v1/refunds/{rid}/approve", headers=ADMIN)
    assert r.status_code == 409, r.text
