import os
import sys
from pathlib import Path

import pytest
import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.evaluator import evaluate, match_when, eval_formula, FormulaError  # noqa: E402

DEV_HEADERS = {"X-Dev-Role": "operator"}


def pack(rules):
    return {"id": "rp-test", "version": "1.0.0", "status": "published",
            "subject_to_regazette": True, "rules": rules}


class TestWhenMatching:
    def test_scalar_and_list(self):
        ok, conds = match_when({"a": 1, "b": "x"}, {"a": 1, "b": ["x", "y"]})
        assert ok and all(c.ok for c in conds)

    def test_operators(self):
        ok, _ = match_when({"n": 5}, {"n": {"gte": 5, "lt": 10}})
        assert ok
        ok, _ = match_when({"n": 11}, {"n": {"lt": 10}})
        assert not ok

    def test_dotted_path(self):
        ok, _ = match_when({"party": {"kind": "company"}}, {"party.kind": "company"})
        assert ok

    def test_exists_and_missing(self):
        ok, _ = match_when({}, {"tin": {"exists": False}})
        assert ok
        ok, _ = match_when({"tin": "1"}, {"tin": {"exists": True}})
        assert ok

    def test_matches_regex(self):
        ok, _ = match_when({"ref": "INV-2025-001"}, {"ref": {"matches": r"INV-\d{4}-\d+"}})
        assert ok


class TestRuleKinds:
    def test_rate_bps_kobo_math(self):
        p = pack([{"id": "r1", "when": {"t": "div"},
                   "then": {"rate_bps": 1000, "narrate": "10%"}}])
        res = evaluate(p, {"t": "div", "amount_kobo": 1_000_001})
        assert res["matched"]
        d = res["decision"]
        assert d["amount_kobo"] == 100_000  # floor, integer kobo
        assert d["rate_bps"] == 1000
        assert d["narrate"] == "10%"

    def test_threshold(self):
        p = pack([{"id": "r1", "when": {}, "then": {
            "threshold": {"field": "turnover", "op": "lte", "value": 100,
                          "decision_if_true": "exempt", "decision_if_false": "taxable"}}}])
        assert evaluate(p, {"turnover": 99})["decision"]["decision"] == "exempt"
        assert evaluate(p, {"turnover": 101})["decision"]["decision"] == "taxable"

    def test_band(self):
        p = pack([{"id": "r1", "when": {}, "then": {"band": {
            "field": "turnover",
            "bands": [
                {"min": 0, "max": 100, "rate_bps": 0, "label": "micro"},
                {"min": 100, "max": 500, "rate_bps": 100, "label": "small"},
                {"min": 500, "max": None, "fixed_amount": 7000, "label": "large"},
            ]}}}])
        r = evaluate(p, {"turnover": 50, "amount_kobo": 1000})
        assert r["decision"]["decision"] == "micro" and r["decision"]["amount_kobo"] == 0
        r = evaluate(p, {"turnover": 250, "amount_kobo": 1_000_000})
        assert r["decision"]["decision"] == "small" and r["decision"]["amount_kobo"] == 10_000
        r = evaluate(p, {"turnover": 900})
        assert r["decision"]["decision"] == "large" and r["decision"]["amount_kobo"] == 7000

    def test_formula(self):
        p = pack([{"id": "r1", "when": {}, "then": {
            "formula": {"expression": "min(amount * 0.2, cap)", "result_field": "relief_kobo",
                        "round": "nearest"}}}])
        r = evaluate(p, {"amount": 1_000_000, "cap": 500_000})
        assert r["decision"]["relief_kobo"] == 200_000
        r = evaluate(p, {"amount": 10_000_000, "cap": 500_000})
        assert r["decision"]["relief_kobo"] == 500_000

    def test_decision_table(self):
        p = pack([{"id": "r1", "when": {}, "then": {"decision_table": {
            "rows": [
                {"match": {"state": "lagos"}, "output": {"rate_bps": 150}},
                {"match": {"state": "kano"}, "output": {"rate_bps": 120}},
            ],
            "default": {"rate_bps": 100}}}}])
        r = evaluate(p, {"state": "lagos", "amount_kobo": 100_000})
        assert r["decision"]["amount_kobo"] == 1500
        r = evaluate(p, {"state": "oyo", "amount_kobo": 100_000})
        assert r["decision"]["amount_kobo"] == 1000

    def test_first_match_wins_and_trace(self):
        p = pack([
            {"id": "r1", "when": {"a": 1}, "then": {"decision": "first"}},
            {"id": "r2", "when": {"a": 1}, "then": {"decision": "second"}},
            {"id": "r3", "when": {"a": 2}, "then": {"decision": "third"}},
        ])
        res = evaluate(p, {"a": 1})
        assert res["decision"]["decision"] == "first"
        notes = {t["rule_id"]: t for t in res["trace"]}
        assert notes["r1"]["matched"] and notes["r1"]["note"] == "selected"
        assert notes["r2"]["matched"] and "shadowed" in notes["r2"]["note"]
        assert not notes["r3"]["matched"]
        assert res["pack"] == "rp-test@1.0.0"

    def test_no_match(self):
        p = pack([{"id": "r1", "when": {"a": 9}, "then": {"decision": "x"}}])
        res = evaluate(p, {"a": 1})
        assert not res["matched"] and res["decision"] is None


