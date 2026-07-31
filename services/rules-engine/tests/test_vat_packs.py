"""VAT pack tests (A4) + pack-integrity enforcement (A3) against the REAL
vendored consumer packs in rule-packs/consumer with the real lockfile pins
and ceremony signatures.
"""
import os
import sys

import pytest

yaml = pytest.importorskip("yaml")

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..", "..", "..")
sys.path.insert(0, os.path.join(REPO_ROOT, "services", "rules-engine"))
sys.path.insert(0, os.path.join(REPO_ROOT, "packages", "rulepack-schema"))

from app.evaluator import evaluate  # noqa: E402
from app.packloader import PackIntegrityError, PackLoader  # noqa: E402

CONSUMER = os.path.join(REPO_ROOT, "rule-packs", "consumer")
LOCK = os.path.join(REPO_ROOT, "rule-packs", "packs.lock.json")
KEYS = os.path.join(REPO_ROOT, "rule-packs", "signing_keys.json")


@pytest.fixture()
def loader():
    return PackLoader(packs_dir=CONSUMER, lock_path=LOCK, signing_keys_path=KEYS,
                      enforce=True)


def test_vat_standard_rate_750bps(loader):
    pack = loader.get("rp-vat-rates")
    r = evaluate(pack, {"supply_type": "standard", "filing_date": "2024-06-01",
                        "amount_kobo": 1_000_000_00})
    assert r["matched"] and r["decision"]["rate_bps"] == 750
    assert r["decision"]["amount_kobo"] == 75_000_00  # 7.5% of ₦1,000,000


def test_vat_legacy_rate_500bps_before_finance_act(loader):
    pack = loader.get("rp-vat-rates")
    r = evaluate(pack, {"supply_type": "standard", "filing_date": "2019-12-31",
                        "amount_kobo": 1_000_000_00})
    assert r["decision"]["rate_bps"] == 500
    assert r["decision"]["amount_kobo"] == 50_000_00


def test_vat_exempt_basket(loader):
    pack = loader.get("rp-vat-exempt-basket")
    r = evaluate(pack, {"category": "medical_services", "amount_kobo": 500_000_00})
    assert r["matched"] and r["decision"]["decision"] == "exempt"
    assert r["decision"]["amount_kobo"] == 0


def test_vat_zero_rated_basket(loader):
    pack = loader.get("rp-vat-zerorated-basket")
    for cat in ("basic_food", "non_oil_exports", "pharmaceuticals"):
        r = evaluate(pack, {"category": cat, "amount_kobo": 500_000_00})
        assert r["matched"] and r["decision"]["decision"] == "zero_rated", cat
        assert r["decision"]["rate_bps"] == 0


def test_all_consumer_packs_pass_integrity(loader):
    """Every published consumer pack verifies: sha256 pin + ed25519 sig."""
    packs = loader.list_loaded()
    ids = {p["id"] for p in packs}
    assert {"rp-vat-rates", "rp-vat-exempt-basket", "rp-vat-zerorated-basket",
            "rp-bank-thresholds", "rp-wht-2024"} <= ids


def test_tampered_pack_rejected(loader, tmp_path):
    import shutil

    d = tmp_path / "packs"
    shutil.copytree(CONSUMER, d)
    f = d / "rp-vat-rates" / "1.0.0.yaml"
    f.write_text(f.read_text().replace("rate_bps: 750", "rate_bps: 100", 1))
    tampered = PackLoader(packs_dir=str(d), lock_path=LOCK, signing_keys_path=KEYS,
                          enforce=True)
    with pytest.raises(PackIntegrityError):
        tampered.get("rp-vat-rates")


def test_unsigned_published_pack_rejected(loader, tmp_path):
    d = tmp_path / "packs" / "rp-evil"
    d.mkdir(parents=True)
    (d / "1.0.0.yaml").write_text(
        "id: rp-evil\nversion: 1.0.0\nstatus: published\nsigned: null\n"
        "rules:\n  - id: x\n    when: {a: 1}\n    then: {rate_bps: 1}\n")
    evil = PackLoader(packs_dir=str(tmp_path / "packs"), lock_path=LOCK,
                      signing_keys_path=KEYS, enforce=True)
    with pytest.raises(PackIntegrityError):
        evil.get("rp-evil")


def test_effective_date_pack_selection(tmp_path):
    """Backdated filings select the version in force at filing_date."""
    d = tmp_path / "packs" / "rp-demo"
    d.mkdir(parents=True)
    base = ("id: rp-demo\nversion: {v}\neffective_from: {ef}\nstatus: published\n"
            "signed: null\nrules:\n  - id: r\n    when: {{x: 1}}\n"
            "    then: {{rate_bps: {rate}}}\n")
    (d / "1.0.0.yaml").write_text(base.format(v="1.0.0", ef="2020-01-01", rate=500))
    (d / "2.0.0.yaml").write_text(base.format(v="2.0.0", ef="2021-01-01", rate=750))
    l = PackLoader(packs_dir=str(tmp_path / "packs"), enforce=False)
    old = l.get_for_date("rp-demo", "2020-06-01")
    new = l.get_for_date("rp-demo", "2022-06-01")
    assert str(old["version"]) == "1.0.0" and str(new["version"]) == "2.0.0"
