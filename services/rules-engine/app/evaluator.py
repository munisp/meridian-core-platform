"""Generic rp-* rule pack evaluator (SPEC 1.4 rule kinds).

Evaluates when/then rules against a context dict with a full decision
trace. Rule kinds: rate_bps, threshold, band, formula, decision_table.
Money is integer kobo only — never floats in outputs.
"""
from __future__ import annotations

import ast
import math
import operator
import re
from dataclasses import dataclass, field
from typing import Any

# ---------------------------------------------------------------------------
# when-clause matching
# ---------------------------------------------------------------------------

_OPS: dict[str, Any] = {
    "eq": operator.eq,
    "neq": operator.ne,
    "gt": operator.gt,
    "gte": operator.ge,
    "lt": operator.lt,
    "lte": operator.le,
    "in": lambda a, b: a in b,
    "nin": lambda a, b: a not in b,
    "contains": lambda a, b: b in a if a is not None else False,
    "matches": lambda a, b: bool(re.fullmatch(str(b), str(a))) if a is not None else False,
    "exists": lambda a, b: (a is not None) == bool(b),
}


def lookup(context: dict, path: str) -> Any:
    """Dotted-path lookup into the evaluation context."""
    cur: Any = context
    for part in path.split("."):
        if isinstance(cur, dict) and part in cur:
            cur = cur[part]
        else:
            return None
    return cur


@dataclass
class ConditionResult:
    key: str
    op: str
    expected: Any
    actual: Any
    ok: bool

    def to_dict(self) -> dict:
        return {"key": self.key, "op": self.op, "expected": self.expected,
                "actual": self.actual, "ok": self.ok}


def match_condition(context: dict, key: str, spec: Any) -> ConditionResult:
    actual = lookup(context, key)
    if isinstance(spec, dict) and spec and all(k in _OPS for k in spec):
        # operator object: {op: value, ...} — all operators must hold
        worst: ConditionResult | None = None
        for op, v in spec.items():
            try:
                ok = bool(_OPS[op](actual, v))
            except TypeError:
                ok = False
            res = ConditionResult(key, op, v, actual, ok)
            if not ok:
                return res
            worst = res
        return worst or ConditionResult(key, "eq", spec, actual, True)
    if isinstance(spec, list):
        ok = actual in spec
        return ConditionResult(key, "in", spec, actual, ok)
    ok = actual == spec
    return ConditionResult(key, "eq", spec, actual, ok)


def match_when(context: dict, when: dict) -> tuple[bool, list[ConditionResult]]:
    results = [match_condition(context, k, v) for k, v in (when or {}).items()]
    return all(r.ok for r in results), results


# ---------------------------------------------------------------------------
# safe formula evaluation (AST whitelist)
# ---------------------------------------------------------------------------

_ALLOWED_FUNCS = {
    "min": min, "max": max, "abs": abs, "round": round,
    "floor": math.floor, "ceil": math.ceil,
}
_BINOPS = {
    ast.Add: operator.add, ast.Sub: operator.sub, ast.Mult: operator.mul,
    ast.Div: operator.truediv, ast.FloorDiv: operator.floordiv,
    ast.Mod: operator.mod, ast.Pow: operator.pow,
}
_CMPOPS = {
    ast.Eq: operator.eq, ast.NotEq: operator.ne, ast.Gt: operator.gt,
    ast.GtE: operator.ge, ast.Lt: operator.lt, ast.LtE: operator.le,
}


class FormulaError(ValueError):
    pass


