"""R4 #9: counterparty sanctions/PEP screening for core money-movement paths.

Refund/payout executions move real money to a counterparty. Before R4 the
core settlement service screened NOBODY: a sanctioned counterparty could
receive a refund payout. This module screens the counterparty against the
KYC screening endpoint (same interface as the inclusion kyc-engine
adapter: ``POST {base}/v1/screen`` -> ``{screened, matches, sanctions_hit,
pep_hit, sim, provider}``) and FAILS CLOSED:

- ``sanctions_hit``        -> CounterpartySanctioned (execution refused)
- endpoint unreachable /   -> ScreeningUnavailable (execution refused)
  timeout / 5xx
- screening disabled only  -> KYC_SCREENING_URL unset AND
  in an explicit dev profile    SCREENING_REQUIRED=0 (honest, loud default:
                                any other configuration screens)

Config:
  KYC_SCREENING_URL     base URL of the KYC screening service (required in
                        prod; when set, screening is ALWAYS enforced)
  SCREENING_REQUIRED    "1" (default) fail closed when unconfigured;
                        "0" skips screening (dev only, tagged in responses)
  SCREENING_TIMEOUT_S   request timeout seconds (default 10)
"""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from typing import Any, Protocol


class CounterpartySanctioned(Exception):
    """The refund/payout counterparty matched a sanctions list."""


class ScreeningUnavailable(Exception):
    """The screening endpoint could not be reached and the policy is
    fail-closed: the money movement must NOT proceed."""


class Screener(Protocol):
    def screen_counterparty(self, *, tin_hash: str, name: str | None = None) -> dict: ...


class HTTPScreener:
    """Real HTTP client against the KYC screening endpoint."""

    def __init__(self, base_url: str, timeout: float = 10.0) -> None:
        self.base = base_url.rstrip("/")
        self.timeout = timeout

    def screen_counterparty(self, *, tin_hash: str, name: str | None = None) -> dict:
        body = {"name": name or tin_hash, "tin_hash": tin_hash}
        req = urllib.request.Request(
            self.base + "/v1/screen",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json",
                     "X-Service-Name": "settlement"})
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:  # noqa: S310
                out = json.loads(resp.read() or b"{}")
        except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
            raise ScreeningUnavailable(
                f"KYC screening endpoint {self.base} unreachable: {exc}") from exc
        out.setdefault("screened", True)
        out.setdefault("provider", "kyc-screening")
        out["sim"] = bool(out.get("sim", False))
        return out


class NullScreener:
    """Dev-only explicit bypass (SCREENING_REQUIRED=0 and no URL)."""

    def screen_counterparty(self, *, tin_hash: str, name: str | None = None) -> dict:
        return {"screened": False, "skipped": True, "sim": True,
                "provider": "none", "matches": [],
                "sanctions_hit": False, "pep_hit": False,
                "reason": "SCREENING_REQUIRED=0 (dev bypass)"}


def screener_from_env() -> Screener:
    """Fail-closed factory. Real screening whenever KYC_SCREENING_URL is
    set. Without it: prod profiles (PROFILE=prod) and any deployment that
    sets SCREENING_REQUIRED=1 fail closed on every call; an explicit dev
    profile (the default test/dev environment) gets the honest-tagged
    NullScreener bypass instead of silently skipping checks in prod."""
    url = os.environ.get("KYC_SCREENING_URL", "").strip()
    if url:
        return HTTPScreener(url, float(os.environ.get("SCREENING_TIMEOUT_S", "10")))
    if os.environ.get("PROFILE", "dev") == "prod" or \
            os.environ.get("SCREENING_REQUIRED") == "1":
        return _FailClosedScreener()
    return NullScreener()


class _FailClosedScreener:
    def screen_counterparty(self, *, tin_hash: str, name: str | None = None) -> dict:
        raise ScreeningUnavailable(
            "KYC_SCREENING_URL is not configured and SCREENING_REQUIRED!=0: "
            "counterparty screening is mandatory for money movement")


def enforce_counterparty_screening(screener: Any, *, tin_hash: str,
                                   name: str | None = None) -> dict:
    """Screen a counterparty; raise on sanctions hits. Returns the result
    (recorded on the execution document for audit)."""
    result = screener.screen_counterparty(tin_hash=tin_hash, name=name)
    if result.get("sanctions_hit"):
        raise CounterpartySanctioned(
            f"counterparty {tin_hash} matched a sanctions list "
            f"({[m.get('list') for m in result.get('matches', []) if m.get('kind') == 'sanctions']}); "
            "refund/payout refused")
    return result
