"""Shared HTTP core for the Meridian SDK (httpx-based)."""

from __future__ import annotations

import uuid
from typing import Any, Optional, Type, TypeVar

import httpx
from pydantic import BaseModel

T = TypeVar("T", bound=BaseModel)


def new_idempotency_key() -> str:
    """Unique idempotency key for mutating calls."""
    return f"idem-{uuid.uuid4().hex}"


class MeridianError(Exception):
    """Problem-details error returned by a service."""

    def __init__(self, status: int, title: str, detail: str = ""):
        super().__init__(f"meridian: {status} {title}: {detail}")
        self.status = status
        self.title = title
        self.detail = detail


class Client:
    """Shared client core. Construct per service base URL.

    >>> ob = Client("http://localhost:8101").onboarding()
    """

    def __init__(
        self,
        base_url: str,
        *,
        dev_role: str = "operator",
        token: Optional[str] = None,
        timeout: float = 15.0,
        http: Optional[httpx.Client] = None,
    ):
        self.base_url = base_url.rstrip("/")
        self.dev_role = dev_role
        self.token = token
        self._http = http or httpx.Client(timeout=timeout)

    def _headers(self, idempotency_key: Optional[str]) -> dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self.dev_role:
            h["X-Dev-Role"] = self.dev_role
        if self.token:
            h["Authorization"] = f"Bearer {self.token}"
        if idempotency_key:
            h["Idempotency-Key"] = idempotency_key
        return h

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: Any = None,
        model: Optional[Type[T]] = None,
        idempotency_key: Optional[str] = None,
    ) -> Any:
        resp = self._http.request(
            method,
            self.base_url + path,
            json=json_body,
            headers=self._headers(idempotency_key),
        )
        if resp.status_code >= 300:
            title, detail = resp.reason_phrase, resp.text[:500]
            try:
                body = resp.json()
                title = body.get("title", title)
                detail = body.get("detail", detail)
            except Exception:
                pass
            raise MeridianError(resp.status_code, title, detail)
        if model is None:
            return resp.json() if resp.content else None
        return model.model_validate(resp.json())

    def get(self, path: str, *, model: Optional[Type[T]] = None) -> Any:
        return self.request("GET", path, model=model)

    def post(
        self,
        path: str,
        body: Any = None,
        *,
        model: Optional[Type[T]] = None,
        idempotency_key: Optional[str] = None,
    ) -> Any:
        if isinstance(body, BaseModel):
            body = body.model_dump(mode="json", exclude_none=True)
        return self.request("POST", path, json_body=body, model=model, idempotency_key=idempotency_key)

    # --- service accessors -------------------------------------------------
    def onboarding(self):
        from .onboarding import OnboardingClient

        return OnboardingClient(self)

    def tingraph(self):
        from .tingraph import TinGraphClient

        return TinGraphClient(self)

    def ledger(self):
        from .ledger import LedgerClient

        return LedgerClient(self)
