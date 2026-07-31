"""F2/F6/F9 funds-flow hardening tests for the settlement service."""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app, _store, _executor  # noqa: E402
from app.refund_execution import InprocLedger, refund_id, deterministic_id  # noqa: E402

H = {"X-Dev-Role": "operator"}


def _clear_open_breaks():
    """prior_breaks is now read server-side from the breaks store; earlier
    test modules leave open breaks behind, so clear them for refund tests."""
    for b in _store.list("breaks"):
        if b.get("status") in ("open", "investigating"):
            _store.put("breaks", b["id"], {**b, "status": "resolved"})


def _auto_approvable(tin="tin-r1", amount=300_000_000, period="2026-07"):
    return {"tin_hash": tin, "amount_kobo": amount, "credit_score": 800,
            "filings_on_time": 12, "filings_total": 12, "tax_type": "vat",
            "period": period}


# --- F2: refund execution ---

def test_auto_approve_executes_real_transfer():
    _clear_open_breaks()
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json=_auto_approvable())
        assert r.status_code == 200, r.text
        doc = r.json()
        assert doc["lane"] == "auto_approve"
        exe = doc["execution"]
        assert exe["status"] == "posted"
        rid = refund_id("tin-r1", "2026-07", "vat")
        assert exe["refund_id"] == rid
        # a REAL pending->post transfer pair exists on the ledger
        led: InprocLedger = _executor.ledger
        post = led.get_transfer(exe["post_transfer_id"])
        assert post is not None and post["amount_kobo"] == 300_000_000


def test_double_submit_one_transfer():
    _clear_open_breaks()
    with TestClient(app) as c:
        r1 = c.post("/v1/refunds/fasttrack", headers=H, json=_auto_approvable(tin="tin-dup"))
        r2 = c.post("/v1/refunds/fasttrack", headers=H, json=_auto_approvable(tin="tin-dup"))
        assert r1.json()["execution"]["refund_id"] == r2.json()["refund_id"]
        assert r2.json().get("idempotent_replay") is True
        rid = refund_id("tin-dup", "2026-07", "vat")
        exes = [e for e in _store.list("refund_executions") if e["refund_id"] == rid]
        assert len(exes) == 1
        led: InprocLedger = _executor.ledger
        posts = [t for t in led.transfers.values()
                 if t.get("credit") and t["amount_kobo"] == 300_000_000
                 and t["id"] == deterministic_id("ref-post:" + rid)]
        assert len(posts) == 1


def test_crash_after_pending_sweep_resumes():
    # simulate the crash: pending created + record persisted, no post
    doc = {"lane": "auto_approve"}
    exe = _executor.execute(tin_hash="tin-crash", period="2026-07", tax_type="vat",
                            amount_kobo=100_000_000, decision=doc, approved_by="test")
    assert exe["status"] == "posted"
    # craft a crashed execution by hand
    rid = refund_id("tin-crash2", "2026-07", "vat")
    led: InprocLedger = _executor.ledger
    from app.refund_execution import treasury_account, taxpayer_account
    tre, tax = treasury_account(), taxpayer_account("tin-crash2")
    led.ensure_account(tre, 1, 1, "nrs-refund-treasury")
    led.ensure_account(tax, 1, 0, "refund-taxpayer")
    led.accounts[tre]["credits_posted"] = 10_000_000_000  # funded treasury
    pend = deterministic_id("ref-pend:" + rid)
    led.create_pending({"id": pend, "debit": tre, "credit": tax, "amount_kobo": 50_000_000})
    _store.put("refund_executions", rid, {
        "refund_id": rid, "tin_hash": "tin-crash2", "period": "2026-07", "tax_type": "vat",
        "amount_kobo": 50_000_000, "status": "pending",
        "pending_transfer_id": pend, "post_transfer_id": deterministic_id("ref-post:" + rid)})
    res = _executor.sweep_pending()
    assert res["resumed"] == 1
    exe2 = _store.get("refund_executions", rid)
    assert exe2["status"] == "posted"
    # sweep again: nothing pending left
    assert _executor.sweep_pending()["resumed"] == 0


def test_post_failure_compensates_void():
    led: InprocLedger = _executor.ledger
    orig = led.post_pending_as
    def boom(pid, post_id, amount):
        raise ValueError("simulated ledger outage")
    led.post_pending_as = boom
    try:
        try:
            _executor.execute(tin_hash="tin-void", period="2026-07", tax_type="vat",
                              amount_kobo=10_000_000, decision={"lane": "auto_approve"},
                              approved_by="test")
            raise AssertionError("execution must fail")
        except ValueError:
            pass
    finally:
        led.post_pending_as = orig
    rid = refund_id("tin-void", "2026-07", "vat")
    exe = _store.get("refund_executions", rid)
    assert exe["status"] == "post_failed"
    pend = led.get_transfer(exe["pending_transfer_id"])
    assert pend["resolved"] is True and pend["pending"] is False  # voided


