"""R4 #9 regression: core payout/refund counterparty sanctions screening.

Pre-fix these tests fail (no app.screening module; RefundExecutor.execute
moves money with NO counterparty screening). Post-fix: a sanctioned
counterparty is refused BEFORE any ledger movement, an unreachable
screening endpoint fails closed, and a clean counterparty proceeds with
the screening result recorded on the execution document.
"""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

import pytest  # noqa: E402

from app.main import app, _store, _executor  # noqa: E402
from app.refund_execution import (InprocLedger, RefundExecutor,  # noqa: E402
                                  refund_id, taxpayer_account)
from app.screening import (CounterpartySanctioned, NullScreener,  # noqa: E402
                           ScreeningUnavailable, _FailClosedScreener,
                           screener_from_env)

H = {"X-Dev-Role": "operator"}


def _profile(tin):
    _store.put("taxpayer_credit_profiles", tin, {
        "tin_hash": tin, "credit_score": 800,
        "filings_on_time": 12, "filings_total": 12})


class SanctionedScreener:
    def screen_counterparty(self, *, tin_hash, name=None):
        return {"screened": True, "provider": "test-list", "sim": False,
                "matches": [{"list": "OFAC-SDN", "kind": "sanctions",
                             "name": tin_hash, "score": 0.99}],
                "sanctions_hit": True, "pep_hit": False}


class DownScreener:
    def screen_counterparty(self, *, tin_hash, name=None):
        raise ScreeningUnavailable("kyc screening endpoint unreachable")


def test_sanctioned_counterparty_blocked_no_money_moves():
    """A refund whose counterparty matches a sanctions list must be
    refused BEFORE the pending transfer is created."""
    _profile("tin-sanctioned")
    exe = RefundExecutor(_store, _executor.ledger, screener=SanctionedScreener())
    with pytest.raises(CounterpartySanctioned):
        exe.execute(tin_hash="tin-sanctioned", period="2026-09", tax_type="vat",
                    amount_kobo=100_000, decision={"lane": "auto_approve"},
                    approved_by="test")
    rid = refund_id("tin-sanctioned", "2026-09", "vat")
    assert _store.get("refund_executions", rid) is None  # nothing persisted
    led: InprocLedger = _executor.ledger
    acct = led.accounts.get(taxpayer_account("tin-sanctioned"))
    assert acct is None or acct["credits_posted"] == 0  # no money moved


def test_screening_outage_fails_closed():
    """Fail-closed: when the screening endpoint is unreachable the refund
    must NOT proceed (no silent skip)."""
    _profile("tin-outage")
    exe = RefundExecutor(_store, _executor.ledger, screener=DownScreener())
    with pytest.raises(ScreeningUnavailable):
        exe.execute(tin_hash="tin-outage", period="2026-09", tax_type="vat",
                    amount_kobo=100_000, decision={"lane": "auto_approve"},
                    approved_by="test")
    rid = refund_id("tin-outage", "2026-09", "vat")
    assert _store.get("refund_executions", rid) is None


def test_prod_profile_without_screening_url_fails_closed():
    """Factory: PROFILE=prod without KYC_SCREENING_URL must produce a
    fail-closed screener (never a silent bypass)."""
    os.environ["PROFILE"] = "prod"
    try:
        s = screener_from_env()
        with pytest.raises(ScreeningUnavailable):
            s.screen_counterparty(tin_hash="tin-x")
    finally:
        os.environ.pop("PROFILE", None)


def test_clean_counterparty_proceeds_and_records_screening():
    _profile("tin-clean")
    exe = RefundExecutor(_store, _executor.ledger, screener=NullScreener())
    out = exe.execute(tin_hash="tin-clean", period="2026-09", tax_type="vat",
                      amount_kobo=100_000, decision={"lane": "auto_approve"},
                      approved_by="test")
    assert out["status"] == "posted"
    assert out["counterparty_screening"]["provider"] == "none"
    assert out["counterparty_screening"]["sanctions_hit"] is False


def test_endpoint_blocks_sanctioned_refund_5xx_or_4xx_not_posted():
    """End-to-end through the fasttrack endpoint: a sanctioned counterparty
    never reaches 'posted'."""
    _profile("tin-sanctioned-ep")
    orig = _executor.screener
    _executor.screener = SanctionedScreener()
    try:
        with TestClient(app) as c:
            r = c.post("/v1/refunds/fasttrack", headers=H, json={
                "tin_hash": "tin-sanctioned-ep", "amount_kobo": 100_000,
                "tax_type": "vat", "period": "2026-09"})
            assert r.status_code in (403, 409, 422, 502), r.text
            assert "posted" not in r.text
    finally:
        _executor.screener = orig