def _eval_node(node: ast.AST, variables: dict) -> Any:
    if isinstance(node, ast.Expression):
        return _eval_node(node.body, variables)
    if isinstance(node, ast.Constant):
        if isinstance(node.value, (int, float, bool, str)):
            return node.value
        raise FormulaError(f"disallowed constant {node.value!r}")
    if isinstance(node, ast.Name):
        if node.id in variables:
            return variables[node.id]
        raise FormulaError(f"unknown name {node.id!r}")
    if isinstance(node, ast.BinOp):
        op = _BINOPS.get(type(node.op))
        if op is None:
            raise FormulaError(f"disallowed operator {type(node.op).__name__}")
        return op(_eval_node(node.left, variables), _eval_node(node.right, variables))
    if isinstance(node, ast.UnaryOp):
        val = _eval_node(node.operand, variables)
        if isinstance(node.op, ast.USub):
            return -val
        if isinstance(node.op, ast.UAdd):
            return +val
        if isinstance(node.op, ast.Not):
            return not val
        raise FormulaError(f"disallowed unary {type(node.op).__name__}")
    if isinstance(node, ast.BoolOp):
        vals = [_eval_node(v, variables) for v in node.values]
        if isinstance(node.op, ast.And):
            return all(vals)
        if isinstance(node.op, ast.Or):
            return any(vals)
        raise FormulaError("disallowed boolop")
    if isinstance(node, ast.Compare):
        left = _eval_node(node.left, variables)
        for op_node, comp in zip(node.ops, node.comparators):
            op = _CMPOPS.get(type(op_node))
            if op is None:
                raise FormulaError("disallowed comparison")
            right = _eval_node(comp, variables)
            if not op(left, right):
                return False
            left = right
        return True
    if isinstance(node, ast.IfExp):
        return _eval_node(node.body if _eval_node(node.test, variables) else node.orelse, variables)
    if isinstance(node, ast.Call):
        if not isinstance(node.func, ast.Name) or node.func.id not in _ALLOWED_FUNCS:
            raise FormulaError("only min/max/abs/round/floor/ceil calls allowed")
        args = [_eval_node(a, variables) for a in node.args]
        return _ALLOWED_FUNCS[node.func.id](*args)
    raise FormulaError(f"disallowed syntax {type(node).__name__}")


def eval_formula(expression: str, variables: dict) -> Any:
    tree = ast.parse(expression, mode="eval")
    return _eval_node(tree, variables)


# ---------------------------------------------------------------------------
# rule outcomes
# ---------------------------------------------------------------------------

@dataclass
class TraceEntry:
    rule_id: str
    matched: bool
    conditions: list[ConditionResult] = field(default_factory=list)
    outcome: dict | None = None
    note: str = ""

    def to_dict(self) -> dict:
        return {
            "rule_id": self.rule_id,
            "matched": self.matched,
            "conditions": [c.to_dict() for c in self.conditions],
            "outcome": self.outcome,
            "note": self.note,
        }


def _amount(context: dict) -> int:
    for k in ("amount_kobo", "amount", "base_kobo", "value"):
        v = context.get(k)
        if isinstance(v, (int, float)):
            return int(v)
    return 0


def _apply_round(value: float, mode: str) -> int:
    if mode == "up":
        return math.ceil(value)
    if mode == "down":
        return math.floor(value)
    return int(round(value))  # nearest / none both land on int kobo