class TestFormulaSafety:
    def test_disallowed(self):
        with pytest.raises(FormulaError):
            eval_formula("__import__('os').system('id')", {})
        with pytest.raises(FormulaError):
            eval_formula("open('/etc/passwd')", {})

    def test_allowed_funcs(self):
        assert eval_formula("max(1, 2) + floor(1.7)", {}) == 3


class TestAPI:
    @pytest.fixture()
    def client(self, tmp_path, monkeypatch):
        from fastapi.testclient import TestClient
        from app.main import app, loader

        monkeypatch.setattr(loader, "packs_dir",
                            Path(__file__).resolve().parents[1] / "packs")
        with TestClient(app) as c:
            yield c

    def test_health(self, client):
        r = client.get("/healthz")
        assert r.status_code == 200 and r.json()["service"] == "rules-engine"

    def test_auth_required(self, client):
        assert client.post("/v1/evaluate", json={}).status_code in (401, 422)

    def test_evaluate_seed_pack(self, client):
        r = client.post("/v1/evaluate", headers=DEV_HEADERS, json={
            "pack_id": "rp-wht-2024", "version": "1.0.0",
            "context": {"payment_type": "dividend", "beneficiary": "company",
                        "amount_kobo": 5_000_000}})
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["decision"]["amount_kobo"] == 500_000
        assert body["subject_to_regazette"] is True
        assert len(body["trace"]) >= 5

    def test_notin_double_rate(self, client):
        r = client.post("/v1/evaluate", headers=DEV_HEADERS, json={
            "pack_id": "rp-wht-2024",
            "context": {"payment_type": "services", "has_tin": False,
                        "amount_kobo": 100_000}})
        assert r.json()["decision"]["rate_bps"] == 1000

    def test_smallco_carveout(self, client):
        r = client.post("/v1/evaluate", headers=DEV_HEADERS, json={
            "pack_id": "rp-wht-2024",
            "context": {"payment_type": "services", "beneficiary": "small_company",
                        "annual_turnover_kobo": 150_000_000, "amount_kobo": 100_000}})
        assert r.json()["decision"]["decision"] == "exempt"

    def test_list_packs(self, client):
        r = client.get("/v1/packs", headers=DEV_HEADERS)
        assert any(p["id"] == "rp-wht-2024" for p in r.json()["packs"])

    def test_missing_pack_404(self, client):
        r = client.post("/v1/evaluate", headers=DEV_HEADERS, json={
            "pack_id": "rp-nope", "context": {}})
        assert r.status_code == 404
