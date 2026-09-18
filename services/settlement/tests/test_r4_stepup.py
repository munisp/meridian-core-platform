"""R4-9c regression tests: TOTP step-up on settlement money routes.

Covers: money route without step-up -> 403 step_up_required (prod /
enrolled), valid TOTP proceeds, out-of-window code -> 401, unenrolled
admin in prod denied, dev bypass, enrollment confirm flow, recovery code
single-use, Go<->Python secret-format compatibility (RFC 6238 vector).
"""
from __future__ import annotations

import os

os.environ.setdefault("AUTH_MODE", "dev")
os.environ.setdefault("DATA_DIR", "/tmp/settlement-test-r4-stepup")
os.environ["STEPUP_SEAL_KEY"] = "test-seal"

import shutil  # noqa: E402

import pytest  # noqa: E402
from fastapi.testclient import TestClient  # noqa: E402

shutil.rmtree(os.environ["DATA_DIR"], ignore_errors=True)

from app import stepup  # noqa: E402
from app.main import app, _store  # noqa: E402

c = TestClient(app)
ADMIN = {"X-Dev-Role": "admin", "X-Tenant-ID": "tenant-a"}
OP = {"X-Dev-Role": "operator", "X-Tenant-ID": "tenant-a"}


def _enroll(sub: str = "dev-admin") -> tuple[str, list[str]]:
    out = stepup.enroll(_store, sub)
    assert stepup.confirm(_store, sub, stepup.totp_code(out["secret"]))
    return out["secret"], out["recovery_codes"]


def _wipe_enrollments():
    for k in list(_store.list("stepup_enrollments")):
        _store.delete("stepup_enrollments", k["actor"])


@pytest.fixture(autouse=True)
def _clean():
    _wipe_enrollments()
    yield
    # leave no step-up state behind for other test modules sharing _store
    _wipe_enrollments()
    os.environ.pop("PROFILE", None)


def test_rfc6238_vector_matches_go():
    # RFC 6238 App. B (SHA1): 59s -> 94287082 (8 digits) -> 6-digit "287082".
    # Same vector asserted in packages/stepup Go tests: shared format proof.
    assert stepup.totp_code("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", at=59) == "287082"


def test_money_route_without_stepup_forbidden_when_enrolled():
    _enroll("dev-operator")
    r = c.post("/v1/refunds/fasttrack", headers=OP, json={
        "tin_hash": "tin-su-1", "amount_kobo": 1000, "period": "2026-01"})
    assert r.status_code == 403 and "step_up_required" in r.text


def test_unenrolled_admin_prod_denied():
    os.environ["PROFILE"] = "prod"
    r = c.post("/v1/tenants/bind-tin", headers=ADMIN,
               json={"tin_hash": "tin-su-2", "tenant_id": "tenant-a"})
    assert r.status_code == 403 and "step_up_required" in r.text


def test_unenrolled_dev_bypass():
    os.environ["PROFILE"] = "dev"
    r = c.post("/v1/tenants/bind-tin", headers=ADMIN,
               json={"tin_hash": "tin-su-3", "tenant_id": "tenant-a"})
    assert r.status_code == 200, r.text


def test_valid_totp_proceeds_and_stale_rejected():
    secret, _ = _enroll("dev-admin")
    hdrs = {**ADMIN, "X-Stepup-Code": stepup.totp_code(secret)}
    r = c.post("/v1/tenants/bind-tin", headers=hdrs,
               json={"tin_hash": "tin-su-4", "tenant_id": "tenant-a"})
    assert r.status_code == 200, r.text
    # out-of-window code -> 401
    stale = stepup.totp_code(secret, at=__import__("time").time() - 120)
    if stale == stepup.totp_code(secret):  # paranoia: never equal across 4 steps
        stale = "000000" if stale != "000000" else "111111"
    hdrs2 = {**ADMIN, "X-Stepup-Code": stale}
    r2 = c.post("/v1/tenants/bind-tin", headers=hdrs2,
                json={"tin_hash": "tin-su-5", "tenant_id": "tenant-a"})
    assert r2.status_code == 401, r2.text


def test_enrollment_confirm_flow_over_http():
    r = c.post("/v1/stepup/enroll", headers=ADMIN)
    assert r.status_code == 200, r.text
    uri = r.json()["otpauth_uri"]
    assert uri.startswith("otpauth://totp/MeridianSettlement:dev-admin")
    assert len(r.json()["recovery_codes"]) == stepup.RECOVERY_COUNT
    secret = uri.split("secret=")[1].split("&")[0]
    # sealed at rest: raw secret must not appear in the stored record
    rec = _store.get("stepup_enrollments", "dev-admin")
    assert secret not in rec["sealed_secret"]
    assert not rec["enabled"]
    # bad confirm
    bad = c.post("/v1/stepup/confirm", headers=ADMIN, json={"code": "000000"})
    assert bad.status_code in (401, 422) or bad.status_code == 401
    # good confirm
    ok = c.post("/v1/stepup/confirm", headers=ADMIN,
                json={"code": stepup.totp_code(secret)})
    assert ok.status_code == 200, ok.text
    assert stepup.enrolled(_store, "dev-admin")
    # operator cannot enroll (admin-only)
    assert c.post("/v1/stepup/enroll", headers=OP).status_code == 403


def test_recovery_code_single_use():
    _, recovery = _enroll("dev-admin")
    h = {**ADMIN, "X-Stepup-Code": recovery[0]}
    r = c.post("/v1/tenants/bind-tin", headers=h,
               json={"tin_hash": "tin-su-6", "tenant_id": "tenant-a"})
    assert r.status_code == 200, r.text
    r2 = c.post("/v1/tenants/bind-tin", headers=h,
                json={"tin_hash": "tin-su-7", "tenant_id": "tenant-a"})
    assert r2.status_code == 401, r2.text
    # a different recovery code still works
    h2 = {**ADMIN, "X-Stepup-Code": recovery[1]}
    r3 = c.post("/v1/tenants/bind-tin", headers=h2,
                json={"tin_hash": "tin-su-8", "tenant_id": "tenant-a"})
    assert r3.status_code == 200, r3.text


def test_approve_requires_stepup():
    secret, _ = _enroll("dev-operator")
    checker_secret, _ = _enroll("dev-checker")
    CHECKER = {**OP, "X-Dev-Sub": "checker"}  # maker!=checker: distinct sub
    _store.put("taxpayer_credit_profiles", "tin-su-9", {
        "tin_hash": "tin-su-9", "credit_score": 600,
        "filings_on_time": 10, "filings_total": 10})
    _store.put("tenant_tins", "tin-su-9", {"tin_hash": "tin-su-9", "tenant_id": "tenant-a"})
    hdrs = {**OP, "X-Stepup-Code": stepup.totp_code(secret)}
    r = c.post("/v1/refunds/fasttrack", headers=hdrs, json={
        "tin_hash": "tin-su-9", "amount_kobo": 700_000_000, "period": "2026-02"})
    assert r.status_code == 200, r.text
    rid = r.json()["refund_id"]
    # approve without step-up -> 403; with -> proceeds (not 401/403)
    ra = c.post(f"/v1/refunds/{rid}/approve", headers=CHECKER)
    assert ra.status_code == 403 and "step_up_required" in ra.text
    rb = c.post(f"/v1/refunds/{rid}/approve",
                headers={**CHECKER, "X-Stepup-Code": stepup.totp_code(checker_secret)})
    assert rb.status_code not in (401, 403), rb.text
