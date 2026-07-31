"""Schema registry for nrs.* topics (audit I3/I8) — Python mirror of
packages/events/schemareg (Go). Loads the checked-in dev store
(packages/events/schemareg/schemas + topics.json) so dev/test needs no
external service. Uses the `jsonschema` package when installed, otherwise a
built-in validator covering the subset used by Meridian schemas (type,
required, properties, items, enum, additionalProperties, min/max(Length),
pattern).

Compatibility: check_compatibility enforces BACKWARD compatibility — a new
schema version must accept everything the previous version accepted.
"""
from __future__ import annotations

import json
import logging
import os
import re
from pathlib import Path
from typing import Any

from .envelope import Envelope

log = logging.getLogger("meridian.schemareg")

TOPIC_RE = re.compile(r"^nrs\.[a-z0-9._]+\.v\d+$")

try:  # optional, preferred validator
    import jsonschema as _jsonschema
except ImportError:  # pragma: no cover - depends on env
    _jsonschema = None


class UnregisteredTopicError(KeyError):
    pass


class ValidationFailedError(ValueError):
    pass


class IncompatibleSchemaError(ValueError):
    pass


def _default_store_dir() -> Path | None:
    env = os.environ.get("MERIDIAN_SCHEMAREG_DIR")
    if env:
        return Path(env)
    here = Path(__file__).resolve()
    for parent in here.parents:
        cand = parent / "packages" / "events" / "schemareg" / "schemas"
        if cand.is_dir():
            return cand
    return None


class Registry:
    """topic -> JSON Schema for the envelope `data` payload."""

    def __init__(self, store_dir: str | os.PathLike | None = None) -> None:
        self.schemas: dict[str, dict] = {}
        self.catalog: list[dict] = []
        d = Path(store_dir) if store_dir else _default_store_dir()
        if d is None or not d.is_dir():
            raise FileNotFoundError(
                "schemareg dev store not found; set MERIDIAN_SCHEMAREG_DIR")
        self.dir = d
        cat = json.loads((d.parent / "topics.json").read_text())
        self.catalog = cat["topics"]
        for entry in self.catalog:
            name = entry["schema"]
            if name == "generic":
                name = "nrs.generic.v1"
            schema = json.loads((d / f"{name}.schema.json").read_text())
            self.schemas[entry["topic"]] = schema

    # -- API ---------------------------------------------------------------
    def topics(self) -> list[str]:
        return sorted(self.schemas)

    def lookup(self, topic: str) -> dict | None:
        return self.schemas.get(topic)

    def register(self, topic: str, schema: dict, *, check_compat: bool = True) -> None:
        if not TOPIC_RE.match(topic):
            raise ValueError(f"invalid topic name {topic!r}")
        if check_compat and topic in self.schemas:
            self.check_compatibility(topic, schema)
        self.schemas[topic] = schema

    def validate_data(self, topic: str, data: Any) -> None:
        schema = self.schemas.get(topic)
        if schema is None:
            raise UnregisteredTopicError(topic)
        if _jsonschema is not None:
            try:
                _jsonschema.validate(data, schema)
            except _jsonschema.ValidationError as exc:
                raise ValidationFailedError(
                    f"{topic}: {exc.message} at $.{'.'.join(map(str, exc.path))}") from exc
            return
        problems = _validate_subset(schema, data, "$")
        if problems:
            raise ValidationFailedError(f"{topic}: " + "; ".join(problems))

    def validate_envelope(self, env: Envelope | dict) -> None:
        d = env.to_dict() if isinstance(env, Envelope) else env
        problems = []
        if len(str(d.get("id", ""))) != 26:
            problems.append("id must be a 26-char ULID")
        if not TOPIC_RE.match(str(d.get("type", ""))):
            problems.append(f"type {d.get('type')!r} does not match nrs.*.vN")
        if not d.get("source"):
            problems.append("source is required")
        if not d.get("time"):
            problems.append("time is required")
        if len(str(d.get("trace_id", ""))) != 32:
            problems.append("trace_id must be 32 hex chars")
        if problems:
            raise ValidationFailedError("; ".join(problems))
        self.validate_data(d["type"], d.get("data"))

    def check_compatibility(self, topic: str, candidate: dict) -> None:
        old = self.schemas.get(topic)
        if old is None:
            return
        problems = _compat_problems("", old, candidate)
        if problems:
            raise IncompatibleSchemaError(f"{topic}: " + "; ".join(problems))


