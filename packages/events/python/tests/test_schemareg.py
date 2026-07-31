"""Tests for the schemareg registry, compat checks, DuckDB outbox, and the
legacy-envelope shim."""
import json
import os
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from meridian_events.envelope import new_envelope  # noqa: E402
from meridian_events.schemareg import (  # noqa: E402
    IncompatibleSchemaError, Registry, UnregisteredTopicError,
    ValidationFailedError)
from meridian_events.shim import (  # noqa: E402
    coerce_envelope, is_canonical_envelope, upgrade_legacy)


@pytest.fixture(scope="module")
def reg():
    return Registry()


def test_registry_loads_catalog(reg):
    topics = reg.topics()
    assert len(topics) >= 30
    for t in ("nrs.psm.payments.v1", "nrs.onb.ussd.v1", "nrs.ml.scored.v1",
              "nrs.feature.materialised.v1"):
        assert t in topics


def test_validate_ok(reg):
    reg.validate_data("nrs.psm.payments.v1", {
        "reference": "r1", "amount_kobo": 5000, "tin_hash": "a" * 64})


def test_validate_rejects_bad_type(reg):
    with pytest.raises(ValidationFailedError):
        reg.validate_data("nrs.psm.payments.v1", {"amount_kobo": "x"})


def test_validate_unregistered(reg):
    with pytest.raises(UnregisteredTopicError):
        reg.validate_data("nrs.nope.nothing.v1", {})


def test_validate_envelope(reg):
    env = new_envelope("nrs.psm.payments.v1", "svc-test",
                       {"reference": "r1", "amount_kobo": 1})
    reg.validate_envelope(env)
    bad = env.to_dict()
    bad["type"] = "bogus"
    with pytest.raises(ValidationFailedError):
        reg.validate_envelope(bad)


def test_compat(reg):
    v1 = {"type": "object", "required": ["a"],
          "properties": {"a": {"type": "string"}, "b": {"type": "integer", "enum": ["x", "y"]}}}
    reg.register("nrs.test.compat.v1", v1)
    reg.check_compatibility("nrs.test.compat.v1", {
        "type": "object", "required": ["a"],
        "properties": {"a": {"type": "string"}, "b": {"type": "integer", "enum": ["x", "y", "z"]},
                       "c": {"type": "string"}}})
    with pytest.raises(IncompatibleSchemaError):  # new required field
        reg.check_compatibility("nrs.test.compat.v1", {
            "type": "object", "required": ["a", "c"], "properties": {"a": {"type": "string"}}})
    with pytest.raises(IncompatibleSchemaError):  # type change
        reg.check_compatibility("nrs.test.compat.v1", {
            "type": "object", "required": ["a"], "properties": {"a": {"type": "integer"}}})


# -- legacy shim ---------------------------------------------------------------

def test_shim_detects_canonical():
    env = new_envelope("nrs.psm.payments.v1", "svc", {"a": 1}).to_dict()
    assert is_canonical_envelope(env)
    assert not is_canonical_envelope({"action": "onb.register"})
    assert not is_canonical_envelope({"type": "nrs.x.v1"})  # missing fields


def test_shim_upgrades_ussd_raw_map():
    raw = {"action": "onb.register", "session_id": "s-123", "msisdn": "08031234567",
           "name": "Ada"}
    env = upgrade_legacy("nrs.onb.ussd.v1", raw, source_hint="ussd-gateway")
    assert env.type == "nrs.onb.ussd.v1"
    assert len(env.id) == 26
    assert env.source == "ussd-gateway(legacy-shim)"
    assert env.data["upgraded_legacy"] is True
    assert "msisdn" not in env.data and len(env.data["msisdn_hash"]) == 64
    # deterministic id: same session_id -> same envelope id (dedup in bronze)
    env2 = upgrade_legacy("nrs.onb.ussd.v1", raw, source_hint="ussd-gateway")
    assert env2.id == env.id


def test_shim_coerce_passthrough():
    env = new_envelope("nrs.psm.payments.v1", "svc", {"a": 1}).to_dict()
    out = coerce_envelope("nrs.psm.payments.v1", env)
    assert out.id == env["id"]


# -- DuckDB outbox (feature-store pattern) -------------------------------------

def test_duckdb_outbox_roundtrip():
    duckdb = pytest.importorskip("duckdb")
    from meridian_events.outbox import DuckDBOutbox, OutboxRelay
    from meridian_events.bus import InprocBus

    db = duckdb.connect(":memory:")
    ob = DuckDBOutbox(db)
    env = new_envelope("nrs.feature.materialised.v1", "feature-store",
                       {"feature": "fv_x", "entities_written": 3})
    ob.append("nrs.feature.materialised.v1", env)
    pend = ob.pending(0)
    assert len(pend) == 1 and pend[0]["seq"] == 1
    bus = InprocBus()
    got = []
    bus.subscribe("nrs.feature.materialised.v1", lambda e: got.append(e))
    import tempfile
    with tempfile.TemporaryDirectory() as d:
        relay = OutboxRelay(ob, bus, d)
        assert relay.flush_once() == 1
        assert relay.flush_once() == 0  # checkpoint: no redelivery
    assert len(got) == 1
    assert got[0].data["feature"] == "fv_x"
