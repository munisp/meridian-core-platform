"""R4 #10: core rule-pack consumer sync — rp-vat-rates re-synced to the
canonical pack from munisp/meridian-rule-packs main (blob 327d9247), with a
real sha256 pin in packs.lock.json and verify-on-load signature enforcement
via the rules-engine local-load path (PackLoader).

Contract note: PackLoader verifies the ceremony contract
(meridian-ceremony-canonical-yaml/v1 — canonical YAML bytes sans `signed`,
ed25519 over those bytes, key_id governance-board-2026). This is the
verification entrypoint bridged by PR #66; these tests exercise exactly
that entrypoint against the vendored consumer pack.

Tests:
- canonical pack loads under full integrity enforcement (pin + signature)
- the lockfile pin equals the recomputed canonical digest (drift guard)
- a tampered rule is rejected (pin mismatch)
- a tampered signature is rejected
- the NTA 2025 ₦100m VAT registration-threshold rules evaluate
"""
import hashlib
import json
import os
import shutil
import sys
import tempfile

import pytest

yaml = pytest.importorskip("yaml")

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..", "..", "..")
sys.path.insert(0, os.path.join(REPO_ROOT, "services", "rules-engine"))
sys.path.insert(0, os.path.join(REPO_ROOT, "packages", "rulepack-schema"))

from app.evaluator import evaluate  # noqa: E402
from app.packloader import (PackIntegrityError, PackLoader,  # noqa: E402
                            canonical_pack_bytes)

CONSUMER = os.path.join(REPO_ROOT, "rule-packs", "consumer")
LOCK = os.path.join(REPO_ROOT, "rule-packs", "packs.lock.json")
KEYS = os.path.join(REPO_ROOT, "rule-packs", "signing_keys.json")

N100M_KOBO = 10_000_000_000  # ₦100,000,000


@pytest.fixture()
def loader():
    return PackLoader(packs_dir=CONSUMER, lock_path=LOCK, signing_keys_path=KEYS,
                      enforce=True)


def _pack():
    return yaml.safe_load(
        open(os.path.join(CONSUMER, "rp-vat-rates", "1.0.0.yaml")).read())


def test_canonical_pack_loads_under_full_enforcement(loader):
    pack = loader.get("rp-vat-rates", "1.0.0")
    assert pack["id"] == "rp-vat-rates" and pack["status"] == "published"
    rule_ids = {r["id"] for r in pack["rules"]}
    # canonical content, incl. the NTA 2025 registration-threshold rules
    assert "vat.registration.threshold" in rule_ids
    assert "vat.registration.threshold-legacy" in rule_ids
    assert pack["signed"]["key_id"] == "governance-board-2026"


def test_lockfile_pin_is_real_and_matches_canonical_digest():
    pins = json.load(open(LOCK))["pins"]
    pin = pins["rp-vat-rates"]["sha256"]
    assert pin, "synced pack must not keep a sha256:null pin"
    digest = hashlib.sha256(canonical_pack_bytes(_pack())).hexdigest()
    assert pin == digest, "consumer pack drifted from its lockfile pin"


def test_tampered_rule_rejected():
    tmp = tempfile.mkdtemp()
    try:
        dst = os.path.join(tmp, "rp-vat-rates")
        os.makedirs(dst)
        pack = _pack()
        pack["rules"][0]["then"]["rate_bps"] = 900  # tamper: 7.5% -> 9%
        with open(os.path.join(dst, "1.0.0.yaml"), "w") as f:
            yaml.safe_dump(pack, f, sort_keys=False)
        l = PackLoader(packs_dir=tmp, lock_path=LOCK, signing_keys_path=KEYS,
                       enforce=True)
        with pytest.raises(PackIntegrityError):
            l.get("rp-vat-rates", "1.0.0")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def test_tampered_signature_rejected():
    tmp = tempfile.mkdtemp()
    try:
        dst = os.path.join(tmp, "rp-vat-rates")
        os.makedirs(dst)
        pack = _pack()
        # re-sign-shaped but forged signature over untampered content: the
        # pin still matches (signed block excluded), so ONLY the signature
        # check can catch this.
        sig = pack["signed"]["signature"]
        pack["signed"]["signature"] = ("0" if sig[0] != "0" else "1") + sig[1:]
        with open(os.path.join(dst, "1.0.0.yaml"), "w") as f:
            yaml.safe_dump(pack, f, sort_keys=False)
        l = PackLoader(packs_dir=tmp, lock_path=LOCK, signing_keys_path=KEYS,
                       enforce=True)
        with pytest.raises(PackIntegrityError):
            l.get("rp-vat-rates", "1.0.0")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def _rule(pack, rule_id):
    return next(r for r in pack["rules"] if r["id"] == rule_id)


def test_nta_100m_registration_threshold_rule(loader):
    """NTAA 2025 s.147: from 2026-01-01, registration triggers only ABOVE
    ₦100m annual taxable supplies (gt — exactly ₦100m stays exempt). The
    when-clause selects taxable persons from 2026 onward."""
    pack = loader.get("rp-vat-rates")
    rule = _rule(pack, "vat.registration.threshold")
    assert rule["effective_from"] == "2026-01-01"
    assert rule["when"] == {"person": "taxable", "date": {"gte": "2026-01-01"}}
    threshold = rule["then"]["threshold"]["annual_taxable_supplies_kobo"]
    assert threshold == {"gt": N100M_KOBO}
    # when-clause matching: 2026 supply matches, 2025 does not
    from app.evaluator import match_condition
    assert match_condition({"person": "taxable", "date": "2026-06-01"},
                           "date", {"gte": "2026-01-01"}).ok
    assert not match_condition({"person": "taxable", "date": "2025-12-31"},
                               "date", {"gte": "2026-01-01"}).ok


def test_legacy_25m_registration_threshold_rule(loader):
    pack = loader.get("rp-vat-rates")
    rule = _rule(pack, "vat.registration.threshold-legacy")
    assert rule["effective_to"] == "2025-12-31"
    assert rule["when"] == {"person": "taxable", "date": {"lt": "2026-01-01"}}
    threshold = rule["then"]["threshold"]["annual_taxable_supplies_kobo"]
    assert threshold == {"gte": 2_500_000_000}  # ₦25m


def test_standard_rate_rules_intact(loader):
    pack = loader.get("rp-vat-rates")
    r = evaluate(pack, {"supply_class": "standard", "date": "2024-06-01"})
    assert r["matched"] and r["decision"]["rate_bps"] == 750
    r = evaluate(pack, {"supply_class": "standard", "date": "2019-12-31"})
    assert r["matched"] and r["decision"]["rate_bps"] == 500