# -- mini validator (fallback when `jsonschema` is not installed) -----------

def _type_ok(t: str, v: Any) -> bool:
    return {
        "object": lambda: isinstance(v, dict),
        "array": lambda: isinstance(v, list),
        "string": lambda: isinstance(v, str),
        "boolean": lambda: isinstance(v, bool),
        "integer": lambda: isinstance(v, int) and not isinstance(v, bool),
        "number": lambda: isinstance(v, (int, float)) and not isinstance(v, bool),
        "null": lambda: v is None,
    }.get(t, lambda: True)()


def _validate_subset(schema: dict, v: Any, path: str) -> list[str]:
    errs: list[str] = []
    enum = schema.get("enum")
    if enum and v not in enum:
        return [f"{path}: value {v!r} not in enum {enum}"]
    t = schema.get("type")
    if t is not None:
        types = t if isinstance(t, list) else [t]
        if not any(_type_ok(tt, v) for tt in types):
            return [f"{path}: expected type {t}, got {type(v).__name__}"]
    if isinstance(v, dict):
        for req in schema.get("required", []):
            if req not in v:
                errs.append(f"{path}: missing required field {req!r}")
        props = schema.get("properties", {})
        for name, pv in v.items():
            if name in props:
                errs += _validate_subset(props[name], pv, f"{path}.{name}")
            elif schema.get("additionalProperties") is False:
                errs.append(f"{path}: additional property {name!r} not allowed")
    elif isinstance(v, list):
        items = schema.get("items")
        if isinstance(items, dict):
            for i, e in enumerate(v):
                errs += _validate_subset(items, e, f"{path}[{i}]")
    elif isinstance(v, str):
        if "minLength" in schema and len(v) < schema["minLength"]:
            errs.append(f"{path}: shorter than minLength {schema['minLength']}")
        if "maxLength" in schema and len(v) > schema["maxLength"]:
            errs.append(f"{path}: longer than maxLength {schema['maxLength']}")
        if "pattern" in schema and not re.search(schema["pattern"], v):
            errs.append(f"{path}: does not match pattern {schema['pattern']!r}")
    elif isinstance(v, (int, float)) and not isinstance(v, bool):
        if "minimum" in schema and v < schema["minimum"]:
            errs.append(f"{path}: below minimum {schema['minimum']}")
        if "maximum" in schema and v > schema["maximum"]:
            errs.append(f"{path}: above maximum {schema['maximum']}")
    return errs


# -- backward-compatibility check -------------------------------------------

def _compat_problems(path: str, old: dict, new: dict) -> list[str]:
    out: list[str] = []
    old_req = set(old.get("required", []))
    for req in new.get("required", []):
        if req not in old_req:
            out.append(f"{path or 'schema'}: new required field {req!r}")
    for name, op in old.get("properties", {}).items():
        np = new.get("properties", {}).get(name)
        if np is None:
            continue
        if op.get("type") and np.get("type") and op["type"] != np["type"]:
            out.append(f"{path or 'schema'}: type of {name!r} changed "
                       f"{op['type']} -> {np['type']}")
        if op.get("enum") and np.get("enum"):
            for v in op["enum"]:
                if v not in np["enum"]:
                    out.append(f"{path or 'schema'}: enum of {name!r} narrowed")
        if "properties" in op or "properties" in np:
            out += _compat_problems(f"{path}.{name}", op, np)
    return out