def test_manual_approve_endpoint_executes():
    _clear_open_breaks()
    with TestClient(app) as c:
        big = _auto_approvable(tin="tin-big", amount=1_200_000_000)  # ₦12m > ₦5m cap
        r = c.post("/v1/refunds/fasttrack", headers=H, json=big)
        assert r.json()["lane"] == "manual_review"
        assert "execution" not in r.json()
        rid = r.json()["refund_id"]
        r2 = c.post(f"/v1/refunds/{rid}/approve", headers=H)
        assert r2.status_code == 200, r2.text
        assert r2.json()["execution"]["status"] == "posted"
        # second approve replays (no double pay)
        r3 = c.post(f"/v1/refunds/{rid}/approve", headers=H)
        assert r3.json()["execution"].get("idempotent_replay") is True


# --- F6: fee-aware recon ---

def test_recon_fee_netted_match():
    with TestClient(app) as c:
        body = {
            "platform": [{"reference": "R1", "amount_kobo": 100000}],
            "pssp": [{"reference": "R1", "amount_kobo": 99000, "meta": {"fee_kobo": 1000}}],
            "treasury": [{"reference": "R1", "amount_kobo": 99000}],
        }
        r = c.post("/v1/recon/pssp/run", headers=H, json=body)
        assert r.status_code == 200, r.text
        run = r.json()["run"]
        assert run["matched"] == 1 and run["break_count"] == 0


def test_recon_unexplained_delta_still_breaks():
    with TestClient(app) as c:
        body = {
            "platform": [{"reference": "R2", "amount_kobo": 100000}],
            "pssp": [{"reference": "R2", "amount_kobo": 90000}],  # no fee declared
            "treasury": [{"reference": "R2", "amount_kobo": 90000}],
        }
        r = c.post("/v1/recon/pssp/run", headers=H, json=body)
        assert r.json()["run"]["break_count"] == 1


# --- F9: pull-mode recon + auto-heal + revenue dedup ---

def test_pull_mode_recon_auto_heal_and_revenue_dedup():
    with TestClient(app) as c:
        today = __import__("time").strftime("%Y-%m-%d")
        c.post("/v1/recon/ingest", headers=H, json={"side": "platform", "records": [
            {"reference": "P1", "amount_kobo": 50000, "date": today},
            {"reference": "P2", "amount_kobo": 70000, "date": today}]})
        c.post("/v1/recon/ingest", headers=H, json={"side": "treasury", "records": [
            {"reference": "P1", "amount_kobo": 50000},
            {"reference": "P2", "amount_kobo": 70000}]})
        # PSSP report only contains P1 -> P2 is ledger-captured, PSSP-missing
        c.post("/v1/recon/ingest", headers=H, json={"side": "pssp", "records": [
            {"reference": "P1", "amount_kobo": 50000}]})
        r = c.post("/v1/recon/pssp/pull-run", headers=H, json={"run_id": "pull-t1"})
        assert r.status_code == 200, r.text
        out = r.json()
        assert out["run"]["mode"] == "pull"
        # auto-heal: one investigation case for P2
        assert len(out["auto_healed"]) == 1
        case_id = out["auto_healed"][0]["case_id"]
        assert case_id == "case:P2"
        case = _store.get("investigation_cases", case_id)
        assert case["class"] == "ledger_captured_pssp_missing"
        # the break is marked investigating, not left open
        br = [b for b in out["breaks"] if b["reference"] == "P2"][0]
        assert br["status"] == "investigating"
        # P1 matched (present in all 3) -> revenue event, sim adapter honest tag
        assert out["run"]["adapter"] == "sim"
        # re-run with the same references resubmitted: revenue not double-counted
        before = len(_store.list("revenue_events"))
        r2 = c.post("/v1/recon/pssp/pull-run", headers=H, json={"run_id": "pull-t2"})
        assert r2.status_code == 200
        after = len(_store.list("revenue_events"))
        assert before == after, "re-submitted reference must not double-count revenue"
        # second auto-heal run dedupes the case
        cases = [k for k in _store.list("investigation_cases") if k["reference"] == "P2"]
        assert len(cases) == 1