def apply_then(rule_id: str, then: dict, context: dict) -> dict:
    """Compute the outcome of a matched rule. All money is integer kobo."""
    out: dict[str, Any] = {"rule_id": rule_id}
    if "rate_bps" in then:
        rate = int(then["rate_bps"])
        base = _amount(context)
        out["kind"] = "rate_bps"
        out["rate_bps"] = rate
        out["base_kobo"] = base
        out["amount_kobo"] = (base * rate) // 10_000
    elif "threshold" in then:
        th = then["threshold"]
        actual = lookup(context, th["field"])
        op = _OPS[th["op"]]
        try:
            ok = bool(op(actual, th["value"]))
        except TypeError:
            ok = False
        out["kind"] = "threshold"
        out["field"] = th["field"]
        out["actual"] = actual
        out["threshold"] = {"op": th["op"], "value": th["value"]}
        out["passed"] = ok
        out["decision"] = th.get("decision_if_true" if ok else "decision_if_false",
                                 "pass" if ok else "fail")
    elif "band" in then:
        spec = then["band"]
        value = lookup(context, spec["field"])
        chosen = None
        if isinstance(value, (int, float)):
            for b in spec["bands"]:
                lo = b.get("min", 0)
                hi = b.get("max")
                if value >= lo and (hi is None or value < hi):
                    chosen = b
                    break
        out["kind"] = "band"
        out["field"] = spec["field"]
        out["value"] = value
        if chosen is None:
            out["decision"] = "no_band"
        else:
            out["band"] = chosen
            if "rate_bps" in chosen:
                base = _amount(context) or int(value)
                out["rate_bps"] = int(chosen["rate_bps"])
                out["base_kobo"] = base
                out["amount_kobo"] = (base * int(chosen["rate_bps"])) // 10_000
            if "fixed_amount" in chosen:
                out["amount_kobo"] = int(chosen["fixed_amount"])
            if chosen.get("label"):
                out["decision"] = chosen["label"]
    elif "formula" in then:
        spec = then["formula"]
        variables = {k: v for k, v in context.items() if isinstance(v, (int, float, bool))}
        # flatten one level of numeric dicts as dotted names -> name_key
        for k, v in context.items():
            if isinstance(v, dict):
                for kk, vv in v.items():
                    if isinstance(vv, (int, float, bool)):
                        variables[f"{k}_{kk}"] = vv
        result = eval_formula(spec["expression"], variables)
        out["kind"] = "formula"
        out["expression"] = spec["expression"]
        if isinstance(result, float):
            result = _apply_round(result, spec.get("round", "nearest"))
        out[spec["result_field"]] = result
        out["result_field"] = spec["result_field"]
    elif "decision_table" in then:
        spec = then["decision_table"]
        chosen: dict | None = None
        for row in spec["rows"]:
            ok, _ = match_when(context, row.get("match", {}))
            if ok:
                chosen = dict(row.get("output", {}))
                break
        if chosen is None:
            chosen = dict(spec.get("default") or {})
            out["note"] = "default row applied"
        out["kind"] = "decision_table"
        out["output"] = chosen
        if "rate_bps" in chosen:
            base = _amount(context)
            out["rate_bps"] = int(chosen["rate_bps"])
            out["base_kobo"] = base
            out["amount_kobo"] = (base * int(chosen["rate_bps"])) // 10_000
    elif "decision" in then:
        out["kind"] = "decision"
        out["decision"] = then["decision"]
    elif "set" in then:
        out["kind"] = "set"
    if "set" in then and "set" not in out:
        out["set"] = then["set"]
    # Carry an explicit decision label alongside computed kinds (e.g. a
    # zero-rated VAT rule sets both rate_bps: 0 and decision: zero_rated).
    if "decision" in then and "decision" not in out:
        out["decision"] = then["decision"]
    if "narrate" in then:
        out["narrate"] = then["narrate"]
    return out


def evaluate(pack: dict, context: dict) -> dict:
    """Evaluate a pack against a context. First matching rule wins; every
    rule is traced with per-condition reasons (audit defensibility)."""
    trace: list[TraceEntry] = []
    decision: dict | None = None
    for rule in pack.get("rules", []):
        rid = rule.get("id", "?")
        matched, conds = match_when(context, rule.get("when") or {})
        entry = TraceEntry(rule_id=rid, matched=matched, conditions=conds)
        if matched and decision is None:
            outcome = apply_then(rid, rule.get("then") or {}, context)
            entry.outcome = outcome
            decision = outcome
            entry.note = "selected"
        elif matched:
            entry.note = "matched but shadowed by earlier rule"
        trace.append(entry)
    return {
        "pack": f"{pack.get('id')}@{pack.get('version')}",
        "pack_status": pack.get("status"),
        "subject_to_regazette": pack.get("subject_to_regazette", False),
        "matched": decision is not None,
        "decision": decision,
        "trace": [t.to_dict() for t in trace],
    }
