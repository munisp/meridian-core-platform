"""Ledger service client (api/ledger.yaml). Amounts are kobo."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel

from .client import Client


class Account(BaseModel):
    id: str
    ledger: int
    code: int
    flags: int = 0
    user_data: str = ""


class Balance(BaseModel):
    account_id: str
    debits_pending: int = 0
    debits_posted: int = 0
    credits_pending: int = 0
    credits_posted: int = 0
    posted_net: int = 0
    available: int = 0


class Transfer(BaseModel):
    id: str = ""
    debit_account_id: str
    credit_account_id: str
    amount: int
    ledger: int
    code: int
    pending: bool = False
    resolved: bool = False
    user_data: str = ""


class LedgerClient:
    def __init__(self, c: Client):
        self._c = c

    def create_accounts(self, accounts: list[Account], *, idempotency_key: Optional[str] = None) -> None:
        self._c.post("/v1/accounts",
                     {"accounts": [a.model_dump(mode="json") for a in accounts]},
                     idempotency_key=idempotency_key)

    def balance(self, account_id: str) -> Balance:
        return self._c.get(f"/v1/accounts/{account_id}/balance", model=Balance)

    def transfer(self, t: Transfer, *, idempotency_key: Optional[str] = None) -> Transfer:
        return self._c.post("/v1/transfers", t, model=Transfer, idempotency_key=idempotency_key)

    def transfer_pending(self, t: Transfer, *, idempotency_key: Optional[str] = None) -> Transfer:
        return self._c.post("/v1/transfers/pending", t, model=Transfer, idempotency_key=idempotency_key)

    def post_pending(self, transfer_id: str) -> Transfer:
        return self._c.post(f"/v1/transfers/{transfer_id}/post", model=Transfer)

    def void_pending(self, transfer_id: str) -> Transfer:
        return self._c.post(f"/v1/transfers/{transfer_id}/void", model=Transfer)
