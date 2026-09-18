"""R4 contract-bridge regression: core's packloader enforces the ceremony
signature contract (meridian-ceremony-canonical-yaml/v1) — ed25519 over the
canonical YAML bytes the governance ceremony signs
(meridian-rule-packs tools/rpcommon.canonical_bytes, key_id
governance-board-2026-r2), not a JSON form and not a sha256 digest.

Fixture: rp-fmt-fct 1.0.0 copied byte-for-byte from munisp/meridian-rule-packs
(packs/rp-fmt-fct/1.0.0.yaml @ main), with its real ceremony signature.

Proves: (a) the real canonical pack verifies through core's loader;
(b) a one-byte tamper FAILS; (c) a wrong key_id FAILS.
"""
import os
import sys

import pytest

yaml = pytest.importorskip("yaml")

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..", "..", "..")
sys.path.insert(0, os.path.join(REPO_ROOT, "services", "rules-engine"))
sys.path.insert(0, os.path.join(REPO_ROOT, "packages", "rulepack-schema"))

from app.packloader import PackIntegrityError, PackLoader, canonical_pack_bytes  # noqa: E402

KEYS = os.path.join(REPO_ROOT, "rule-packs", "signing_keys.json")

# Real canonical pack artifact (signed block embedded), unmodified.
REAL_PACK_YAML = """\
# rp-fmt-fct v1.0.0 — Meridian rule pack (SPEC §1.4 format)
# Filing & management template — FCT (FCT-IRS administered taxes)
id: rp-fmt-fct
version: 1.0.0
title: Filing & management template — FCT (FCT-IRS administered taxes)
description: 'FCT filing matrix: FCT-IRS administers PIT for FCT residents, WHT on individuals, entertainment levy; federal
  presumptive default table applies (rp-presumptive-federal).'
effective_from: '2026-01-01'
effective_to: null
status: published
subject_to_regazette: true
provenance:
  as_passed: FCT Internal Revenue Service Act 2015; PITA; NTA/NTAA 2025
  as_gazetted: null
  source_citation: FCT-IRS Act 2015; PITA; Nigeria Tax Act 2025
rules:
- id: fmt.fct.paye
  when:
    tax: PIT_PAYE
    employer_territory: fct
  then:
    frequency: monthly
    deadline_day_of_month: 10
    narrate: PAYE for FCT-resident employees to FCT-IRS by 10th of following month.
- id: fmt.fct.wht-individuals
  when:
    tax: WHT
    beneficiary: individual
    territory: fct
  then:
    frequency: monthly
    deadline_day_of_month: 30
    narrate: WHT on individuals in FCT remitted to FCT-IRS by 30th of following month.
- id: fmt.fct.direct-assessment
  when:
    tax: PIT_direct_assessment
  then:
    deadline: 31 March
    narrate: Direct assessment filings by 31 March.
- id: fmt.fct.presumptive
  when:
    tax: presumptive
    territory: fct
  then:
    decision: use_federal_table
    narrate: FCT informal operators use rp-presumptive-federal band amounts.
signed:
  algorithm: ed25519
  key_id: governance-board-2026-r2
  signature: b011a0ada6e8f2dd7d16208bcc9cc95dfc8506590caa1773add1d6d9658f822135d4013551e39af13cd5eb1bd191d9f9c73009b9fefc5e003b16d9b7eff35a08
"""


@pytest.fixture()
def packs_dir(tmp_path):
    d = tmp_path / "packs" / "rp-fmt-fct"
    d.mkdir(parents=True)
    return d


def _loader(d):
    return PackLoader(packs_dir=str(d.parent), lock_path=str(d / "absent.lock.json"),
                      signing_keys_path=KEYS, enforce=True)


def test_real_ceremony_pack_verifies(packs_dir):
    (packs_dir / "1.0.0.yaml").write_text(REAL_PACK_YAML)
    pack = _loader(packs_dir).get("rp-fmt-fct", "1.0.0")
    assert pack["id"] == "rp-fmt-fct"
    assert pack["signed"]["key_id"] == "governance-board-2026-r2"


def test_canonical_bytes_match_ceremony_form(packs_dir):
    pack = yaml.safe_load(REAL_PACK_YAML)
    cb = canonical_pack_bytes(pack)
    # ceremony form: PyYAML safe_dump sort_keys, no signed block, UTF-8
    assert b"signed:" not in cb
    assert cb.startswith(b"description: ")  # sorted keys, first key
    assert cb.endswith(b"\n")
    # and the real signature verifies over these bytes via the loader path
    (packs_dir / "1.0.0.yaml").write_text(REAL_PACK_YAML)
    _loader(packs_dir).get("rp-fmt-fct", "1.0.0")  # raises on mismatch


def test_tampered_pack_rejected(packs_dir):
    tampered = REAL_PACK_YAML.replace("by 10th of following month",
                                      "by 11th of following month", 1)
    assert tampered != REAL_PACK_YAML
    (packs_dir / "1.0.0.yaml").write_text(tampered)
    with pytest.raises(PackIntegrityError, match="signature verification failed"):
        _loader(packs_dir).get("rp-fmt-fct", "1.0.0")


def test_wrong_key_id_rejected(packs_dir):
    swapped = REAL_PACK_YAML.replace("key_id: governance-board-2026-r2",
                                     "key_id: governance-board-1999", 1)
    (packs_dir / "1.0.0.yaml").write_text(swapped)
    with pytest.raises(PackIntegrityError, match="unknown signing key_id"):
        _loader(packs_dir).get("rp-fmt-fct", "1.0.0")
