"""Python validator for rp-* rule packs (SPEC 1.4).

Validates a pack dict (parsed from YAML) against the pack grammar. Uses
``jsonschema`` when installed against schema/rulepack.schema.json; otherwise
falls back to a hand-rolled structural validator identical to validate.go.
"""
from __future__ import annotations

import re
from pathlib import Path

ID_RE = re.compile(r"^rp-[a-z0-9][a-z0-9-]*$")
VERSION_RE = re.compile(r"^\d+\.\d+\.\d+$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
RULE_ID_RE = re.compile(r"^[a-z0-9][a-z0-9._-]*$")
SIG_RE = re.compile(r"^[0-9a-f]*$")
STATUSES = {"draft", "review", "simulation", "published", "retired"}
RULE_KINDS = ("rate_bps", "threshold", "band", "formula", "decision_table")
THEN_KEYS = set(RULE_KINDS) | {"decision", "set", "narrate"}

SCHEMA_PATH = Path(__file__).parent / "schema" / "rulepack.schema.json"


class PackValidationError(ValueError):
    """Raised when a pack fails validation; carries all problems."""

    def __init__(self, errors: list[str]):
        self.errors = errors
        super().__init__("; ".join(errors))


def _validate_structural(pack: dict) -> list[str]:
    errs: list[str] = []

    def add(msg: str) -> None:
        errs.append(msg)

    if not isinstance(pack, dict):
        return ["pack must be a mapping"]
    pid = pack.get("id")
    if not isinstance(pid, str) or not ID_RE.match(pid):
        add(f"id {pid!r} must match {ID_RE.pattern}")
    version = pack.get("version")
    if not isinstance(version, str) or not VERSION_RE.match(str(version)):
        add(f"version {version!r} must be semver X.Y.Z")
    eff_from = str(pack.get("effective_from", ""))[:10]
    if not DATE_RE.match(eff_from):
        add(f"effective_from {pack.get('effective_from')!r} must be YYYY-MM-DD")
    eff_to = pack.get("effective_to")
    if eff_to is not None and not DATE_RE.match(str(eff_to)[:10]):
        add(f"effective_to {eff_to!r} must be YYYY-MM-DD or null")
    if pack.get("status") not in STATUSES:
        add(f"status {pack.get('status')!r} not in {sorted(STATUSES)}")
    if not isinstance(pack.get("subject_to_regazette"), bool):
        add("subject_to_regazette must be a boolean")
    prov = pack.get("provenance")
    if not isinstance(prov, dict):
        add("provenance is required")
    else:
        if not prov.get("as_passed"):
            add("provenance.as_passed is required")
        if not prov.get("source_citation"):
            add("provenance.source_citation is required")
    signed = pack.get("signed")
    if signed is not None:
        if not isinstance(signed, dict):
            add("signed must be an object or null")
        else:
            if signed.get("algorithm") != "ed25519":
                add("signed.algorithm must be ed25519")
            if not signed.get("key_id"):
                add("signed.key_id is required")
            if not SIG_RE.match(str(signed.get("signature", ""))):
                add("signed.signature must be lowercase hex")
    rules = pack.get("rules")
    if not isinstance(rules, list) or not rules:
        add("rules must contain at least one rule")
        return errs
    seen: set[str] = set()
    for i, rule in enumerate(rules):
        if not isinstance(rule, dict):
            add(f"rules[{i}] must be an object")
            continue
        rid = rule.get("id")
        if not isinstance(rid, str) or not RULE_ID_RE.match(rid):
            add(f"rules[{i}].id {rid!r} must match {RULE_ID_RE.pattern}")
        elif rid in seen:
            add(f"rules[{i}].id {rid!r} duplicated")
        seen.add(str(rid))
        if "when" not in rule or not isinstance(rule["when"], dict):
            add(f"rules[{i}] ({rid}).when is required (use {{}} to match all)")
        then = rule.get("then")
        if not isinstance(then, dict):
            add(f"rules[{i}] ({rid}).then is required")
            continue
        kinds = [k for k in RULE_KINDS if k in then]
        if len(kinds) == 0 and "decision" not in then and "set" not in then:
            add(
                f"rules[{i}] ({rid}).then must contain a rule kind "
                "(rate_bps|threshold|band|formula|decision_table), decision or set"
            )
        if len(kinds) > 1:
            add(f"rules[{i}] ({rid}).then must contain exactly one rule kind, found {len(kinds)}")
        for k in then:
            if k not in THEN_KEYS:
                add(f"rules[{i}] ({rid}).then has unknown key {k!r}")
    return errs


def validate_pack(pack: dict, *, raise_on_error: bool = False) -> list[str]:
    """Validate a parsed pack; returns list of problems (empty = valid).

    Prefers the JSON Schema in schema/rulepack.schema.json when the
    ``jsonschema`` package is available; falls back to structural checks.
    """
    errors = _validate_structural(pack)
    # normalise YAML-parsed date objects to ISO strings for schema validation
    import datetime

    norm = dict(pack)
    for k in ("effective_from", "effective_to"):
        if isinstance(norm.get(k), (datetime.date, datetime.datetime)):
            norm[k] = norm[k].isoformat()[:10]
    try:  # pragma: no cover - depends on optional dep
        import json
        import jsonschema  # type: ignore

        schema = json.loads(SCHEMA_PATH.read_text())
        v = jsonschema.Draft202012Validator(schema)
        schema_errors = sorted(
            (f"schema: {e.message} at {'/'.join(map(str, e.absolute_path))}" for e in v.iter_errors(norm))
        )
        # union of both validators, deduped
        errors = sorted(set(errors) | set(schema_errors))
    except ImportError:
        pass
    if errors and raise_on_error:
        raise PackValidationError(errors)
    return errors


def validate_yaml(text: str, *, raise_on_error: bool = False) -> list[str]:
    """Parse YAML text and validate the pack."""
    import yaml

    pack = yaml.safe_load(text)
    return validate_pack(pack, raise_on_error=raise_on_error)


def validate_file(path: str | Path, *, raise_on_error: bool = False) -> list[str]:
    return validate_yaml(Path(path).read_text(), raise_on_error=raise_on_error)


def pack_ref(pack: dict) -> str:
    """Canonical pack reference rp-x@1.2.0 (SPEC 1.1)."""
    return f"{pack['id']}@{pack['version']}"
